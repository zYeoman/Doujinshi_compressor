package internal

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

// Out 是进度输出的目标，测试里可以替换成 io.Discard。
var Out io.Writer = os.Stdout

// Logger 在单行内刷新压缩进度。所有方法都只应由写 zip 的单个协程调用。
type Logger struct {
	total               int
	startTime           time.Time
	processed           int
	totalFileSize       int
	totalCompressedSize int
	lastLineLen         int
}

func NewLogger(total int, name string) *Logger {
	fmt.Fprintf(Out, "处理 %s\n", name)
	return &Logger{total: total, startTime: time.Now()}
}

// byteCountSI 把字节数转成易读的 KiB/MiB 形式。
func byteCountSI(b int) string {
	return humanize.Bytes(uint64(b))
}

func (l *Logger) ratio() float64 {
	if l.totalFileSize == 0 {
		return 0
	}
	return float64(l.totalCompressedSize) / float64(l.totalFileSize) * 100
}

func (l *Logger) Add(fileSize, compressedSize int) {
	l.processed++
	l.totalFileSize += fileSize
	l.totalCompressedSize += compressedSize

	percent := 0.0
	if l.total > 0 {
		percent = float64(l.processed) / float64(l.total) * 100
	}
	line := fmt.Sprintf("压缩率 %5.2f%% 进度: [%s] %6.2f%% %10s/%s",
		l.ratio(), bar(percent, 40), percent,
		byteCountSI(l.totalCompressedSize), byteCountSI(l.totalFileSize))

	pad := l.lastLineLen - len(line)
	if pad < 0 {
		pad = 0
	}
	fmt.Fprintf(Out, "\r%s%s", line, strings.Repeat(" ", pad))
	l.lastLineLen = len(line)
}

// Finish 结束进度行并打印本次汇总。
func (l *Logger) Finish(written, skipped int, interrupted bool) {
	fmt.Fprintln(Out)
	status := "完成"
	if interrupted {
		status = "中断"
	}
	fmt.Fprintf(Out, "%s: %d/%d 张，跳过 %d 张，用时 %s，输出 %s（压缩率 %.2f%%）\n",
		status,
		written, l.total, skipped, time.Since(l.startTime).Round(time.Millisecond),
		byteCountSI(l.totalCompressedSize), l.ratio())
}

// bar 返回一个宽度固定为 width 的进度条。
func bar(percent float64, width int) string {
	if width <= 0 {
		return ""
	}
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	full := int(percent/100*float64(width)) - 1
	if full < 0 {
		full = 0
	}
	if full > width-1 {
		full = width - 1
	}
	var b strings.Builder
	b.WriteString(strings.Repeat("=", full))
	b.WriteString(">")
	b.WriteString(strings.Repeat(" ", width-full-1))
	return b.String()
}
