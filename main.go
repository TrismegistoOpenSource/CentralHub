// SPDX-License-Identifier: MIT

// CentralHub scarica con un clic le ultime release da una lista di
// repository GitHub, e installa o aggiorna i programmi che contengono.
package main

import (
	"context"
	"embed"
	"io/fs"
	"strings"

	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"centralhub/internal/config"
	"centralhub/internal/licenze"
)

// Versione dell'app: la stessa di wails.json.
const Versione = "0.2.4"

//go:embed all:frontend/ui
var assets embed.FS

// I testi delle licenze (raccolti a ogni build in internal/licenze/testi/raccolta) viaggiano dentro l'eseguibile.
//
//go:embed all:internal/licenze/testi
var testiLicenze embed.FS

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

// usaLicenzeIncorporate passa al pacchetto licenze i testi incorporati (percorso → testo).
func usaLicenzeIncorporate() {
	m := map[string]string{}
	_ = fs.WalkDir(testiLicenze, "internal/licenze", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, e := testiLicenze.ReadFile(p); e == nil {
				m[strings.TrimPrefix(p, "internal/licenze/")] = string(b)
			}
		}
		return nil
	})
	licenze.Usa(m)
}

func main() {
	usaLicenzeIncorporate()
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
		Menu: menuMac(app),
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

// menuMac: su macOS la voce «Informazioni su CentralHub» del menu dell'app apre la finestra delle licenze dentro il programma (invece del
// riquadro di sistema, che non può elencarle). Su Windows e Linux non c'è menu: la finestra si apre con il clic sul numero di versione.
func menuMac(app *App) *menu.Menu {
	if runtime.GOOS != "darwin" {
		return nil
	}
	m := menu.NewMenu()
	m.Append(menu.SubMenu("CentralHub", menu.NewMenuFromItems(
		menu.Text("Informazioni su CentralHub e licenze…", nil, func(_ *menu.CallbackData) {
			if app.ctx != nil {
				wr.EventsEmit(app.ctx, "apri-licenze")
			}
		}),
		menu.Separator(),
		menu.Text("Nascondi CentralHub", keys.CmdOrCtrl("h"), func(_ *menu.CallbackData) { wr.Hide(app.ctx) }),
		menu.Separator(),
		menu.Text("Esci da CentralHub", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) { wr.Quit(app.ctx) }),
	)))
	m.Append(menu.EditMenu()) // copia e incolla nei campi di testo
	m.Append(menu.WindowMenu())
	return m
}
