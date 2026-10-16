// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"centralhub/internal/config"
)

// Un repository che su macOS pubblica solo un .dmg (come quelli di Storytold):
// installazione, aggiornamento, disinstallazione, e un'app già messa a mano.
func TestMacSoloDmg(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	e := nuovoAmbiente(t)
	e.dmg = true
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	e.app.controlla(context.Background(), e.voce(v.ID))
	v = e.vista(v.ID)
	if v.Installabile != "si" || v.Scelta {
		t.Fatalf("un solo .dmg adatto: si installa senza chiedere. %+v", v)
	}
	v = e.aggiorna(v.ID, false)
	if !v.EsitoUpdate.OK || v.Installato.Tipo != "dmg" || v.Installato.Tag != "v1.0.0" || !strings.HasSuffix(v.Installato.Asset, ".dmg") {
		t.Fatalf("installazione da .dmg: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	percorso := v.Installato.Percorso
	exe := filepath.Join(percorso, "Contents", "MacOS", "Prova")
	if b, _ := os.ReadFile(exe); string(b) != "v1.0.0" {
		t.Errorf("contenuto installato: %q", b)
	}
	// aggiornamento: resta .dmg, niente .precedente
	e.tag = "v1.1.0"
	e.app.controlla(context.Background(), e.voce(v.ID))
	v = e.aggiorna(v.ID, false)
	if !v.EsitoUpdate.OK || v.Installato.Tag != "v1.1.0" || v.Installato.Precedente || v.Installato.Tipo != "dmg" {
		t.Fatalf("aggiornamento: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	if _, err := os.Stat(percorso + ".precedente"); err == nil {
		t.Error("la versione vecchia doveva sparire")
	}
	e.app.disinstalla(context.Background(), e.voce(v.ID), false)
	if v = e.vista(v.ID); v.Installato.Presente || !v.EsitoUpdate.OK {
		t.Fatalf("disinstallazione: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	if _, err := os.Stat(percorso); err == nil {
		t.Error("l'app doveva sparire")
	}

	// un'app messa a mano (zip scompattato: CentralHub non la conosce) viene
	// riconosciuta e il .dmg la sostituisce solo dopo la conferma
	mano := filepath.Join(e.base, "utente", filepath.Base(percorso))
	os.MkdirAll(filepath.Join(mano, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(mano, "Contents", "MacOS", "Prova"), []byte("a mano"), 0o755)
	e.app.controlla(context.Background(), e.voce(v.ID))
	v = e.vista(v.ID)
	if !v.Installato.Presente || v.Installato.Nostro || v.Installato.Tipo != "app" {
		t.Fatalf("app messa a mano: %+v", v.Installato)
	}
	if v = e.aggiorna(v.ID, false); v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "sostituzione") {
		t.Fatalf("senza conferma non si sostituisce: %+v", v.EsitoUpdate)
	}
	if v = e.aggiorna(v.ID, true); !v.EsitoUpdate.OK || v.Installato.Tipo != "dmg" || !v.Installato.Nostro {
		t.Fatalf("sostituzione confermata: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	if b, _ := os.ReadFile(filepath.Join(mano, "Contents", "MacOS", "Prova")); string(b) != "v1.1.0" {
		t.Errorf("l'app a mano non è stata sostituita: %q", b)
	}
}

// Accenti, apostrofi e spazi: nel nome del programma, nella cartella
// dell'utente e nella cartella d'archivio non devono rompere niente.
func TestMacCaratteriSpeciali(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	e := nuovoAmbiente(t)
	e.app.cartelle.Utente = filepath.Join(e.base, "Mie Applicazioni d'Utènte")
	master := filepath.Join(e.base, "Archivio d'Prova àè ù")
	v, err := e.app.SalvaRepo(config.Repo{Nome: "Mio Programmà d'Prova", Repo: "https://github.com/a/Prova",
		Locale: config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: master}})
	if err != nil {
		t.Fatal(err)
	}
	if v = e.archivia(v.ID); v.EsitoArchivio == nil || !v.EsitoArchivio.OK {
		t.Fatalf("archiviazione: %+v", v.EsitoArchivio)
	}
	if _, err := os.Stat(filepath.Join(master, "v1.0.0", e.nomeFile)); err != nil {
		t.Errorf("nella cartella d'archivio: %v", err)
	}
	v = e.aggiorna(v.ID, false)
	if !v.EsitoUpdate.OK || !v.Installato.Presente || !strings.HasPrefix(v.Installato.Percorso, e.app.cartelle.Utente) {
		t.Fatalf("installazione: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	if b, _ := os.ReadFile(filepath.Join(v.Installato.Percorso, "Contents", "MacOS", "Prova")); string(b) != "v1.0.0" {
		t.Errorf("contenuto: %q", b)
	}
	e.tag = "v1.1.0"
	e.app.controlla(context.Background(), e.voce(v.ID))
	if v = e.aggiorna(v.ID, false); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.1.0" || v.Installato.Precedente {
		t.Errorf("aggiornamento: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	e.app.disinstalla(context.Background(), e.voce(v.ID), false)
	if v = e.vista(v.ID); v.Installato.Presente || !v.EsitoUpdate.OK {
		t.Errorf("disinstallazione: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
}
