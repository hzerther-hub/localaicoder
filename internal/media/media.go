// Package media 媒体文件分类 + 图片 data URL（对译 Python media.py 的内核部分）。
// UI 播放/缩略图交给前端 <audio>/<video> 与 ffmpeg，内核只做附件归类。
package media

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"localai/internal/config"
)

var imageExts = map[string]bool{
	".png": true, ".gif": true, ".jpg": true, ".jpeg": true, ".bmp": true,
	".webp": true, ".ico": true, ".tif": true, ".tiff": true,
}

var audioExts = map[string]bool{
	".wav": true, ".mp3": true, ".flac": true, ".ogg": true, ".m4a": true,
	".aac": true, ".wma": true, ".opus": true,
}

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".webm": true, ".avi": true, ".mov": true,
	".flv": true, ".wmv": true, ".ts": true,
}

// Classify 文件类型归类："image" / "audio" / "video" / ""（普通文件）。
func Classify(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case imageExts[ext]:
		return "image"
	case audioExts[ext]:
		return "audio"
	case videoExts[ext]:
		return "video"
	}
	return ""
}

// ImageToDataURL 读图片转 base64 data URL；超过 ATTACH_IMAGE_MAX_PIX 等比缩小
// （仅 png/jpg 解码重编码，其余格式原样 base64）。失败返回错误。
func ImageToDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(path))
	maxPix := config.AttachImageMaxPix

	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
		if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
			b := img.Bounds()
			w, h := b.Dx(), b.Dy()
			scale := 1.0
			if w > maxPix || h > maxPix {
				if w >= h {
					scale = float64(maxPix) / float64(w)
				} else {
					scale = float64(maxPix) / float64(h)
				}
			}
			if scale < 1.0 {
				nw, nh := int(float64(w)*scale), int(float64(h)*scale)
				if nw < 1 {
					nw = 1
				}
				if nh < 1 {
					nh = 1
				}
				resized := nearestScale(img, nw, nh)
				var buf bytes.Buffer
				if ext == ".png" {
					err = png.Encode(&buf, resized)
				} else {
					err = jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 88})
				}
				if err == nil {
					mime := "image/png"
					if ext != ".png" {
						mime = "image/jpeg"
					}
					return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
				}
				// 重编码失败退回原图
			}
		}
	}
	mime := mimeOf(ext)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// ThumbDataURL 从图片 dataURL 生成等比缩略图 dataURL（最长边 ≤ maxPix；超过才缩放）。
// 手机端只显示缩略图即可，用它大幅减小跨网传输体积。失败/已足够小则返回原 dataURL。
func ThumbDataURL(dataURL string, maxPix int) string {
	if maxPix <= 0 {
		maxPix = 512
	}
	raw := dataURL
	if i := strings.Index(raw, ","); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimPrefix(raw, "data:")
	if raw == "" {
		return dataURL
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return dataURL
	}
	img, _, err := image.Decode(bytes.NewReader(dec))
	if err != nil {
		return dataURL
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxPix && h <= maxPix {
		return dataURL
	}
	var nw, nh int
	if w >= h {
		nw, nh = maxPix, int(float64(h)*float64(maxPix)/float64(w))
	} else {
		nw, nh = int(float64(w)*float64(maxPix)/float64(h)), maxPix
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	resized := nearestScale(img, nw, nh)
	var buf bytes.Buffer
	if err := png.Encode(&buf, resized); err != nil {
		return dataURL
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func mimeOf(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

// nearestScale 最近邻等比缩放（标准库实现，无外部依赖）。
func nearestScale(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y*srcH/h
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*srcW/w
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	return dst
}

// FileSizeStr 人类可读文件大小。
func FileSizeStr(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "?"
	}
	n := st.Size()
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
