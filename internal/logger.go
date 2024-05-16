package internal

import (
	"bytes"
	"fmt"
	"time"
)

type Logger struct {
	total               int
	startTime           time.Time
	processed           int
	totalFileSize       int
	totalCompressedSize int
}

// byteCountSI 将字节数转换为KiB或MiB格式，使用国际单位制（SI）前缀
func byteCountSI(b int) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB",
		float64(b)/float64(div), "KM"[exp])
}

func NewLogger(total int, filename string) *Logger {
	fmt.Printf("处理 %s\n", filename)
	return &Logger{total, time.Now(), 0, 0, 0}
}

func (l *Logger) Add(fileSize int, compressedSize int) {
	l.processed++
	l.totalFileSize += fileSize
	l.totalCompressedSize += compressedSize
	percent := float64(l.processed) / float64(l.total) * 100
	compress_percent := float64(l.totalCompressedSize) / float64(l.totalFileSize) * 100
	fmt.Printf("\r压缩率 %4.2f 进度: [%-50s] %.2f%% %10s/%s", compress_percent, bar(percent, 50), percent, byteCountSI(l.totalCompressedSize), byteCountSI(l.totalFileSize))
}

// bar 返回一个表示进度的字符串条
func bar(percent float64, width int) string {
	full := int(percent/100*float64(width)) - 1
	var b bytes.Buffer
	for i := 0; i < full; i++ {
		b.WriteString("=")
	}
	b.WriteString(">")
	for i := full + 1; i < width; i++ {
		b.WriteString(" ")
	}
	return b.String()
}
