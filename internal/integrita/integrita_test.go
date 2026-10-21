// SPDX-License-Identifier: MIT

package integrita

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func scrivi(t *testing.T, p, contenuto string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(contenuto), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCartellaIntegraDiversaMancante(t *testing.T) {
	d := t.TempDir()
	scrivi(t, filepath.Join(d, "App.exe"), "programma")
	scrivi(t, filepath.Join(d, "lib", "ffmpeg"), strings.Repeat("f", 10000))
	scrivi(t, filepath.Join(d, "lib", "dati.bin"), "dati")
	m, err := Calcola(d)
	if err != nil || len(m.File) != 3 || m.File["lib/ffmpeg"] == "" {
		t.Fatalf("manifesto: %+v %v", m, err)
	}
	if e := Confronta(d, m); !e.Integro() || e.Totale != 3 {
		t.Errorf("appena installato deve essere integro: %+v", e)
	}
	// un file nuovo (le impostazioni accanto al programma) non conta
	scrivi(t, filepath.Join(d, "impostazioni.json"), "{}")
	if !Confronta(d, m).Integro() {
		t.Error("un file nuovo non deve contare")
	}
	// un file cambiato di un byte, uno sparito
	scrivi(t, filepath.Join(d, "lib", "ffmpeg"), strings.Repeat("f", 9999)+"g")
	os.Remove(filepath.Join(d, "lib", "dati.bin"))
	e := Confronta(d, m)
	if e.Integro() || len(e.Diversi) != 1 || e.Diversi[0] != "lib/ffmpeg" || len(e.Mancano) != 1 || e.Mancano[0] != "lib/dati.bin" {
		t.Errorf("file cambiato e file sparito: %+v", e)
	}
}

func TestFileSingolo(t *testing.T) {
	d := t.TempDir()
	f := filepath.Join(d, "App.AppImage")
	scrivi(t, f, "un solo file")
	m, err := Calcola(f)
	if err != nil || len(m.File) != 1 {
		t.Fatal(m, err)
	}
	if !Confronta(f, m).Integro() {
		t.Error("file singolo appena annotato")
	}
	scrivi(t, f, "un solo file, alterato")
	if e := Confronta(f, m); e.Integro() || len(e.Diversi) != 1 {
		t.Errorf("file singolo alterato: %+v", e)
	}
	os.Remove(f)
	if e := Confronta(f, m); len(e.Mancano) != 1 {
		t.Errorf("file singolo sparito: %+v", e)
	}
}

func TestCollegamentiSimbolici(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("i collegamenti simbolici su Windows chiedono privilegi")
	}
	d := t.TempDir()
	scrivi(t, filepath.Join(d, "Versions", "A", "lib"), "x")
	if err := os.Symlink("Versions/A/lib", filepath.Join(d, "lib")); err != nil {
		t.Fatal(err)
	}
	m, err := Calcola(d)
	if err != nil || m.File["lib"] != "link:Versions/A/lib" {
		t.Fatalf("collegamento: %+v %v", m, err)
	}
	if !Confronta(d, m).Integro() {
		t.Error("collegamento intatto")
	}
	os.Remove(filepath.Join(d, "lib"))
	os.Symlink("Versions/B/lib", filepath.Join(d, "lib"))
	if e := Confronta(d, m); len(e.Diversi) != 1 {
		t.Errorf("collegamento spostato: %+v", e)
	}
}

func TestSalvaCaricaElimina(t *testing.T) {
	reg := filepath.Join(t.TempDir(), "installati")
	d := t.TempDir()
	scrivi(t, filepath.Join(d, "a"), "a")
	m, _ := Calcola(d)
	if _, ok := Carica(reg, "Prova"); ok {
		t.Fatal("non c'è ancora niente")
	}
	if err := Salva(reg, "Prova", m); err != nil {
		t.Fatal(err)
	}
	if c, ok := Carica(reg, "Prova"); !ok || c.File["a"] != m.File["a"] {
		t.Errorf("riletto: %+v %v", c, ok)
	}
	// il file del registro delle installazioni non si confonde con questo
	if _, err := os.Stat(filepath.Join(reg, "Prova.json")); err == nil {
		t.Error("il manifesto non deve occupare il nome del registro")
	}
	Elimina(reg, "Prova")
	if _, ok := Carica(reg, "Prova"); ok {
		t.Error("eliminato ma ancora lì")
	}
}
