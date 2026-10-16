// SPDX-License-Identifier: MIT

// CentralHub scarica con un clic le ultime release da una lista di
// repository GitHub, e installa o aggiorna i programmi che contengono.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// Versione dell'app: la stessa di wails.json.
const Versione = "0.2.0"

//go:embed all:frontend/ui
var assets embed.FS

func main() {
	app := NuovaApp()
	err := wails.Run(&options.App{
		Title:     "CentralHub",
		Width:     980,
		Height:    660,
		MinWidth:  820,
		MinHeight: 540,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 14, G: 16, B: 22, A: 255},
		OnStartup:        app.avvio,
		OnShutdown:       app.chiusura,
		Linux: &linux.Options{
			Icon: windowIcon(appIcon), // ridotta: vedi icon_linux.go
		},
		Bind: []interface{}{app},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			About: &mac.AboutInfo{
				Title:   "CentralHub " + Versione,
				Message: "Scarica, installa e aggiorna le ultime release dai repository GitHub",
			},
		},
	})
	if err != nil {
		panic(err)
	}
}
