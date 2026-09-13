package fs

import (
	"os"
	"sync"
	"time"
)

// listCacheEntry 是单个目录列表的缓存项。
type listCacheEntry struct {
	entries  []Entry
	modTime  time.Time
	expireAt time.Time
}

var (
	listCacheMu sync.RWMutex
	listCache   = make(map[string]listCacheEntry)
)

// ListCached 包装 List，对目录列表结果做短时缓存，避免对含大量文件的目录
// 重复执行昂贵的 ReadDir + 逐文件 lstat（在网络文件系统挂载下尤其缓慢）。
//
// 失效由两道关卡判定：
//  1. 目录的修改时间（mtime）变化 —— 通过一次仅 stat 目录本身的廉价调用判断，
//     文件增删改（含压缩/解压任务）都会使 mtime 变化，从而自动失效；
//  2. TTL 到期 —— 作为兜底，防止罕见情况下 mtime 未变却需刷新。
//
// 命中缓存时直接返回上次结果，跳过 ReadDir 与每个条目的 lstat。
func ListCached(path string, ttl time.Duration) ([]Entry, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	now := time.Now()
	listCacheMu.RLock()
	if c, ok := listCache[path]; ok && now.Before(c.expireAt) && c.modTime.Equal(info.ModTime()) {
		listCacheMu.RUnlock()
		return c.entries, c.modTime, nil
	}
	listCacheMu.RUnlock()

	entries, modTime, err := List(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	listCacheMu.Lock()
	listCache[path] = listCacheEntry{entries: entries, modTime: modTime, expireAt: now.Add(ttl)}
	listCacheMu.Unlock()
	return entries, modTime, nil
}

// InvalidateListCache 使指定目录的列表缓存失效，下次请求将重新遍历。
// 供前端通过 ?refresh=1 强制刷新，以及增量更新失败时的兜底使用。
func InvalidateListCache(path string) {
	listCacheMu.Lock()
	delete(listCache, path)
	listCacheMu.Unlock()
}

// UpdateListCache 对缓存中的目录列表做精确增量更新，而非整条失效。
// 用于任务完成等「已知目录变更 delta」的场景：解压会删除源压缩包并新增解压出的
// 文件夹，压缩会删除源文件并新增 .zip——两者都是「删 removedPath、加 addedPath」。
// 直接重建该目录的缓存条目（1 次 Lstat 取新增项 + 1 次 Stat 刷新目录真实 mtime +
// 内存切片重建），避免对含大量文件（如数千文件夹）的目录在频繁任务下反复触发
// 昂贵的全量重列（ReadDir + 逐条 Lstat），使缓存始终保持温热、切实发挥作用。
//
// 并发安全（写时复制）：所有写操作在写锁内进行，且总是构建全新的切片整体替换，
// 绝不原地修改旧切片——正在读取旧切片引用的请求不受影响，不存在数据竞争。
//
// 若目录未被浏览过（无未过期缓存项），则什么都不做：只为真正在用的缓存做维护，
// 不会主动为未浏览目录构建缓存。若新增项无法 Lstat（极端情况），退化为整条失效，
// 由下次访问重建，保证安全。外部/手动的文件改动仍由 ListCached 的 mtime 守卫兜底。
func UpdateListCache(path, removedPath, addedPath string) {
	listCacheMu.Lock()
	defer listCacheMu.Unlock()
	c, ok := listCache[path]
	if !ok || time.Now().After(c.expireAt) {
		return
	}
	added, err := buildEntry(addedPath)
	if err != nil {
		delete(listCache, path)
		return
	}
	// 写时复制：构建全新切片，不改动旧的，保证并发读取安全。
	out := make([]Entry, 0, len(c.entries)+1)
	for _, e := range c.entries {
		if e.Path == removedPath {
			continue
		}
		out = append(out, e)
	}
	out = append(out, added)
	sortEntries(out)
	// 用目录真实 mtime 刷新（单次 Stat），使 mtime 守卫与真实状态一致，
	// 避免下次访问因 mtime 不匹配而误判失效、触发全量重列。
	if info, serr := os.Stat(path); serr == nil {
		c.modTime = info.ModTime()
	}
	c.entries = out
	listCache[path] = c
}

// ClearListCache 清空全部目录列表缓存（测试与运维手动刷新用）。
func ClearListCache() {
	listCacheMu.Lock()
	listCache = make(map[string]listCacheEntry)
	listCacheMu.Unlock()
}
