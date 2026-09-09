package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const copyBufSize = 32 * 1024

// ErrLimitExceeded 当解压总量超过配置上限时返回。
var ErrLimitExceeded = errors.New("解压总量超过上限")

// copyWithLimit 分块拷贝：每块之间检查 ctx 取消，使大文件拷贝可被中断
// （此前 io.Copy 只能在条目边界响应取消）；maxBytes >= 0 时约束累计写入量，
// gzip 这类无法预知大小的流由此获得上限，< 0 表示不限制。
// 进度统计由 dst 侧的 countWriter 上报，这里返回实际写入的字节数。
func copyWithLimit(ctx context.Context, dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	buf := make([]byte, copyBufSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			end := n
			over := false
			if maxBytes >= 0 && total+int64(n) > maxBytes {
				end = int(maxBytes - total)
				if end < 0 {
					end = 0
				}
				over = true
			}
			if end > 0 {
				written, werr := dst.Write(buf[:end])
				total += int64(written)
				if werr != nil {
					return total, werr
				}
				if written < end {
					return total, io.ErrShortWrite
				}
			}
			if over {
				return total, fmt.Errorf("%w：已写入 %d 字节，上限 %d 字节", ErrLimitExceeded, total, maxBytes)
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}
