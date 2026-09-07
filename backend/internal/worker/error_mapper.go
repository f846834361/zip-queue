package worker

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"zip-queue/internal/archive"
)

// classifyError 将原始错误映射为面向用户的友好中文原因。
// 返回简短描述 + 可选的详细信息。
func classifyError(err error) string {
	if err == nil {
		return ""
	}

	// 1. 已知的业务错误
	switch {
	case errors.Is(err, archive.ErrPasswordRequired):
		return "压缩包已加密，但未配置匹配的解压密码。请在「配置」页面添加密码后重试。"
	case errors.Is(err, archive.ErrUnsupportedFormat):
		return "不支持的压缩格式，仅支持 zip / tar / tar.gz / gz。"
	}

	msg := err.Error()

	// 2. 中断
	if strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "interrupted by shutdown") ||
		strings.Contains(msg, "context deadline exceeded") {
		return "任务被中断，可能是服务重启或关闭所致，可重新执行。"
	}

	// 3. 不安全路径
	if strings.Contains(msg, "unsafe entry path") {
		return "压缩包内含不安全路径，已拒绝解压以防止目录穿越。"
	}

	// 4. 磁盘空间不足
	if strings.Contains(msg, "no space left on device") ||
		strings.Contains(msg, "disk quota exceeded") {
		return "磁盘空间不足，无法完成操作。请清理磁盘后重试。"
	}

	// 5. 权限不足
	if strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "Access is denied") {
		return "权限不足，无法访问或写入文件。请检查文件/文件夹权限。"
	}

	// 6. 源文件不存在
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		if os.IsNotExist(err) {
			return "源文件不存在或已被删除。"
		}
	}

	// 7. 压缩包损坏相关
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "zip: not a valid zip file") ||
		strings.Contains(lower, "not a valid zip") ||
		strings.Contains(lower, "bad zip") ||
		strings.Contains(lower, "unexpected eof") ||
		strings.Contains(lower, "corrupt") {
		return "压缩包已损坏或格式不正确，无法解压。"
	}
	// gzip 损坏
	if strings.Contains(lower, "gzip: invalid header") ||
		strings.Contains(lower, "unexpected eof") {
		return "压缩包已损坏或格式不正确，无法解压。"
	}
	// tar 损坏
	if strings.Contains(lower, "archive/tar:") {
		return "压缩包已损坏或格式不正确，无法解压。"
	}

	// 8. 密码错误（alexmullins/zip 返回 ErrPassword）
	if strings.Contains(msg, "zip: invalid password") ||
		strings.Contains(msg, "zip: authentication failed") {
		return "解压密码错误，请检查「配置」页面中的解压密码。"
	}

	// 9. 空文件夹
	if strings.Contains(msg, "no files to compress") {
		return "文件夹内没有可压缩的文件。"
	}

	// 10. 回退：返回原始信息
	return "操作失败：" + msg
}

// classifyTargetExists 针对「目标已存在」给出更具体的描述。
func classifyTargetExists(path string, isCompress bool) string {
	if isCompress {
		return "目标路径已存在同名压缩包：" + path + "。请删除或重命名后重试。"
	}
	return "目标路径已存在同名文件或文件夹：" + path + "。请删除或重命名后重试。"
}

// classifyMoveError 针对「移动输出失败」。
func classifyMoveError(err error) string {
	if os.IsPermission(err) {
		return "移动输出文件失败：权限不足。"
	}
	if strings.Contains(err.Error(), "no space left on device") {
		return "移动输出文件失败：磁盘空间不足。"
	}
	return "移动输出文件失败：" + err.Error()
}

// classifyDeleteError 针对「删除原文件失败」。
func classifyDeleteError(err error, taskType string) string {
	target := "原压缩包"
	if taskType == "compress" {
		target = "原文件/文件夹"
	}
	if os.IsPermission(err) {
		return "操作已完成，但删除" + target + "失败：权限不足。"
	}
	return "操作已完成，但删除" + target + "失败：" + err.Error()
}
