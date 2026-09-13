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
// 供前端通过 ?refresh=1 强制刷新时使用。
func InvalidateListCache(path string) {
	listCacheMu.Lock()
	delete(listCache, path)
	listCacheMu.Unlock()
}

// ClearListCache 清空全部目录列表缓存（测试与运维手动刷新用）。
func ClearListCache() {
	listCacheMu.Lock()
	listCache = make(map[string]listCacheEntry)
	listCacheMu.Unlock()
}
