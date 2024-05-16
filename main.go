package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"doujinshi_compressor/internal"
	"github.com/chai2010/webp"
	"github.com/nfnt/resize"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
)

var (
	format   = flag.String("format", "webp", "Output format: webp, jpg, or png")
	quality  = flag.Float64("quality", 75, "Output quality for WebP and JPG (1-100)")
	maxWidth = flag.Uint("max-width", 1080, "Maximum width of the output images (0 for no resizing)")
)

func isImageFile(fileName string) bool {
	// 简单地检查文件扩展名
	ext := strings.ToLower(filepath.Ext(fileName))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp"
}

type ImageInfo struct {
	Name  string
	Size  int
	Image image.Image
}
type BytesInfo struct {
	Name     string
	Size     int
	ImageBuf bytes.Buffer
}

func readFiles(dirPath string, wg *sync.WaitGroup, ch chan ImageInfo) {
	defer wg.Done()
	// 获取目录下的所有文件
	files, err := os.ReadDir(dirPath)
	if err != nil {
		fmt.Printf("\nopen %s failed\n", dirPath)
		return
	}
	for _, file := range files {
		if !file.IsDir() {
			// 检查文件是否为图片
			if isImageFile(file.Name()) {
				// 构建源文件和目标文件的路径
				srcFilePath := filepath.Join(dirPath, file.Name())
				baseFileName := strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
				// 打开源文件
				srcFile, err := os.Open(srcFilePath)
				if err != nil {
					fmt.Printf("\nopen %s failed\n", srcFilePath)
					continue
				}
				defer srcFile.Close()

				fileInfo, err := srcFile.Stat()
				if err != nil {
					fmt.Printf("\nstate %s failed\n", srcFilePath)
					continue
				}
				srcSize := fileInfo.Size()

				// 解码图片
				img, _, err := image.Decode(srcFile)
				if err != nil {
					fmt.Printf("\ndecode %s failed\n", srcFilePath)
					continue
				}
				ch <- ImageInfo{baseFileName, int(srcSize), img}
			}
		}
	}
}
func encodeFiles(maxWidth uint, outputFormat string, quality float64, wg *sync.WaitGroup, inch chan ImageInfo, outch chan BytesInfo) {
	defer wg.Done()
	for image_info := range inch {
		var buf bytes.Buffer
		img := image_info.Image
		// 如果设置了最大宽度，则调整图片大小
		if maxWidth > 0 && uint(img.Bounds().Dx()) > maxWidth {
			img = resize.Resize(maxWidth, 0, img, resize.Lanczos3)
		}

		var err error = nil
		// 根据输出格式进行转换
		switch outputFormat {
		case "webp":
			err = webp.Encode(&buf, img, &webp.Options{Quality: float32(quality)})
		case "jpg", "jpeg":
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: int(quality)})
		case "png":
			err = png.Encode(&buf, img)
		case "gif":
			err = gif.Encode(&buf, img, &gif.Options{NumColors: 256})
		default:
			err = fmt.Errorf("unsupported output format: %s", outputFormat)
		}
		if err != nil {
			fmt.Printf("\nfile %s format %s fail %v\n", image_info.Name, outputFormat, err)
		}
		outch <- BytesInfo{image_info.Name, image_info.Size, buf}
	}
}

func interruptHandler(zipWriter *zip.Writer) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	// 启动一个goroutine来监听信号
	go func() {
		<-sigChan
		fmt.Println("\nInterrupt signal received. Closing ZIP writer...")
		err := zipWriter.Flush()
		if err != nil {
			fmt.Println("Error flush ZIP writer:", err)
		}
		// 关闭zip writer
		err = zipWriter.Close()
		if err != nil {
			fmt.Println("Error closing ZIP writer:", err)
		}
		fmt.Println("ZIP writer closed. Exiting...")
		os.Exit(0)
	}()
}

func writeFiles(root string, dirName string, totalFileNum int, wg *sync.WaitGroup, ch chan BytesInfo) {
	defer wg.Done()
	zipFileName := filepath.Join(root, fmt.Sprintf("%s.zip", dirName))
	zipFile, err := os.Create(zipFileName)
	if err != nil {
		fmt.Printf("\nopen %s fail:%v\n", zipFileName, err)
		return
	}
	defer zipFile.Close()
	zipWriter := zip.NewWriter(zipFile)
	interruptHandler(zipWriter)
	defer zipWriter.Close()
	log := internal.NewLogger(totalFileNum, dirName)
	for item := range ch {
		log.Add(item.Size, item.ImageBuf.Len())
		writer, err := zipWriter.Create(item.Name)
		if err != nil {
			fmt.Printf("\nwriter %s fail:%v\n", zipFileName, err)
		}
		_, err = item.ImageBuf.WriteTo(writer)
		if err != nil {
			fmt.Printf("\nimage write %s fail:%v\n", zipFileName, err)
		}
	}
	fmt.Printf("\n")
}

func main() {
	flag.Parse()

	// 获取当前目录
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Println("获取当前目录失败:", err)
		return
	}
	// 读取当前目录下的所有文件和文件夹
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		fmt.Println("读取目录失败:", err)
		return
	}
	// 遍历一级子目录
	for _, entry := range entries {
		if entry.IsDir() {
			path := entry.Name()
			err := processDirectory(path, currentDir, runtime.NumCPU())
			if err != nil {
				fmt.Printf("处理目录 %s 失败: %v\n", path, err)
			}
		}
	}
}
func totalImageFileNum(dirPath string) int {
	numTotalImageFile := 0
	// 获取目录下的所有文件
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return 0
	}
	for _, file := range files {
		if !file.IsDir() {
			// 检查文件是否为图片
			if isImageFile(file.Name()) {
				numTotalImageFile++
			}
		}
	}
	return numTotalImageFile
}
func processDirectory(dirPath string, root string, concurrency int) error {
	dirName := filepath.Base(dirPath)
	totalFileNum := totalImageFileNum(dirPath)
	if totalFileNum == 0 {
		return nil
	}
	wgRead := &sync.WaitGroup{}
	wgEncode := &sync.WaitGroup{}
	wgWrite := &sync.WaitGroup{}
	readEncodeCh := make(chan ImageInfo, concurrency)
	encodeWriteCh := make(chan BytesInfo, concurrency)
	wgRead.Add(1)
	go readFiles(dirPath, wgRead, readEncodeCh)
	for i := 0; i < concurrency; i++ {
		wgEncode.Add(1)
		go encodeFiles(*maxWidth, *format, *quality, wgEncode, readEncodeCh, encodeWriteCh)
	}
	wgWrite.Add(1)
	go writeFiles(root, dirName, totalFileNum, wgWrite, encodeWriteCh)
	wgRead.Wait()
	close(readEncodeCh)
	wgEncode.Wait()
	close(encodeWriteCh)
	wgWrite.Wait()
	return nil
}
