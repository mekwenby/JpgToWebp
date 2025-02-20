package main

import (
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/chai2010/webp"
)

func main() {
	// 创建应用
	myApp := app.New()
	myWindow := myApp.NewWindow("Image to WebP Converter")
	myWindow.Resize(fyne.NewSize(600, 450))

	// UI组件
	inputLabel := widget.NewLabel("输入文件夹: 未选择")
	outputLabel := widget.NewLabel("输出文件夹: 未选择")
	qualityLabel := widget.NewLabel("质量: 80")
	qualitySlider := widget.NewSlider(0, 100)
	qualitySlider.Value = 80
	losslessCheck := widget.NewCheck("无损压缩", nil)

	// 最大线程数（默认 CPU 核心数，最多 2 倍）
	maxThreads := runtime.NumCPU()
	if maxThreads > 1 {
		maxThreads = min(maxThreads*2, runtime.NumCPU()*2) // 最多不超过 CPU 数量的 2 倍
	}
	threadsLabel := widget.NewLabel(fmt.Sprintf("最大线程数: %d", maxThreads))
	threadsSlider := widget.NewSlider(1, float64(runtime.NumCPU()*2))
	threadsSlider.Value = float64(maxThreads)
	threadsSlider.OnChanged = func(value float64) {
		threadsLabel.SetText(fmt.Sprintf("最大线程数: %d", int(value)))
	}

	// 进度条
	progressBar := widget.NewProgressBar()
	progressBar.Min = 0
	progressBar.Max = 1
	progressBar.Value = 0

	// 状态
	statusLabel := widget.NewLabel("状态: 就绪")

	// 选择输入文件夹
	inputButton := widget.NewButton("选择输入文件夹", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				inputLabel.SetText("输入文件夹: " + uri.Path())
			}
		}, myWindow)
	})

	// 选择输出文件夹
	outputButton := widget.NewButton("选择输出文件夹", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				outputLabel.SetText("输出文件夹: " + uri.Path())
			}
		}, myWindow)
	})

	// 质量滑块变化
	qualitySlider.OnChanged = func(value float64) {
		qualityLabel.SetText("质量: " + strconv.Itoa(int(value)))
	}

	// 转换按钮
	var convertButton *widget.Button
	convertButton = widget.NewButton("开始转换", func() {
		inputPath := strings.TrimPrefix(inputLabel.Text, "输入文件夹: ")
		outputPath := strings.TrimPrefix(outputLabel.Text, "输出文件夹: ")

		if inputPath == "未选择" || outputPath == "未选择" {
			dialog.ShowError(fmt.Errorf("请先选择输入和输出文件夹"), myWindow)
			return
		}

		quality := int(qualitySlider.Value)
		lossless := losslessCheck.Checked
		maxThreads := int(threadsSlider.Value)
		statusLabel.SetText("状态: 转换中...")
		convertButton.Disable() // 禁用按钮

		go func() {
			err := convertFolderToWebP(inputPath, outputPath, quality, lossless, maxThreads, progressBar, statusLabel)
			if err != nil {
				dialog.ShowError(err, myWindow)
				statusLabel.SetText("状态: 错误")
			} else {
				statusLabel.SetText("状态: 转换完成")
				dialog.ShowInformation("成功", "所有图片转换完成！", myWindow)
			}
			convertButton.Enable() // 启用按钮
		}()
	})

	// 布局
	content := container.NewVBox(
		inputLabel,
		inputButton,
		outputLabel,
		outputButton,
		qualityLabel,
		qualitySlider,
		losslessCheck,
		threadsLabel,
		threadsSlider,
		progressBar,
		convertButton,
		statusLabel,
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

func convertFolderToWebP(inputDir, outputDir string, quality int, lossless bool, maxThreads int, progressBar *widget.ProgressBar, status *widget.Label) error {
	// 创建输出根目录
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("无法创建输出目录: %v", err)
	}

	// 收集所有图片文件
	var files []string
	err := filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("输入文件夹中没有支持的图片文件")
	}

	// 设置进度条最大值
	progressBar.Max = float64(len(files))
	progressBar.Value = 0

	// 使用信号量控制并发
	sem := make(chan struct{}, maxThreads)
	var wg sync.WaitGroup
	errChan := make(chan error, 1)
	processed := 0

	for _, inputPath := range files {
		sem <- struct{}{} // 获取信号量
		wg.Add(1)

		// 计算输出路径
		relPath, err := filepath.Rel(inputDir, inputPath)
		if err != nil {
			return err
		}
		ext := strings.ToLower(filepath.Ext(inputPath))
		outputPath := filepath.Join(outputDir, strings.TrimSuffix(relPath, ext)+".webp")
		outputDirPath := filepath.Dir(outputPath)
		if err := os.MkdirAll(outputDirPath, 0755); err != nil {
			return fmt.Errorf("无法创建子目录: %v", err)
		}

		// 并行转换
		go func(input, output string) {
			defer wg.Done()
			defer func() { <-sem }() // 释放信号量

			if err := convertToWebP(input, output, quality, lossless); err != nil {
				select {
				case errChan <- err:
				default:
				}
				return
			}

			// 更新进度条和状态（线程安全）
			processed++
			progressBar.SetValue(float64(processed))
			status.SetText(fmt.Sprintf("状态: 已处理 %d/%d 个文件", processed, len(files)))
		}(inputPath, outputPath)
	}

	// 等待所有转换完成
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// 检查是否有错误
	if len(errChan) > 0 {
		return <-errChan
	}

	return nil
}

func convertToWebP(inputPath, outputPath string, quality int, lossless bool) error {
	input, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("无法打开输入文件 %s: %v", inputPath, err)
	}
	defer input.Close()

	var img image.Image
	ext := strings.ToLower(filepath.Ext(inputPath))

	switch ext {
	case ".jpg", ".jpeg":
		img, err = jpeg.Decode(input)
	case ".png":
		img, err = png.Decode(input)
	case ".gif":
		img, err = gif.Decode(input)
	default:
		return fmt.Errorf("不支持的图片格式: %s", ext)
	}

	if err != nil {
		return fmt.Errorf("解码图片 %s 失败: %v", inputPath, err)
	}

	output, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("无法创建输出文件 %s: %v", outputPath, err)
	}
	defer output.Close()

	options := &webp.Options{
		Quality:  float32(quality),
		Lossless: lossless,
	}

	return webp.Encode(output, img, options)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
