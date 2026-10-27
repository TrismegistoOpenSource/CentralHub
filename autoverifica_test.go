// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"centralhub/internal/autoverifica"
	"centralhub/internal/config"
)

func TestVersioneComeWails(t *testing.T) {
	b, err := os.ReadFile("wails.json")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`"productVersion":\s*"([^"]+)"`).FindSubmatch(b)
	if m == nil || string(m[1]) != Versione {
		t.Fatalf("Versione %q, wails.json %q", Versione, m)
	}
}

// Fuori da un pacchetto (qui, il binario di prova) non c'è un INTEGRITA.txt:
// l'esito è «non verificabile», senza rete, e viene scritto nella cartella di
// configurazione (qui temporanea, mai quella vera).
func TestAutoverificaFuoriDalPacchetto(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.VariabileCartella, dir)
	a := NuovaApp()
	s := a.AutoverificaProgramma()
	if s.Esito != autoverifica.EsitoNonVerificabile {
		t.Fatalf("esito %q", s.Esito)
	}
	if ancora := a.AutoverificaProgramma(); ancora.Esito != s.Esito {
		t.Error("la seconda chiamata dà un esito diverso")
	}
	if _, err := os.Stat(filepath.Join(dir, autoverifica.NomeFileStato)); err != nil {
		t.Errorf("l'esito non è stato scritto: %v", err)
	}
}
