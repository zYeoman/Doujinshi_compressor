package main

import (
	"archive/zip"
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"doujinshi_compressor/internal"

	"github.com/chai2010/webp"
	"github.com/nfnt/resize"
)

var (
	formatFlag   = flag.String("format", "webp", "输出格式：webp、jpg、png 或 gif")
	qualityFlag  = flag.Float64("quality", 75, "webp/jpg 的输出质量（1-100）")
	maxWidthFlag = flag.Uint("max-width", 1080, "输出图片的最大宽度，0 表示不缩放")
	jobsFlag     = flag.Int("jobs", runtime.NumCPU(), "并发的编码协程数")
	outDirFlag   = flag.String("o", ".", "zip 输出目录")
)

// imageExtensions 是可解码的输入图片扩展名。
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// supportedFormats 把用户传入的 -format 值映射到统一的输出扩展名。
var supportedFormats = map[string]string{
	"webp": "webp",
	"jpg":  "jpg",
	"jpeg": "jpg",
	"png":  "png",
	"gif":  "gif",
}

type options struct {
	format   string
	quality  float64
	maxWidth uint
	jobs     int
	outDir   string
}

// encodeJob 是一张待处理的图片。index 记录原始先后顺序：解码/编码会乱序完成，
// 最终由 writeResults 按 index 顺序写回 zip，保证页序与文件名一致。
type encodeJob struct {
	index   int
	srcName string
	zipName string
	path    string
	srcSize int
	modTime time.Time
}

// encodeResult 永远为每个 encodeJob 产生一条，失败时 err 非空。
// 这样即使某张图失败，writeResults 也能推进序号，不会卡住后续图片。
type encodeResult struct {
	encodeJob
	data []byte
	err  error
}

type stats struct {
	total    int
	written  int
	skipped  int
	srcBytes int
	outBytes int
	failures []string
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "用法: %s [选项] [目录...]\n\n", filepath.Base(os.Args[0]))
	fmt.Fprintln(os.Stderr, "把目录里的图片缩放、重新编码后打包成 <目录名>.zip。")
	fmt.Fprintln(os.Stderr, "不指定目录时，处理当前目录下的每个一级子目录（忽略隐藏目录）。")
	fmt.Fprintln(os.Stderr, "\n选项:")
	flag.PrintDefaults()
}

func run() error {
	format, err := normalizeFormat(*formatFlag)
	if err != nil {
		return err
	}
	if *qualityFlag < 1 || *qualityFlag > 100 {
		return fmt.Errorf("quality 必须在 1-100 之间，当前为 %g", *qualityFlag)
	}
	jobs := *jobsFlag
	if jobs < 1 {
		jobs = 1
	}
	opts := options{
		format:   format,
		quality:  *qualityFlag,
		maxWidth: *maxWidthFlag,
		jobs:     jobs,
		outDir:   *outDirFlag,
	}
	if err := os.MkdirAll(opts.outDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录 %s: %w", opts.outDir, err)
	}

	targets, err := resolveTargets(flag.Args())
	if err != nil {
		return err
	}

	// 只注册一次信号处理；所有协程通过 ctx 感知中断。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for _, dir := range targets {
		if ctx.Err() != nil {
			break
		}
		if _, err := processDirectory(ctx, dir, opts); err != nil {
			fmt.Fprintf(os.Stderr, "处理 %s 失败: %v\n", dir, err)
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "已中断。")
	}
	return nil
}

// resolveTargets 返回要处理的目录列表：显式传入的目录，或当前目录下的一级子目录。
func resolveTargets(args []string) ([]string, error) {
	if len(args) > 0 {
		for _, arg := range args {
			info, err := os.Stat(arg)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("%s 不是目录", arg)
			}
		}
		return args, nil
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	return dirs, nil
}

func normalizeFormat(format string) (string, error) {
	key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	if ext, ok := supportedFormats[key]; ok {
		return ext, nil
	}
	return "", fmt.Errorf("不支持的输出格式 %q（可选 webp/jpg/png/gif）", format)
}

func isImageFile(fileName string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(fileName))]
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// naturalLess 按数字大小比较文件名，使 1.png < 2.png < 10.png。
func naturalLess(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if isDigit(ra[i]) && isDigit(rb[j]) {
			startA, startB := i, j
			for i < len(ra) && isDigit(ra[i]) {
				i++
			}
			for j < len(rb) && isDigit(rb[j]) {
				j++
			}
			numA := strings.TrimLeft(string(ra[startA:i]), "0")
			numB := strings.TrimLeft(string(rb[startB:j]), "0")
			if len(numA) != len(numB) {
				return len(numA) < len(numB)
			}
			if numA != numB {
				return numA < numB
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	return len(ra)-i < len(rb)-j
}

// listJobs 按自然顺序列出目录中的图片，并为每张图生成带序号的编码任务。
func listJobs(dirPath, outFormat string) ([]encodeJob, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	type fileEntry struct {
		name    string
		size    int64
		modTime time.Time
	}
	var images []fileEntry
	for _, entry := range entries {
		if entry.IsDir() || !isImageFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		images = append(images, fileEntry{entry.Name(), info.Size(), info.ModTime()})
	}
	sort.Slice(images, func(i, j int) bool { return naturalLess(images[i].name, images[j].name) })

	jobs := make([]encodeJob, 0, len(images))
	for i, image := range images {
		base := strings.TrimSuffix(image.name, filepath.Ext(image.name))
		jobs = append(jobs, encodeJob{
			index:   i,
			srcName: image.name,
			zipName: base + "." + outFormat,
			path:    filepath.Join(dirPath, image.name),
			srcSize: int(image.size),
			modTime: image.modTime,
		})
	}
	return jobs, nil
}

// decodeFile 打开、解码并立即关闭源文件，避免同时占用大量文件描述符。
func decodeFile(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("解码: %w", err)
	}
	return img, nil
}

func encodeOne(opts options, job encodeJob) encodeResult {
	result := encodeResult{encodeJob: job}
	img, err := decodeFile(job.path)
	if err != nil {
		result.err = err
		return result
	}
	if opts.maxWidth > 0 && uint(img.Bounds().Dx()) > opts.maxWidth {
		img = resize.Resize(opts.maxWidth, 0, img, resize.Lanczos3)
	}

	var buf bytes.Buffer
	switch opts.format {
	case "webp":
		err = webp.Encode(&buf, img, &webp.Options{Quality: float32(opts.quality)})
	case "jpg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: int(opts.quality)})
	case "png":
		err = png.Encode(&buf, img)
	case "gif":
		err = gif.Encode(&buf, img, &gif.Options{NumColors: 256})
	default:
		err = fmt.Errorf("不支持的输出格式: %s", opts.format)
	}
	if err != nil {
		result.err = fmt.Errorf("编码: %w", err)
		return result
	}
	result.data = buf.Bytes()
	return result
}

func feedJobs(ctx context.Context, jobs []encodeJob, out chan<- encodeJob) {
	defer close(out)
	for _, job := range jobs {
		select {
		case out <- job:
		case <-ctx.Done():
			return
		}
	}
}

func startEncoders(ctx context.Context, opts options, in <-chan encodeJob, out chan<- encodeResult) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < opts.jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range in {
				result := encodeOne(opts, job)
				select {
				case out <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	return &wg
}

// writeResults 按 index 顺序把编码结果写入 zip：编码乱序完成，但只在
// 序号连续时落盘，因此 zip 内的页序始终与文件名顺序一致。
func writeResults(ctx context.Context, zipWriter *zip.Writer, total int, in <-chan encodeResult, logger *internal.Logger) stats {
	st := stats{total: total}
	pending := make(map[int]encodeResult)
	next := 0

	write := func(result encodeResult) {
		if result.err != nil {
			st.skipped++
			st.failures = append(st.failures, fmt.Sprintf("%s: %v", result.srcName, result.err))
			return
		}
		header := &zip.FileHeader{Name: result.zipName, Method: zip.Store}
		if result.modTime.Year() >= 1980 {
			header.SetModTime(result.modTime)
		}
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			st.skipped++
			st.failures = append(st.failures, fmt.Sprintf("%s: 写入 zip: %v", result.srcName, err))
			return
		}
		if _, err := writer.Write(result.data); err != nil {
			st.skipped++
			st.failures = append(st.failures, fmt.Sprintf("%s: 写入 zip: %v", result.srcName, err))
			return
		}
		st.written++
		st.srcBytes += result.srcSize
		st.outBytes += len(result.data)
		logger.Add(result.srcSize, len(result.data))
	}

loop:
	for {
		select {
		case result, ok := <-in:
			if !ok {
				break loop
			}
			pending[result.index] = result
			for {
				ready, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				next++
				write(ready)
			}
		case <-ctx.Done():
			break loop
		}
	}
	return st
}

func processDirectory(ctx context.Context, dirPath string, opts options) (stats, error) {
	jobs, err := listJobs(dirPath, opts.format)
	if err != nil {
		return stats{}, err
	}
	if len(jobs) == 0 {
		return stats{}, nil
	}

	zipPath := filepath.Join(opts.outDir, filepath.Base(filepath.Clean(dirPath))+".zip")
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return stats{}, fmt.Errorf("创建 %s: %w", zipPath, err)
	}

	logger := internal.NewLogger(len(jobs), dirPath)

	jobCh := make(chan encodeJob, opts.jobs)
	resultCh := make(chan encodeResult, opts.jobs)
	go feedJobs(ctx, jobs, jobCh)
	encoders := startEncoders(ctx, opts, jobCh, resultCh)
	go func() {
		encoders.Wait()
		close(resultCh)
	}()

	zipWriter := zip.NewWriter(zipFile)
	st := writeResults(ctx, zipWriter, len(jobs), resultCh, logger)

	closeErr := zipWriter.Close()
	if err := zipFile.Close(); err != nil && closeErr == nil {
		closeErr = err
	}
	interrupted := ctx.Err() != nil
	logger.Finish(st.written, st.skipped, interrupted)

	if st.written == 0 {
		// 没有任何成功的输出，别留下空 zip。
		os.Remove(zipPath)
	}
	for _, msg := range st.failures {
		fmt.Fprintf(os.Stderr, "跳过 %s\n", msg)
	}
	if interrupted {
		if st.written == 0 {
			fmt.Fprintf(os.Stderr, "%s: 已中断，未生成 zip。\n", dirPath)
		} else {
			fmt.Fprintf(os.Stderr, "%s: 已中断，%s 中保留 %d/%d 张图片。\n",
				dirPath, filepath.Base(zipPath), st.written, len(jobs))
		}
	}
	return st, closeErr
}
