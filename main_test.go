package main

import (
	"archive/zip"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"doujinshi_compressor/internal"
)

func TestMain(m *testing.M) {
	internal.Out = io.Discard
	os.Exit(m.Run())
}

func writeTestPNG(t *testing.T, path string, width, height int, noise bool) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if noise {
				img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
			} else {
				img.Set(x, y, color.RGBA{255, 255, 255, 255})
			}
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

func zipEntryNames(t *testing.T, path string) []*zip.File {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("打开 zip: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader.File
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.png", "2.png", true},
		{"2.png", "10.png", true},
		{"1.png", "01.png", false},
		{"01.png", "1.png", false},
		{"10.png", "9.png", false},
		{"a2.png", "a10.png", true},
		{"1.png", "1.png", false},
	}
	for _, tc := range cases {
		if got := naturalLess(tc.a, tc.b); got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestNormalizeFormat(t *testing.T) {
	valid := map[string]string{
		"webp": "webp", "JPG": "jpg", "jpeg": "jpg", ".png": "png", " gif ": "gif",
	}
	for in, want := range valid {
		got, err := normalizeFormat(in)
		if err != nil || got != want {
			t.Errorf("normalizeFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeFormat("bmp"); err == nil {
		t.Error("normalizeFormat(\"bmp\") 应当报错")
	}
}

// 编码耗时不均时，zip 内的页序仍必须与文件名顺序一致。
func TestProcessDirectoryPreservesOrder(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()

	// 故意不补零，检验自然排序：1,2,...,12 而不是 1,10,11,...
	names := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}
	for i, name := range names {
		writeTestPNG(t, filepath.Join(srcDir, name+".png"), 200, 150, i%2 == 1)
	}

	opts := options{format: "webp", quality: 80, maxWidth: 120, jobs: 6, outDir: outDir}
	st, err := processDirectory(context.Background(), srcDir, opts)
	if err != nil {
		t.Fatalf("processDirectory: %v", err)
	}
	if st.written != len(names) || st.skipped != 0 {
		t.Fatalf("written=%d skipped=%d, want %d/0 (failures: %v)", st.written, st.skipped, len(names), st.failures)
	}

	zipPath := filepath.Join(outDir, filepath.Base(srcDir)+".zip")
	files := zipEntryNames(t, zipPath)
	var got, want []string
	for _, f := range files {
		got = append(got, f.Name)
		if f.UncompressedSize64 == 0 {
			t.Errorf("条目 %s 是空文件", f.Name)
		}
		if f.Method != zip.Store {
			t.Errorf("条目 %s 使用了压缩方法 %d，期望 zip.Store", f.Name, f.Method)
		}
	}
	for _, name := range names {
		want = append(want, name+".webp")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zip 条目顺序 = %v, want %v", got, want)
	}
}

// 单张图片解码失败时应跳过，且不能让后续图片错位或留下空条目。
func TestProcessDirectorySkipsUndecodable(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()

	names := []string{"1", "2", "3", "4", "5"}
	for _, name := range names {
		writeTestPNG(t, filepath.Join(srcDir, name+".png"), 120, 90, false)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "3.png"), []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := options{format: "webp", quality: 80, maxWidth: 0, jobs: 3, outDir: outDir}
	st, err := processDirectory(context.Background(), srcDir, opts)
	if err != nil {
		t.Fatalf("processDirectory: %v", err)
	}
	if st.written != 4 || st.skipped != 1 {
		t.Fatalf("written=%d skipped=%d, want 4/1 (failures: %v)", st.written, st.skipped, len(st.failures))
	}

	zipPath := filepath.Join(outDir, filepath.Base(srcDir)+".zip")
	var got, want []string
	for _, f := range zipEntryNames(t, zipPath) {
		got = append(got, f.Name)
		if f.UncompressedSize64 == 0 {
			t.Errorf("条目 %s 是空文件", f.Name)
		}
	}
	want = []string{"1.webp", "2.webp", "4.webp", "5.webp"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zip 条目顺序 = %v, want %v", got, want)
	}
}

func TestWriteResultsHandlesCancelWithoutZip(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	for _, name := range []string{"1", "2", "3"} {
		writeTestPNG(t, filepath.Join(srcDir, name+".png"), 80, 60, false)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟一开始就收到中断

	opts := options{format: "png", quality: 75, maxWidth: 0, jobs: 2, outDir: outDir}
	st, err := processDirectory(ctx, srcDir, opts)
	if err != nil {
		t.Fatalf("processDirectory: %v", err)
	}
	if st.written != 0 {
		t.Fatalf("written = %d, want 0", st.written)
	}
	if _, err := os.Stat(filepath.Join(outDir, filepath.Base(srcDir)+".zip")); !os.IsNotExist(err) {
		t.Errorf("中断且没有任何输出时不应留下 zip, stat err = %v", err)
	}
}

func TestIsImageFile(t *testing.T) {
	for _, name := range []string{"a.jpg", "A.JPEG", "b.png", "c.gif", "d.WEBP"} {
		if !isImageFile(name) {
			t.Errorf("isImageFile(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"a.txt", "b.zip", "noext"} {
		if isImageFile(name) {
			t.Errorf("isImageFile(%q) = true, want false", name)
		}
	}
}
