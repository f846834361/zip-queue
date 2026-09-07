package archive

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	zip "github.com/alexmullins/zip"
)

// ErrPasswordRequired 当 zip 加密但无匹配密码时返回。
var ErrPasswordRequired = fmt.Errorf("zip is encrypted but no matching password found")

// extractZip 解压 zip 到 targetDir，应用智能合并；支持加密 zip（按密码列表顺序尝试）。
func extractZip(ctx context.Context, src, targetDir string, passwords []string, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	// 探测是否有加密条目，若有则按顺序轮询密码
	password := ""
	for _, f := range r.File {
		if !f.IsEncrypted() {
			continue
		}
		// 找到第一个加密条目，用其探测正确密码
		pw, err := tryPasswords(r, passwords)
		if err != nil {
			return err
		}
		password = pw
		break
	}

	// 扫描条目以确定剥离前缀与总量
	var entries []entryMeta
	for _, f := range r.File {
		entries = append(entries, entryMeta{
			name:  f.Name,
			isDir: f.Mode().IsDir() || strings.HasSuffix(f.Name, "/"),
			size:  int64(f.UncompressedSize64),
		})
	}
	strip := determineStrip(entries)
	var totalBytes int64
	totalEntries := 0
	for _, e := range entries {
		if !e.isDir {
			totalBytes += e.size
			totalEntries++
		}
	}
	t := newTracker(totalBytes, totalEntries, p)

	for _, f := range r.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		isDir := f.Mode().IsDir() || strings.HasSuffix(f.Name, "/")
		dest, skip, err := resolveEntry(targetDir, f.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if isDir {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		t.setCurrent(normName(f.Name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		var rc io.ReadCloser
		if f.IsEncrypted() {
			f.SetPassword(password)
		}
		rc, err = f.Open()
		if err != nil {
			return fmt.Errorf("open entry %q: %w", f.Name, err)
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return err
		}
		cw := &countWriter{w: out, t: t}
		_, copyErr := io.Copy(cw, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
		_ = os.Chmod(dest, f.Mode().Perm())
		t.entryDone()
	}
	t.notify(true)
	return nil
}

// tryPasswords 用每个密码尝试打开加密条目，返回第一个成功打开的密码。
func tryPasswords(r *zip.ReadCloser, passwords []string) (string, error) {
	if len(passwords) == 0 {
		return "", ErrPasswordRequired
	}
	// 找到第一个加密的非目录条目作为探针
	var probe *zip.File
	for _, f := range r.File {
		if f.IsEncrypted() && !f.Mode().IsDir() && !strings.HasSuffix(f.Name, "/") {
			probe = f
			break
		}
	}
	if probe == nil {
		// 只有加密目录条目，无文件可探测，直接用第一个密码
		return passwords[0], nil
	}
	for _, pw := range passwords {
		probe.SetPassword(pw)
		rc, err := probe.Open()
		if err == nil {
			rc.Close()
			return pw, nil
		}
	}
	return "", ErrPasswordRequired
}

// Compress 将 src（文件夹或单文件）压缩为 destZip（zip/deflate）。
func Compress(ctx context.Context, src, destZip string, p ProgressFn) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	var files []string
	var totalBytes int64
	var basePath string
	if info.IsDir() {
		basePath = src
		walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			files = append(files, path)
			totalBytes += info.Size()
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	} else {
		basePath = filepath.Dir(src)
		files = []string{src}
		totalBytes = info.Size()
	}
	if len(files) == 0 {
		return fmt.Errorf("no files to compress in %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(destZip), 0o755); err != nil {
		return err
	}
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()

	t := newTracker(totalBytes, len(files), p)
	for _, fpath := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(basePath, fpath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		t.setCurrent(rel)
		info, err := os.Stat(fpath)
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = rel
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(fpath)
		if err != nil {
			return err
		}
		cw := &countWriter{w: w, t: t}
		_, copyErr := io.Copy(cw, f)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
		t.entryDone()
	}
	t.notify(true)
	return nil
}
