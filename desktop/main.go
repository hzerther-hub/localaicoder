// localai desktop — Wails 桌面壳（独立 Go 模块，CGO/WebView 构建与 CLI 静态构建分离）。
// 前端通过 window.go.main.App.* 直接调用绑定方法，事件经 window.runtime.EventsOn 推送。
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"localai/internal/config"
	"localai/internal/products"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed models.json
var bundledModels []byte

func main() {
	config.SetBundledModels(bundledModels)

	profile := products.Active()
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     profile.Title,
		Width:     1320,
		Height:    860,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 15, G: 17, B: 23, A: 255},
		OnStartup:        app.startup,
		OnDomReady:       app.domReady,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
			app.terms,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.Dark,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
