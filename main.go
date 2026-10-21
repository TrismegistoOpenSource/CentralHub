// SPDX-License-Identifier: MIT

// CentralHub scarica con un clic le ultime release da una lista di
// repository GitHub, e installa o aggiorna i programmi che contengono.
package main

import (
	"context"
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"centralhub/internal/config"
)

// Versione dell'app: la stessa di wails.json.
const Versione = "0.2.2"

//go:embed all:frontend/ui
var assets embed.FS

// identificativoIstanza è ciò che due CentralHub usano per riconoscersi: il
// secondo che parte non si apre, e fa tornare avanti la finestra del primo.
const identificativoIstanza = "it.trismegistoopensource.centralhub"

// unaSolaIstanza: due copie aperte insieme si sovrascriverebbero
// config.json e lo stato. Non vale in modalità di prova (configurazione
// spostata): le prove possono avere più copie, ognuna con la sua cartella.
func unaSolaIstanza(app *App) *options.SingleInstanceLock {
	if _, isolata, _ := config.Cartella(); isolata {
		return nil
	}
	return &options.SingleInstanceLock{
		UniqueId: identificativoIstanza,
		OnSecondInstanceLaunch: func(options.SecondInstanceData) {
			app.mu.Lock()
			ctx := app.ctx
			app.mu.Unlock()
			if ctx == nil {
				return
			}
			portaAvanti(ctx)
		},
	}
}

func portaAvanti(ctx context.Context) {
	wr.WindowUnminimise(ctx)
	wr.WindowShow(ctx)
}

func main() {
	app := NuovaApp()
	err := wails.Run(&options.App{
		SingleInstanceLock: unaSolaIstanza(app),
		Title:              "CentralHub",
		Width:              980,
		Height:             660,
		MinWidth:           820,
		MinHeight:          540,
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
