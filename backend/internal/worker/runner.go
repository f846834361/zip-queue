package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	iofs "io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"zip-queue/internal/archive"
	"zip-queue/internal/fs"
	"zip-queue/internal/model"
	"zip-queue/internal/setting"
)

// TempDirPrefix 是任务执行期临时目录/临时文件名前缀。
// API 侧批量扫描目录时会跳过带此前缀的路径，避免扫到运行中任务的产物。
const TempDirPrefix = ".zq-tmp-"

// loadPasswords 按 sort_order 顺序加载全部已启用密码的值（停用密码跳过）。
func loadPasswords(db *gorm.DB) []string {
	var pws []model.Password
	if err := db.Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&pws).Error; err != nil {
		log.Printf("加载密码列表失败，本次执行将无法解密加密包：%v", err)
		return nil
	}
	out := make([]string, 0, len(pws))
	for _, p := range pws {
		out = append(out, p.Value)
	}
	return out
}

// loadCompressionLevel 读取压缩效率档位（任务开始时取一次，未设置回落默认档）。
// 与密码同样在任务执行时读库，使页面改配置只影响之后开始的任务。
func loadCompressionLevel(db *gorm.DB) archive.Level {
	return archive.ParseLevel(setting.Get(db, setting.KeyCompressionLevel))
}

// loadStripFolder 读取"去掉顶层文件夹"开关（任务开始时取一次，默认 false）。
// 与压缩效率同样执行期读库，使页面改配置只影响之后开始的任务。
func loadStripFolder(db *gorm.DB) bool {
	v, err := strconv.ParseBool(setting.Get(db, setting.KeyStripFolder))
	return err == nil && v
}

// loadAddFolderMode 读取"智能添加文件夹"模式（任务开始时取一次，未设置回落默认 1个）。
// 与压缩效率同样执行期读库，使页面改配置只影响之后开始的任务。
func loadAddFolderMode(db *gorm.DB) string {
	v := setting.Get(db, setting.KeyAddFolderMode)
	if !setting.ValidAddFolderMode(v) {
		return setting.AddFolderNone
	}
	return v
}

// shouldWrapFolder 按"智能添加文件夹"模式，结合解压出的顶层条目决定要不要包一层父文件夹。
//   - none：始终不包。
//   - multiple（多个）：仅当顶层条目数 > 1 才包；单个文件或单个文件夹都不包（避免文件夹嵌套）。
//   - one（1个）：多个条目必包；单个文件也包；仅当单个条目本身是文件夹时不包（避免套文件夹）。
func shouldWrapFolder(mode string, entries []os.DirEntry) bool {
	if mode == setting.AddFolderNone {
		return false
	}
	if len(entries) > 1 {
		return true
	}
	if len(entries) == 0 {
		return false
	}
	if entries[0].IsDir() {
		// 单个条目已是文件夹：包一层会变成文件夹套文件夹，故不包。
		return false
	}
	// 单个文件：one 包、multiple 不包。
	return mode == setting.AddFolderOne
}

// CancelChecker 供 Runner 判断某任务是否由用户主动取消（以区分服务中断重排）。
type CancelChecker interface {
	IsCancelled(taskID uint) bool
}

// Runner 执行单个任务（解压或压缩）的全部副作用：临时目录、进度落库、替换原文件、清理。
type Runner struct {
	db     *gorm.DB
	limits archive.Limits
	chk    CancelChecker
	// 分卷集互斥：key 为 dir+逻辑名，标记正在解压的分卷集，避免同一分卷集的多个任务并发执行。
	splitMu     sync.Mutex
	activeSplit map[string]bool
}

func NewRunner(db *gorm.DB, limits archive.Limits, chk CancelChecker) *Runner {
	return &Runner{db: db, limits: limits, chk: chk, activeSplit: make(map[string]bool)}
}

// Run 执行一个已处于 running 状态的任务。
func (r *Runner) Run(ctx context.Context, task *model.Task) {
	switch task.Type {
	case model.TypeDecompress:
		r.runDecompress(ctx, task)
	case model.TypeCompress:
		r.runCompress(ctx, task)
	case model.TypeDedup:
		r.runDedup(ctx, task)
	default:
		r.fail(task, "unknown task type: "+task.Type)
	}
}

func (r *Runner) runDecompress(ctx context.Context, task *model.Task) {
	src := task.SourcePath

	// 源文件存在性检查
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			r.fail(task, "源压缩包不存在或已被删除："+src)
		} else {
			r.fail(task, "无法访问源文件："+classifyError(err))
		}
		return
	}

	if !archive.IsSupportedArchive(src) {
		r.fail(task, classifyError(archive.ErrUnsupportedFormat))
		return
	}
	dir := filepath.Dir(src)
	// 枚举分卷：任意分卷入口都能定位全套；缺失/不连续会在此报错。
	vols, logicalName, isSplit, verr := archive.EnumerateVolumes(src)
	if verr != nil {
		r.fail(task, classifyError(verr))
		return
	}
	// 目标文件夹名取逻辑基名（剥掉 .zip/.7z 等容器扩展名），与单文件归档保持一致。
	base := archive.StripArchiveExt(logicalName)
	if base == "" {
		base = logicalName
	}
	if base == "" {
		base = "decompressed"
	}
	// target 是"加文件夹"时的父文件夹（以 zip 逻辑名命名），包文件夹时结果落在 dir/base。
	target := filepath.Join(dir, base)

	// 分卷集互斥：同一分卷集（不同卷入口）并发解压时只允许一个，其余直接失败让出槽位。
	if isSplit {
		splitKey := filepath.Join(dir, logicalName)
		r.splitMu.Lock()
		if r.activeSplit[splitKey] {
			r.splitMu.Unlock()
			r.fail(task, fmt.Sprintf("分卷 %s 正在被其他任务解压，已跳过", logicalName))
			return
		}
		r.activeSplit[splitKey] = true
		r.splitMu.Unlock()
		defer func() {
			r.splitMu.Lock()
			delete(r.activeSplit, splitKey)
			r.splitMu.Unlock()
		}()
	}
	addMode := loadAddFolderMode(r.db)

	// 目标冲突预检（在大量解压前快速失败）：加文件夹时检查 wrapper 目录；
	// 不加文件夹时条目名需解压后才知道，冲突检查放到移动前。
	if addMode != setting.AddFolderNone {
		if _, err := os.Stat(target); err == nil {
			r.fail(task, classifyTargetExists(target, false))
			return
		}
	}

	tempDir := filepath.Join(dir, fmt.Sprintf("%s%d", TempDirPrefix, task.ID))
	_ = os.RemoveAll(tempDir)
	r.setTempTarget(task, tempDir, target)

	if err := archive.Extract(ctx, src, tempDir, loadPasswords(r.db), r.limits, r.progressFn(task)); err != nil {
		if ctx.Err() != nil {
			if r.chk != nil && r.chk.IsCancelled(task.ID) {
				// 用户主动取消：清理临时文件并标记 cancelled（不重排）
				r.finishCancelled(task)
				return
			}
			// 服务停止/重启导致的中断：删除临时文件，可安全重做的补一条待执行任务
			FinishInterrupted(r.db, task)
			return
		}
		_ = os.RemoveAll(tempDir)
		r.fail(task, classifyError(err))
		return
	}

	// 解压完成：依据"智能添加文件夹"配置，决定是否把结果包一层父文件夹。
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		r.fail(task, classifyError(err))
		return
	}
	if !shouldWrapFolder(addMode, entries) {
		// 不加文件夹：把 tempDir 内顶层条目直接搬到 dir 下，并避免与已有条目冲突。
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				_ = os.RemoveAll(tempDir)
				r.fail(task, classifyTargetExists(filepath.Join(dir, e.Name()), false))
				return
			}
		}
		for _, e := range entries {
			if err := os.Rename(filepath.Join(tempDir, e.Name()), filepath.Join(dir, e.Name())); err != nil {
				_ = os.RemoveAll(tempDir)
				r.fail(task, classifyMoveError(err))
				return
			}
		}
		_ = os.RemoveAll(tempDir)
		// 记录最终落点供列表缓存增量更新：单条目回写其真实路径，多条目回写父目录。
		finalTarget := dir
		if len(entries) == 1 {
			finalTarget = filepath.Join(dir, entries[0].Name())
		}
		r.updateTask(task.ID, map[string]interface{}{"target_path": finalTarget}, "写入最终目标路径")
	} else {
		if err := os.Rename(tempDir, target); err != nil {
			_ = os.RemoveAll(tempDir)
			r.fail(task, classifyMoveError(err))
			return
		}
	}

	// 解压成功后删除全部分卷（分卷集所有卷；单文件解压时 vols 仅含源路径）
	var delErr error
	for _, v := range vols {
		if e := os.RemoveAll(v); e != nil {
			delErr = e
		}
	}
	if delErr != nil {
		r.fail(task, classifyDeleteError(delErr, model.TypeDecompress))
		return
	}
	r.succeed(task)
}

func (r *Runner) runCompress(ctx context.Context, task *model.Task) {
	src := task.SourcePath
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			r.fail(task, "源文件/文件夹不存在或已被删除："+src)
		} else {
			r.fail(task, "无法访问源文件："+classifyError(err))
		}
		return
	}
	dir := filepath.Dir(src)
	base := filepath.Base(src)
	if !info.IsDir() {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	target := filepath.Join(dir, base+".zip")
	if _, err := os.Stat(target); err == nil {
		r.fail(task, classifyTargetExists(target, true))
		return
	}
	tempZip := filepath.Join(dir, fmt.Sprintf("%s%d.zip", TempDirPrefix, task.ID))
	_ = os.RemoveAll(tempZip)
	r.setTempTarget(task, tempZip, target)

	// 任务开始执行时取一次压缩效率档位与是否去顶层文件夹（与密码同样执行期读库），
	// 并记录到任务，使任务详情可回显"当时实际使用的效率"，即使之后在配置页改动也不受影响。
	level := loadCompressionLevel(r.db)
	stripFolder := loadStripFolder(r.db)
	r.updateTask(task.ID, map[string]interface{}{"compression_level": string(level)}, "写入压缩效率档位")

	if err := archive.Compress(ctx, src, tempZip, level, stripFolder, setting.SkipCompressed(r.db), r.progressFn(task)); err != nil {
		if ctx.Err() != nil {
			if r.chk != nil && r.chk.IsCancelled(task.ID) {
				// 用户主动取消：清理临时文件并标记 cancelled（不重排）
				r.finishCancelled(task)
				return
			}
			// 服务停止/重启导致的中断：删除临时文件，可安全重做的补一条待执行任务
			FinishInterrupted(r.db, task)
			return
		}
		_ = os.RemoveAll(tempZip)
		r.fail(task, classifyError(err))
		return
	}
	if err := os.Rename(tempZip, target); err != nil {
		_ = os.RemoveAll(tempZip)
		r.fail(task, classifyMoveError(err))
		return
	}
	if err := os.RemoveAll(src); err != nil {
		r.fail(task, classifyDeleteError(err, model.TypeCompress))
		return
	}
	r.succeed(task)
}

// 查重任务的资源约束（内部固定默认值，不暴露配置）：
const (
	// dedupConcurrency 限制同时读取文件的 goroutine 数，避免打满磁盘 IO/CPU、影响宿主机其他进程。
	dedupConcurrency = 2
	// dedupSampleSize 采样哈希读取的头/尾字节数。
	dedupSampleSize = 64 << 10
	// dedupReadBuffer 读文件的定长缓冲：内存占用恒定，绝不整文件入内存。
	dedupReadBuffer = 1 << 20
)

// dedupFile 为一个待比对文件的元信息（不保存任何文件内容）。
type dedupFile struct {
	path string
	size int64
}

// hashVal 为一次哈希的结果；full 表示哈希已覆盖文件全部内容（采样即全量）。
type hashVal struct {
	hash string
	full bool
}

// dedupJob 为一次哈希任务（files 下标 + 路径）。
type dedupJob struct {
	idx  int
	path string
}

// runDedup 执行查重任务：三阶段递进淘汰，全程只读、可取消。
//  1. 按文件大小分组：大小唯一的文件必不重复，直接排除，不做任何读取；
//  2. 对同大小文件做"头+尾"采样哈希：淘汰头尾不同的不同文件；
//  3. 仅对采样哈希仍相同的候选做全量流式哈希，最终确认重复。
//
// 文件夹以其包含的全部文件身份集合为签名，签名相同（忽略目录层级）即判为重复文件夹。
func (r *Runner) runDedup(ctx context.Context, task *model.Task) {
	var sources []string
	if task.Sources != "" {
		if err := json.Unmarshal([]byte(task.Sources), &sources); err != nil {
			r.fail(task, "查重任务的输入路径无法解析："+err.Error())
			return
		}
	}
	if len(sources) == 0 {
		r.fail(task, "查重任务缺少输入路径")
		return
	}

	files, dirs, dirFiles, err := collectDedupFiles(ctx, sources)
	if err != nil {
		if ctx.Err() != nil {
			r.finishDedupInterrupt(task)
			return
		}
		r.fail(task, "扫描查重范围失败："+classifyError(err))
		return
	}

	tr := newDedupTracker(r, task.ID)

	// 阶段一：按大小分组，仅"同大小且 ≥2 个"才可能是重复，进入采样
	bySize := make(map[int64][]int, len(files))
	for i, f := range files {
		bySize[f.size] = append(bySize[f.size], i)
	}
	var sampleJobs []dedupJob
	var sampleTotal int64
	for _, idxs := range bySize {
		if len(idxs) < 2 {
			continue
		}
		for _, i := range idxs {
			sampleJobs = append(sampleJobs, dedupJob{idx: i, path: files[i].path})
			// 小文件采样即读全文，大文件只读头尾两段
			sampleTotal += minInt64(files[i].size, dedupSampleSize*2)
		}
	}

	// 阶段二：采样哈希（进度 0~80%）
	tr.reset(sampleTotal, 0, 80)
	sampleRes, err := runHashJobs(ctx, sampleJobs, files, func(ctx context.Context, f dedupFile) (hashVal, int64, error) {
		return sampleHash(ctx, f.path, f.size)
	}, tr.add)
	if err != nil {
		if ctx.Err() != nil {
			r.finishDedupInterrupt(task)
			return
		}
		r.fail(task, "采样哈希失败："+classifyError(err))
		return
	}

	// 阶段三：仅"同大小同采样哈希且 ≥2 个"的大文件需要全量哈希（进度 80~100%）
	fullJobs := dedupFullCandidates(files, sampleRes)
	fullRes := map[int]hashVal{}
	if len(fullJobs) > 0 {
		var fullTotal int64
		for _, j := range fullJobs {
			fullTotal += files[j.idx].size
		}
		tr.reset(fullTotal, 80, 20)
		fullRes, err = runHashJobs(ctx, fullJobs, files, func(ctx context.Context, f dedupFile) (hashVal, int64, error) {
			h, n, herr := fullHash(ctx, f.path, f.size)
			return hashVal{hash: h, full: true}, n, herr
		}, tr.add)
		if err != nil {
			if ctx.Err() != nil {
				r.finishDedupInterrupt(task)
				return
			}
			r.fail(task, "全量哈希失败："+classifyError(err))
			return
		}
	}

	fileGroups, wasted := dedupFileGroups(files, sampleRes, fullRes)

	// 文件夹判重：签名 = 所包含文件的身份集合，相同即重复（忽略目录层级）
	folderGroups := dedupFolders(dirs, dirFiles, files, sampleRes, fullRes)

	sortGroups(fileGroups)
	sortGroups(folderGroups)

	result := model.DedupResult{
		DuplicateFiles:   fileGroups,
		DuplicateFolders: folderGroups,
		WastedBytes:      wasted,
		TotalFiles:       len(files),
		TotalFolders:     len(dirs),
		CheckedBytes:     tr.readBytes(),
	}
	// 无重复时返回空数组而非 null，前端可直接使用 length
	if result.DuplicateFiles == nil {
		result.DuplicateFiles = []model.DedupGroup{}
	}
	if result.DuplicateFolders == nil {
		result.DuplicateFolders = []model.DedupGroup{}
	}
	blob, err := json.Marshal(result)
	if err != nil {
		r.fail(task, "查重结果序列化失败："+err.Error())
		return
	}
	r.succeedDedup(task, string(blob), result.CheckedBytes)
}

// collectDedupFiles 递归收集查重范围内待比对的文件：
// 文件直接入选，目录递归收集其下文件并记录归属（供文件夹签名判重）。
// 跳过空文件（无内容可比）与任务执行期临时目录。
//
// 每个文件在 files 中只收录一次（文件比对用），但会记入**每一个**包含它的勾选目录：
// 父子目录同时被勾选时，子目录也必须拿到完整成员列表，否则它的签名为空、
// 会被 dedupFolders 跳过，导致子目录无法参与文件夹判重。
// seen 记录 路径 -> 在 files 中的下标。
func collectDedupFiles(ctx context.Context, sources []string) ([]dedupFile, []string, map[string][]int, error) {
	var files []dedupFile
	var dirs []string
	dirFiles := make(map[string][]int)
	seen := make(map[string]int)
	for _, src := range sources {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		info, err := os.Stat(src)
		if err != nil {
			continue // 创建任务时已校验存在，此处忽略中途消失的路径
		}
		if !info.IsDir() {
			if info.Size() == 0 {
				continue
			}
			if _, dup := seen[src]; dup {
				continue
			}
			seen[src] = len(files)
			files = append(files, dedupFile{path: src, size: info.Size()})
			continue
		}
		dirs = append(dirs, src)
		werr := filepath.WalkDir(src, func(p string, d iofs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				// 跳过任务执行期的临时目录，避免扫到运行中任务的产物
				if strings.HasPrefix(d.Name(), TempDirPrefix) {
					return filepath.SkipDir
				}
				return nil
			}
			fi, ierr := d.Info()
			if ierr != nil || fi.Size() == 0 {
				return nil
			}
			// 已收录过的文件不重复入池，但仍要归属于当前这个勾选目录
			idx, ok := seen[p]
			if !ok {
				idx = len(files)
				seen[p] = idx
				files = append(files, dedupFile{path: p, size: fi.Size()})
			}
			dirFiles[src] = append(dirFiles[src], idx)
			return nil
		})
		if werr != nil {
			return nil, nil, nil, werr
		}
	}
	return files, dirs, dirFiles, nil
}

// runHashJobs 以受限并发执行哈希任务，返回 文件下标 -> 哈希结果。
// 只记录首个错误（个别文件读失败不应中断整体查重）；ctx 取消后立即停止派发。
func runHashJobs(ctx context.Context, jobs []dedupJob, files []dedupFile,
	hasher func(ctx context.Context, f dedupFile) (hashVal, int64, error),
	onRead func(int64)) (map[int]hashVal, error) {
	sem := make(chan struct{}, dedupConcurrency)
	var (
		mu       sync.Mutex
		res      = make(map[int]hashVal, len(jobs))
		firstErr error
		wg       sync.WaitGroup
	)
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{} // 并发闸门：满了就等待，保证同时只有 dedupConcurrency 个文件在读
		go func(j dedupJob) {
			defer wg.Done()
			defer func() { <-sem }()
			v, n, err := hasher(ctx, files[j.idx])
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else {
				res[j.idx] = v
			}
			mu.Unlock()
			if n > 0 && onRead != nil {
				onRead(n)
			}
		}(j)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return res, firstErr
}

// sampleHash 计算"头+尾"采样哈希：先读头 dedupSampleSize 字节、再读尾同长度字节，
// 累加进同一个 sha256。小文件（size <= 2*采样长度）直接读全文，此时采样哈希即全量哈希。
func sampleHash(ctx context.Context, path string, size int64) (hashVal, int64, error) {
	buf := make([]byte, dedupReadBuffer)
	h := sha256.New()
	if size <= dedupSampleSize*2 {
		n, err := readRangeInto(ctx, h, path, 0, size, buf)
		if err != nil {
			return hashVal{}, n, err
		}
		return hashVal{hash: hex.EncodeToString(h.Sum(nil)), full: true}, n, nil
	}
	n1, err := readRangeInto(ctx, h, path, 0, dedupSampleSize, buf)
	if err != nil {
		return hashVal{}, n1, err
	}
	n2, err := readRangeInto(ctx, h, path, size-dedupSampleSize, dedupSampleSize, buf)
	if err != nil {
		return hashVal{}, n1 + n2, err
	}
	return hashVal{hash: hex.EncodeToString(h.Sum(nil))}, n1 + n2, nil
}

// fullHash 以定长缓冲流式计算整文件的 sha256。
func fullHash(ctx context.Context, path string, size int64) (string, int64, error) {
	h := sha256.New()
	n, err := readRangeInto(ctx, h, path, 0, size, make([]byte, dedupReadBuffer))
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// readRangeInto 从 path 的 off 处顺序读 length 字节并累加进 h，返回实际读取字节数。
// 定长缓冲分块读取（不整文件入内存）；每轮检查 ctx，取消时立即停止读取。
func readRangeInto(ctx context.Context, h hash.Hash, path string, off, length int64, buf []byte) (int64, error) {
	if length <= 0 {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return 0, err
		}
	}
	var read int64
	for read < length {
		if err := ctx.Err(); err != nil {
			return read, err
		}
		want := len(buf)
		if rem := length - read; rem < int64(want) {
			want = int(rem)
		}
		nr, err := f.Read(buf[:want])
		if nr > 0 {
			h.Write(buf[:nr])
			read += int64(nr)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return read, err
		}
	}
	return read, nil
}

// dedupFullCandidates 返回需要全量哈希的候选：同大小、同采样哈希且 ≥2 个的大文件。
// 小文件（采样已覆盖全文）与采样组内只有 1 个的文件都不必再读一遍。
func dedupFullCandidates(files []dedupFile, sampleRes map[int]hashVal) []dedupJob {
	bySample := make(map[string][]int, len(sampleRes))
	for idx, v := range sampleRes {
		key := strconv.FormatInt(files[idx].size, 10) + "|" + v.hash
		bySample[key] = append(bySample[key], idx)
	}
	var jobs []dedupJob
	for _, idxs := range bySample {
		if len(idxs) < 2 || files[idxs[0]].size <= dedupSampleSize*2 {
			continue
		}
		for _, i := range idxs {
			jobs = append(jobs, dedupJob{idx: i, path: files[i].path})
		}
	}
	return jobs
}

// dedupFileGroups 汇总重复文件分组与可节省空间：
//   - 小文件：采样哈希已覆盖全文，直接以 (大小, 采样哈希) 成组；
//   - 大文件：以 (大小, 全量哈希) 成组，采样相同但全文不同者在此被正确排除。
//
// 组内 ≥2 个才算重复；可节省空间为 (副本数-1) × 单份大小。
func dedupFileGroups(files []dedupFile, sampleRes, fullRes map[int]hashVal) ([]model.DedupGroup, int64) {
	var out []model.DedupGroup
	var wasted int64

	bySample := make(map[string][]int, len(sampleRes))
	for idx, v := range sampleRes {
		if files[idx].size > dedupSampleSize*2 {
			continue // 大文件以全量哈希为准
		}
		key := strconv.FormatInt(files[idx].size, 10) + "|" + v.hash
		bySample[key] = append(bySample[key], idx)
	}
	for _, idxs := range bySample {
		if len(idxs) < 2 {
			continue
		}
		out = append(out, newDedupGroup(files[idxs[0]].size, sampleRes[idxs[0]].hash, pathsOf(files, idxs)))
		wasted += files[idxs[0]].size * int64(len(idxs)-1)
	}

	byFull := make(map[string][]int, len(fullRes))
	for idx, v := range fullRes {
		key := strconv.FormatInt(files[idx].size, 10) + "|" + v.hash
		byFull[key] = append(byFull[key], idx)
	}
	for _, idxs := range byFull {
		if len(idxs) < 2 {
			continue
		}
		out = append(out, newDedupGroup(files[idxs[0]].size, fullRes[idxs[0]].hash, pathsOf(files, idxs)))
		wasted += files[idxs[0]].size * int64(len(idxs)-1)
	}
	return out, wasted
}

// dedupFolders 按"所含文件身份集合相同（忽略目录层级）"判定重复文件夹。
// 文件身份优先用全量哈希、其次采样哈希、最后是唯一大小文件的合成身份——
// 三种身份在本批文件内都与文件内容一一对应，不会造成误判。
func dedupFolders(dirs []string, dirFiles map[string][]int, files []dedupFile,
	sampleRes, fullRes map[int]hashVal) []model.DedupGroup {
	if len(dirs) < 2 {
		return nil
	}
	ident := make([]string, len(files))
	for i, f := range files {
		switch {
		case fullRes[i].hash != "":
			ident[i] = "h:" + fullRes[i].hash
		case sampleRes[i].hash != "":
			ident[i] = "s:" + sampleRes[i].hash
		default:
			// 大小唯一的文件不可能与其他文件相同，用大小+路径合成唯一身份
			ident[i] = "u:" + strconv.FormatInt(f.size, 10) + ":" + f.path
		}
	}
	bySig := make(map[string][]int, len(dirs))
	sigSize := make(map[string]int64, len(dirs))
	for di, d := range dirs {
		var total int64
		set := make(map[string]struct{})
		for _, fi := range dirFiles[d] {
			set[ident[fi]] = struct{}{}
			total += files[fi].size
		}
		if len(set) == 0 {
			continue // 空目录（或仅含空文件）不参与判重
		}
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// 对排序后的身份集合取哈希，得到定长签名，避免保存超长字符串
		h := sha256.New()
		for _, k := range keys {
			h.Write([]byte(k))
			h.Write([]byte{0})
		}
		sig := hex.EncodeToString(h.Sum(nil))
		bySig[sig] = append(bySig[sig], di)
		sigSize[sig] = total
	}
	var out []model.DedupGroup
	for sig, dis := range bySig {
		if len(dis) < 2 {
			continue
		}
		paths := make([]string, 0, len(dis))
		for _, di := range dis {
			paths = append(paths, dirs[di])
		}
		sort.Slice(paths, func(i, j int) bool { return strings.ToLower(paths[i]) < strings.ToLower(paths[j]) })
		out = append(out, newDedupGroup(sigSize[sig], "", paths))
	}
	return out
}

// pathsOf 取指定文件下标的路径列表（按路径排序，便于展示）。
func pathsOf(files []dedupFile, idxs []int) []string {
	paths := make([]string, 0, len(idxs))
	for _, i := range idxs {
		paths = append(paths, files[i].path)
	}
	sort.Slice(paths, func(i, j int) bool { return strings.ToLower(paths[i]) < strings.ToLower(paths[j]) })
	return paths
}

// sortGroups 分组排序：先按单条大小降序（大文件冗余更值得关注），再按首条完整路径升序。
func sortGroups(groups []model.DedupGroup) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Size != groups[j].Size {
			return groups[i].Size > groups[j].Size
		}
		return firstPath(groups[i]) < firstPath(groups[j])
	})
}

// firstPath 取分组中第一条条目的完整路径，用于分组间排序（确定性比较）。
func firstPath(g model.DedupGroup) string {
	if len(g.Buckets) == 0 || len(g.Buckets[0].Names) == 0 {
		return ""
	}
	return g.Prefix + g.Buckets[0].Prefix + g.Buckets[0].Names[0]
}

// groupBySubdir 把去掉 prefix 的剩余部分按所在子目录分桶：
// 同一子目录的条目合并为一个桶，桶内只保留文件名，避免长目录反复出现。
func groupBySubdir(paths []string, prefix string) []model.DedupBucket {
	type bucket struct {
		prefix string
		names  []string
	}
	order := make([]string, 0, 4)
	byDir := make(map[string]*bucket)
	for _, p := range paths {
		dir, name := splitDirName(strings.TrimPrefix(p, prefix))
		b, ok := byDir[dir]
		if !ok {
			b = &bucket{prefix: dir}
			byDir[dir] = b
			order = append(order, dir)
		}
		b.names = append(b.names, name)
	}
	sort.Slice(order, func(i, j int) bool {
		return strings.ToLower(order[i]) < strings.ToLower(order[j])
	})
	out := make([]model.DedupBucket, 0, len(order))
	for _, d := range order {
		b := byDir[d]
		sort.Slice(b.names, func(i, j int) bool {
			return strings.ToLower(b.names[i]) < strings.ToLower(b.names[j])
		})
		out = append(out, model.DedupBucket{Prefix: b.prefix, Names: b.names})
	}
	return out
}

// splitDirName 把相对路径拆成「所在子目录（以分隔符结尾，无子目录时为空）+ 末段名称」。
func splitDirName(rest string) (string, string) {
	cut := strings.LastIndexAny(rest, `/\`)
	if cut < 0 {
		return "", rest
	}
	return rest[:cut+1], rest[cut+1:]
}

// commonDirPrefix 求全部路径的最长公共前缀，并回退到最后一个路径分隔符，
// 保证结果是一个完整目录（以分隔符结尾）；没有公共目录时返回空串。
// 按字节比较即可：分隔符为 ASCII，UTF-8 中不可能出现在多字节序列内部，截断位置必是字符边界。
func commonDirPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	p := paths[0]
	for _, q := range paths[1:] {
		n := len(p)
		if len(q) < n {
			n = len(q)
		}
		i := 0
		for i < n && p[i] == q[i] {
			i++
		}
		p = p[:i]
	}
	cut := strings.LastIndexAny(p, `/\`)
	if cut < 0 {
		return ""
	}
	return p[:cut+1]
}

// newDedupGroup 构造重复分组：先取全组公共目录前缀，再按子目录拆成若干桶，
// 使"超长公共前缀"与"深层子目录"都只出现一次。
func newDedupGroup(size int64, hash string, paths []string) model.DedupGroup {
	prefix := commonDirPrefix(paths)
	return model.DedupGroup{
		Size:    size,
		Hash:    hash,
		Prefix:  prefix,
		Buckets: groupBySubdir(paths, prefix),
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// dedupTracker 累加实际读取字节数，并把进度映射到当前阶段区间后节流回写。
type dedupTracker struct {
	runner *Runner
	taskID uint
	mu     sync.Mutex
	read   int64 // 全程累计读取字节数
	stage  int64 // 当前阶段已读字节数
	total  int64 // 当前阶段预计读取字节数
	base   int   // 当前阶段进度起点（百分比）
	span   int   // 当前阶段进度跨度（百分比）
	last   time.Time
}

func newDedupTracker(r *Runner, taskID uint) *dedupTracker {
	return &dedupTracker{runner: r, taskID: taskID}
}

// reset 开启一个新阶段：total 为预计读取字节数，base/span 为该阶段占用的进度区间。
func (t *dedupTracker) reset(total int64, base, span int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total, t.base, t.span = total, base, span
	t.stage = 0
	t.last = time.Time{}
}

func (t *dedupTracker) add(n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.read += n
	t.stage += n
	read, total, base, span := t.stage, t.total, t.base, t.span
	now := time.Now()
	// 阶段结束（读满预计字节）必定回写一次，其余按 256ms 节流（与 archive tracker 一致）
	if read < total && now.Sub(t.last) < 256*time.Millisecond {
		t.mu.Unlock()
		return
	}
	t.last = now
	t.mu.Unlock()
	pct := base
	if total > 0 {
		pct = base + int(read*int64(span)/total)
	}
	if pct > 100 {
		pct = 100
	}
	t.runner.updateTask(t.taskID, map[string]interface{}{
		"processed_bytes":  t.read,
		"total_bytes":      total,
		"progress_percent": pct,
	}, "写入查重进度")
}

// readBytes 返回全程累计读取字节数。
func (t *dedupTracker) readBytes() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.read
}

// succeedDedup 收尾查重任务：写入结果 JSON 与成功状态。
// 查重全程只读、不产生任何文件改动，因此不走 succeed 的文件列表缓存增量更新。
func (r *Runner) succeedDedup(task *model.Task, result string, checked int64) {
	r.updateTask(task.ID, map[string]interface{}{
		"status":           model.StatusSucceeded,
		"progress_percent": 100,
		"processed_bytes":  checked,
		"total_bytes":      checked,
		"result":           result,
		"completed_at":     time.Now(),
		"error":            "",
	}, "写入查重结果")
}

// finishDedupInterrupt 收尾被中断的查重任务：用户取消走 cancelled，服务中断走重排。
// 查重无临时文件、无产物，无需任何清理。
func (r *Runner) finishDedupInterrupt(task *model.Task) {
	if r.chk != nil && r.chk.IsCancelled(task.ID) {
		r.finishCancelled(task)
		return
	}
	FinishInterrupted(r.db, task)
}

// progressFn 返回节流的进度回调：archive.tracker 每 256ms 调用一次。
func (r *Runner) progressFn(task *model.Task) archive.ProgressFn {
	return func(p archive.Progress) {
		updates := map[string]interface{}{
			"processed_bytes": p.ProcessedBytes,
			"total_bytes":     p.TotalBytes,
		}
		if pct := p.Percent(); pct >= 0 {
			updates["progress_percent"] = pct
		}
		if err := r.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
			log.Printf("任务 #%d 进度回写失败：%v", task.ID, err)
		}
	}
}

func (r *Runner) setTempTarget(task *model.Task, temp, target string) {
	task.TempPath = temp
	task.TargetPath = target
	r.updateTask(task.ID, map[string]interface{}{
		"temp_path":   temp,
		"target_path": target,
	}, "写入临时路径")
}

func (r *Runner) fail(task *model.Task, msg string) {
	r.updateTask(task.ID, map[string]interface{}{
		"status":       model.StatusFailed,
		"error":        truncate(msg, 2048),
		"completed_at": time.Now(),
		"temp_path":    "",
	}, "写入失败状态")
}

func (r *Runner) succeed(task *model.Task) {
	// 任务成功后对源目录的列表缓存做精确增量更新（删源、加目标），而非整条失效：
	// 解压删源压缩包加解压目录、压缩删源加 .zip，delta 完全一致。分卷解压时"删源"需移除
	// 全部分卷，故此处用 EnumerateVolumes 取全部分卷作为删除项；单文件/压缩时仅为源路径。
	// 这样对含大量文件的目录在频繁任务下不会反复触发昂贵的全量重列，缓存始终温热；
	// 外部/手动改动仍由 ListCached 的 mtime 守卫兜底失效。
	removed := []string{task.SourcePath}
	if vols, _, isSplit, verr := archive.EnumerateVolumes(task.SourcePath); verr == nil && isSplit {
		removed = vols
	}
	fs.UpdateListCache(filepath.Dir(task.SourcePath), task.TargetPath, removed...)
	r.updateTask(task.ID, map[string]interface{}{
		"status":           model.StatusSucceeded,
		"progress_percent": 100,
		"completed_at":     time.Now(),
		"temp_path":        "",
		"error":            "",
	}, "写入成功状态")
}

// finishCancelled 收尾被用户取消的任务：清理临时文件（解压临时目录或临时压缩包）、
// 标记 cancelled；不重排（用户主动取消，不应自动续做），源/目标文件均保留。
func (r *Runner) finishCancelled(task *model.Task) {
	if task.TempPath != "" {
		_ = os.RemoveAll(task.TempPath)
	}
	r.updateTask(task.ID, map[string]interface{}{
		"status":       model.StatusCancelled,
		"completed_at": time.Now(),
		"temp_path":    "",
		"error":        "任务已被用户取消",
	}, "写入取消状态")
}

// updateTask 写入任务字段，失败时重试一次并记录日志。
// 状态落库失败的任务会停留在 running，由下次重启的 RecoverOnStartup 兜底，
// 这里至少要把失败暴露到日志。
func (r *Runner) updateTask(taskID uint, updates map[string]interface{}, what string) {
	err := r.db.Model(&model.Task{}).Where("id = ?", taskID).Updates(updates).Error
	if err == nil {
		return
	}
	log.Printf("任务 #%d %s失败，重试一次：%v", taskID, what, err)
	time.Sleep(100 * time.Millisecond)
	if err := r.db.Model(&model.Task{}).Where("id = ?", taskID).Updates(updates).Error; err != nil {
		log.Printf("任务 #%d %s仍失败，重启后由启动恢复兜底：%v", taskID, what, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按 rune 边界回退，避免把多字节字符（如中文）切成非法 UTF-8
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
