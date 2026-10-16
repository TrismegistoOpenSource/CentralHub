// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCandidatiWindows(t *testing.T) {
	tipi := func(nomi ...string) string {
		c, err := Candidati(nomi, "windows", "amd64", "")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range c {
			x := p.Nome + "=" + p.Tipo
			if p.Consigliato {
				x += "*"
			}
			out = append(out, x)
		}
		return strings.Join(out, " ")
	}
	for _, k := range []struct {
		atteso string
		nomi   []string
	}{
		// lo zip dei progetti: uno solo, e lo gestisce CentralHub
		{"App-Windows.zip=portable*", []string{"App-Windows.zip", "App-Intel.zip", "App-Linux-x86_64.AppImage"}},
		// l'installatore si riconosce dal nome o dall'estensione
		{"App-Setup-1.2.0.exe=installatore*", []string{"App-Setup-1.2.0.exe", "App-1.2.0.dmg"}},
		{"app_1.2.0_x64_en-US.msi=installatore*", []string{"app_1.2.0_x64_en-US.msi", "app_1.2.0_arm64_en-US.msi"}},
		// portable e installatore insieme: si consiglia quello che CentralHub gestisce da sé
		{"App-win64.zip=portable* App-win64-installer.exe=installatore", []string{"App-win64.zip", "App-win64-installer.exe"}},
		// un exe "portable" è il programma
		{"App-portable.exe=eseguibile*", []string{"App-portable.exe"}},
		// un exe senza indicazioni: può essere il programma o il suo installatore, si chiede
		{"App-1.2.0.exe=eseguibile App-1.2.0.exe=installatore", []string{"App-1.2.0.exe", "App-1.2.0.exe.blockmap", "latest.yml"}},
		// non è per Windows, non è il programma
		{"", []string{"app-linux-amd64.exe", "app-cli-win64.zip", "app-darwin.zip", "App-sorgente.zip"}},
	} {
		if got := tipi(k.nomi...); got != k.atteso {
			t.Errorf("%v:\n  avuto  %q\n  atteso %q", k.nomi, got, k.atteso)
		}
	}
	// con un modello decide l'utente quale file; il genere viene dall'estensione
	if c, err := Candidati([]string{"a.msi", "b.zip"}, "windows", "amd64", "*.MSI"); err != nil || len(c) != 1 || c[0].Tipo != TipoInstallatore || !c[0].Consigliato {
		t.Errorf("modello: %+v %v", c, err)
	}
	// sugli altri sistemi un installatore di Windows non è un candidato
	if c, _ := Candidati([]string{"App-Setup.exe", "App.msi"}, "linux", "amd64", ""); len(c) != 0 {
		t.Errorf("linux: %+v", c)
	}
}

func TestScegli(t *testing.T) {
	zip := Pacchetto{Nome: "app-2.0.0-windows-x64-portable.zip", Tipo: TipoPortable, Consigliato: true}
	msi := Pacchetto{Nome: "app-2.0.0-windows-x64.msi", Tipo: TipoInstallatore}
	c := []Pacchetto{zip, msi}
	var scelta *ErrScelta
	// niente di scelto e niente di installato: si chiede
	if _, err := Scegli(c, Pacchetto{}, Pacchetto{}); !errors.As(err, &scelta) || len(scelta.Pacchetti) != 2 {
		t.Errorf("due pacchetti: %v", err)
	}
	if _, err := Scegli(nil, Pacchetto{}, Pacchetto{}); !errors.Is(err, ErrNessunAsset) {
		t.Errorf("nessun pacchetto: %v", err)
	}
	// la scelta dell'utente
	if p, err := Scegli(c, Pacchetto{Nome: msi.Nome}, Pacchetto{}); err != nil || p != msi {
		t.Errorf("scelta esplicita: %+v %v", p, err)
	}
	if _, err := Scegli(c, Pacchetto{Nome: "non-c-e.zip"}, Pacchetto{}); err == nil {
		t.Error("un pacchetto che la release non ha: deve dirlo")
	}
	// un aggiornamento resta del genere installato, anche se il nome ha un'altra versione
	if p, err := Scegli(c, Pacchetto{}, Pacchetto{Nome: "app-1.9.3-windows-x64.msi", Tipo: TipoInstallatore}); err != nil || p != msi {
		t.Errorf("aggiornamento di un installatore: %+v %v", p, err)
	}
	if p, err := Scegli(c, Pacchetto{}, Pacchetto{Tipo: TipoPortable}); err != nil || p != zip {
		t.Errorf("aggiornamento di un portable: %+v %v", p, err)
	}
	if _, err := Scegli([]Pacchetto{msi}, Pacchetto{}, Pacchetto{Tipo: TipoPortable}); err == nil || errors.As(err, &scelta) {
		t.Errorf("la release non ha più il genere installato: %v", err)
	}
	// due dello stesso genere: vale quello con la stessa forma del nome
	lite := Pacchetto{Nome: "app-lite-2.0.0-windows-x64-portable.zip", Tipo: TipoPortable}
	if p, err := Scegli([]Pacchetto{lite, zip}, Pacchetto{}, Pacchetto{Nome: "app-1.0.0-windows-x64-portable.zip", Tipo: TipoPortable}); err != nil || p != zip {
		t.Errorf("stessa forma del nome: %+v %v", p, err)
	}
	if _, err := Scegli([]Pacchetto{lite, zip}, Pacchetto{}, Pacchetto{Nome: "altro.zip", Tipo: TipoPortable}); !errors.As(err, &scelta) {
		t.Errorf("due dello stesso genere e nessuno riconoscibile: %v", err)
	}
	// l'exe senza indicazioni compare due volte: va detto anche il genere
	exe := []Pacchetto{{Nome: "App.exe", Tipo: TipoEseguibile}, {Nome: "App.exe", Tipo: TipoInstallatore}}
	if _, err := Scegli(exe, Pacchetto{Nome: "App.exe"}, Pacchetto{}); !errors.As(err, &scelta) {
		t.Errorf("exe senza genere: %v", err)
	}
	if p, err := Scegli(exe, Pacchetto{Nome: "App.exe", Tipo: TipoInstallatore}, Pacchetto{}); err != nil || p.Tipo != TipoInstallatore {
		t.Errorf("exe come installatore: %+v %v", p, err)
	}
}

func TestWindowsEseguibileSolo(t *testing.T) {
	p := nuovaProva(t)
	file := filepath.Join(p.base, "Prova-1.2.0-portable.exe")
	scrivi(t, file, "exe uno")
	r := p.richiesta("windows", Utente, "v1.2.0", file)
	r.Tipo = TipoEseguibile
	e, err := Installa(r)
	if err != nil {
		t.Fatal(err)
	}
	// prende il nome del programma: il collegamento non cambia a ogni versione
	if leggi(t, filepath.Join(e.Percorso, "Prova.exe")) != "exe uno" || e.Percorso != filepath.Join(p.c.Utente, "Prova") {
		t.Errorf("exe solo: %+v", e)
	}
	if s := LeggiStato(p.reg, "Prova", "windows", p.c); !s.Nostro || s.Tipo != TipoEseguibile || s.Asset != "Prova-1.2.0-portable.exe" {
		t.Errorf("stato: %+v", s)
	}
	r.File = filepath.Join(p.base, "non-un-exe.zip")
	scrivi(t, r.File, "x")
	if _, err := Installa(r); err == nil {
		t.Error("un file che non è un exe non si installa come exe")
	}
}

// sistemaFinto sostituisce l'elenco delle app installate e gli installatori
// del sistema con una mappa in memoria.
type sistemaFinto struct {
	voci     map[string]VoceSistema
	lanciati []string
	// installa è ciò che fa l'installatore finto: modifica le voci e
	// restituisce il codice di uscita.
	installa func(file string) int
	tolti    []string
}

func nuovoSistemaFinto(t *testing.T) *sistemaFinto {
	f := &sistemaFinto{voci: map[string]VoceSistema{
		"HKLM64:{AAA}": {Chiave: "HKLM64:{AAA}", Nome: "Un altro programma", Versione: "3.0"},
	}}
	vLeggi, vElencata, vInst, vDis, vAttesa := leggiVoci, voceElencata, avviaInstallatore, avviaDisinstallatore, attesaVoce
	t.Cleanup(func() {
		leggiVoci, voceElencata, avviaInstallatore, avviaDisinstallatore, attesaVoce = vLeggi, vElencata, vInst, vDis, vAttesa
	})
	attesaVoce = 0
	leggiVoci = func() map[string]VoceSistema {
		copia := map[string]VoceSistema{}
		for k, v := range f.voci {
			copia[k] = v
		}
		return copia
	}
	voceElencata = func(chiave string) bool { _, c := f.voci[chiave]; return c }
	avviaInstallatore = func(_ context.Context, file string) (int, error) {
		f.lanciati = append(f.lanciati, filepath.Base(file))
		return f.installa(file), nil
	}
	avviaDisinstallatore = func(_ context.Context, v VoceSistema) error {
		f.tolti = append(f.tolti, v.Chiave)
		delete(f.voci, v.Chiave)
		return nil
	}
	return f
}

func TestInstallatore(t *testing.T) {
	p := nuovaProva(t)
	f := nuovoSistemaFinto(t)
	dove := filepath.Join(p.base, "Programmi", "Prova")
	f.installa = func(file string) int {
		scrivi(t, filepath.Join(dove, "Prova.exe"), "installato")
		f.voci["HKCU:Prova"] = VoceSistema{Chiave: "HKCU:Prova", Nome: "Prova 1.0", Versione: "1.0", Posizione: dove, Disinstalla: `"` + dove + `\unins.exe"`}
		return 0
	}
	setup := filepath.Join(p.base, "Prova-Setup-1.0.exe")
	scrivi(t, setup, "setup")
	r := p.richiesta("windows", "", "v1.0", setup) // un installatore non ha bisogno dell'ambito
	r.Tipo = TipoInstallatore
	e, err := Installa(r)
	if err != nil {
		t.Fatal(err)
	}
	if e.Percorso != dove || len(f.lanciati) != 1 || len(p.script) != 0 {
		t.Errorf("installatore: %+v lanciati %v", e, f.lanciati)
	}
	s := LeggiStato(p.reg, "Prova", "windows", p.c)
	if !s.Presente || !s.Nostro || s.Tag != "v1.0" || s.Tipo != TipoInstallatore || s.Percorso != dove || s.Ambito != "" {
		t.Errorf("stato dopo l'installatore: %+v", s)
	}
	// aggiornamento: l'installatore nuovo aggiorna la stessa voce
	f.installa = func(string) int {
		v := f.voci["HKCU:Prova"]
		v.Versione, v.Nome = "2.0", "Prova 2.0"
		f.voci["HKCU:Prova"] = v
		return 0
	}
	r.Tag = "v2.0"
	if _, err := Installa(r); err != nil {
		t.Fatal(err)
	}
	if s = LeggiStato(p.reg, "Prova", "windows", p.c); s.Tag != "v2.0" || !s.Presente {
		t.Errorf("stato dopo l'aggiornamento: %+v", s)
	}
	// l'utente lo toglie da «App installate» di Windows: CentralHub se ne accorge
	voce := f.voci["HKCU:Prova"]
	delete(f.voci, "HKCU:Prova")
	if s = LeggiStato(p.reg, "Prova", "windows", p.c); s.Presente {
		t.Errorf("tolto fuori da CentralHub ma risulta installato: %+v", s)
	}
	f.voci["HKCU:Prova"] = voce
	// disinstallazione: la fa il disinstallatore del programma, non CentralHub
	d := Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "windows"}
	es, err := Disinstalla(d)
	if err != nil || len(f.tolti) != 1 || f.tolti[0] != "HKCU:Prova" || es.Percorso != dove {
		t.Fatalf("disinstallazione: %+v %v, tolti %v", es, err, f.tolti)
	}
	if !esiste(filepath.Join(dove, "Prova.exe")) {
		t.Error("i file di un programma messo dal suo installatore li toglie il suo disinstallatore, non CentralHub")
	}
	if esiste(fileRegistro(p.reg, "Prova")) || LeggiStato(p.reg, "Prova", "windows", p.c).Presente {
		t.Error("dopo la disinstallazione non deve restare l'annotazione")
	}

	// l'utente chiude l'installatore: niente di nuovo, codice diverso da zero
	q := nuovaProva(t)
	f.installa = func(string) int { return 2 }
	r = q.richiesta("windows", "", "v1.0", setup)
	r.Tipo = TipoInstallatore
	if _, err := Installa(r); err == nil || !strings.Contains(err.Error(), "codice 2") {
		t.Errorf("installatore annullato: %v", err)
	}
	// esce con zero ma non lascia niente: non si annota un'installazione che non c'è
	f.installa = func(string) int { return 0 }
	if _, err := Installa(r); err == nil || esiste(fileRegistro(q.reg, "Prova")) {
		t.Errorf("installatore che non installa: %v", err)
	}
	// sugli altri sistemi non si sa avviare
	r.GOOS = "linux"
	if _, err := Installa(r); err == nil {
		t.Error("installatore su Linux: deve rifiutare")
	}
}

func TestVoceInstallata(t *testing.T) {
	altra := VoceSistema{Chiave: "a", Nome: "Altro", Versione: "1"}
	prova := VoceSistema{Chiave: "p", Nome: "Prova Studio", Versione: "1"}
	prima := map[string]VoceSistema{"a": altra}
	// una voce nuova
	if v, ok := voceInstallata(prima, map[string]VoceSistema{"a": altra, "p": prova}, "Prova", "", true); !ok || v.Chiave != "p" {
		t.Errorf("voce nuova: %+v %v", v, ok)
	}
	// due voci nuove (l'installatore porta con sé un componente): vale quella con il nome del programma
	comp := VoceSistema{Chiave: "c", Nome: "Componente di supporto"}
	if v, ok := voceInstallata(prima, map[string]VoceSistema{"a": altra, "p": prova, "c": comp}, "Prova", "", true); !ok || v.Chiave != "p" {
		t.Errorf("due voci nuove: %+v %v", v, ok)
	}
	// niente di nuovo
	if _, ok := voceInstallata(prima, prima, "Prova", "", true); ok {
		t.Error("niente di nuovo: nessuna voce")
	}
	// già installato a mano nella stessa versione: si riconosce dal nome, ma solo se l'installatore è riuscito
	conProva := map[string]VoceSistema{"a": altra, "p": prova}
	if v, ok := voceInstallata(conProva, conProva, "prova-studio", "", true); !ok || v.Chiave != "p" {
		t.Errorf("già installato a mano: %+v %v", v, ok)
	}
	if _, ok := voceInstallata(conProva, conProva, "prova-studio", "", false); ok {
		t.Error("installatore fallito e niente di cambiato: nessuna voce")
	}
	// aggiornamento che cambia la versione, anche con un codice di uscita strano
	nuova := prova
	nuova.Versione = "2"
	if v, ok := voceInstallata(conProva, map[string]VoceSistema{"a": altra, "p": nuova}, "x", "p", false); !ok || v.Versione != "2" {
		t.Errorf("voce aggiornata: %+v %v", v, ok)
	}
	if !codiceRiuscito(0) || !codiceRiuscito(3010) || codiceRiuscito(1602) {
		t.Error("codici di uscita di un installatore")
	}
}

func TestDisinstallaWindows(t *testing.T) {
	p := nuovaProva(t)
	nuovoSistemaFinto(t)
	zip := filepath.Join(p.base, "Prova-Windows.zip")
	creaZip(t, zip, map[string]string{"Prova-Windows/Prova.exe": "exe", "Prova-Windows/LICENZE/LICENSE": "gpl"})
	e, err := Installa(p.richiesta("windows", Utente, "v1", zip))
	if err != nil {
		t.Fatal(err)
	}
	// ciò che il programma ha scritto usandolo: impostazioni, e il profilo della webview
	dati := filepath.Join(p.base, "dati")
	scrivi(t, filepath.Join(dati, "Prova", "impostazioni.json"), "{}")
	scrivi(t, filepath.Join(dati, "Prova.exe", "EBWebView", "x"), "profilo")
	scrivi(t, filepath.Join(dati, "Altro", "impostazioni.json"), "{}")
	scrivi(t, filepath.Join(dati, "Microsoft", "x"), "di sistema")
	trovati := DatiProgramma([]string{dati, filepath.Join(p.base, "non-esiste")}, "Prova", e.Percorso, "windows")
	if len(trovati) != 2 || trovati[0] != filepath.Join(dati, "Prova") || trovati[1] != filepath.Join(dati, "Prova.exe") {
		t.Fatalf("impostazioni trovate: %v", trovati)
	}
	if d := DatiProgramma([]string{dati}, "Prova", e.Percorso, "windows", filepath.Join(dati, "prova")); len(d) != 1 {
		t.Errorf("un percorso escluso non va proposto: %v", d)
	}
	if d := DatiProgramma([]string{dati}, "Microsoft", "", "windows"); len(d) != 0 {
		t.Errorf("una cartella condivisa non è mai le impostazioni di un programma: %v", d)
	}
	// resti di una versione precedente
	scrivi(t, filepath.Join(e.Percorso+".precedente", "Prova.exe"), "vecchio")
	lnk := filepath.Join(p.base, "appdata", "Microsoft", "Windows", "Start Menu", "Programs", "Prova.lnk")

	var cestinati []string
	d := Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "windows",
		Cestina: func(_, percorso string) error { cestinati = append(cestinati, percorso); return os.RemoveAll(percorso) }}
	// senza indicare le impostazioni, restano dove sono
	es, err := Disinstalla(d)
	if err != nil {
		t.Fatal(err)
	}
	if es.Percorso != e.Percorso || esiste(e.Percorso) || esiste(e.Percorso+".precedente") || esiste(fileRegistro(p.reg, "Prova")) {
		t.Errorf("disinstallazione: %+v", es)
	}
	if runtime.GOOS == "windows" && esiste(lnk) {
		t.Error("il collegamento nel menu Start è rimasto")
	}
	if len(cestinati) != 0 || !esiste(filepath.Join(dati, "Prova", "impostazioni.json")) {
		t.Error("le impostazioni si toccano solo se l'utente lo chiede")
	}
	if LeggiStato(p.reg, "Prova", "windows", p.c).Presente {
		t.Error("risulta ancora installato")
	}
	if _, err := Disinstalla(d); err == nil {
		t.Error("disinstallare ciò che non c'è: deve dirlo")
	}
	// con le impostazioni: vanno nel cestino quelle indicate, e solo quelle
	if _, err := Installa(p.richiesta("windows", Utente, "v1", zip)); err != nil {
		t.Fatal(err)
	}
	d.Dati = trovati
	es, err = Disinstalla(d)
	if err != nil || len(es.DatiTolti) != 2 || len(cestinati) != 2 || esiste(filepath.Join(dati, "Prova")) || !esiste(filepath.Join(dati, "Altro", "impostazioni.json")) {
		t.Errorf("con le impostazioni: %+v %v, cestinati %v", es, err, cestinati)
	}
	// se il cestino non funziona il programma è tolto lo stesso, e lo si dice
	if _, err := Installa(p.richiesta("windows", Utente, "v1", zip)); err != nil {
		t.Fatal(err)
	}
	d.Dati = []string{filepath.Join(dati, "Altro")}
	d.Cestina = func(string, string) error { return errors.New("cestino pieno") }
	if es, err = Disinstalla(d); err != nil || len(es.Avvisi) != 1 || len(es.DatiTolti) != 0 || !esiste(filepath.Join(dati, "Altro")) {
		t.Errorf("cestino che non funziona: %+v %v", es, err)
	}

	// per tutto il computer: lo fa lo script, con i permessi di amministratore
	e, err = Installa(p.richiesta("windows", Sistema, "v1", zip))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(e.Percorso, 0o755) // lo script finto non copia: la cartella la si mette qui
	p.script = nil
	q := Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "windows", privilegiato: p.richiesta("windows", Sistema, "", "").privilegiato}
	if _, err := Disinstalla(q); err != nil || len(p.script) != 1 {
		t.Fatalf("sistema: %v, script %d", err, len(p.script))
	}
	for _, atteso := range []string{"$ErrorActionPreference = 'Stop'", "$t = '" + e.Percorso + "'", "($t + '.precedente')", "Remove-Item -LiteralPath $x -Recurse -Force",
		"Prova.lnk'", "Remove-Item -LiteralPath $l -Force"} {
		if !strings.Contains(p.script[0], atteso) {
			t.Errorf("nello script di rimozione manca %q:\n%s", atteso, p.script[0])
		}
	}
	if esiste(fileRegistro(p.reg, "Prova")) {
		t.Error("annotazione rimasta dopo la rimozione per tutto il computer")
	}
	// permessi negati: non si toglie niente, e resta annotato
	if _, err = Installa(p.richiesta("windows", Sistema, "v1", zip)); err != nil {
		t.Fatal(err)
	}
	p.rifiuta = true
	if _, err := Disinstalla(q); err == nil || !esiste(fileRegistro(p.reg, "Prova")) {
		t.Errorf("permessi negati: %v", err)
	}
}

func TestDisinstallaLinux(t *testing.T) {
	p := nuovaProva(t)
	file := filepath.Join(p.base, "Prova-Linux-x86_64.AppImage")
	scrivi(t, file, "appimage")
	e, err := Installa(p.richiesta("linux", Utente, "v1", file))
	if err != nil {
		t.Fatal(err)
	}
	scrivi(t, e.Percorso+".precedente", "vecchia")
	scrivi(t, filepath.Join(p.reg, "Prova.png"), "icona")
	if _, err := Disinstalla(Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "linux"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{e.Percorso, e.Percorso + ".precedente", fileRegistro(p.reg, "Prova"), filepath.Join(p.reg, "Prova.png")} {
		if esiste(f) {
			t.Errorf("rimasto %s", f)
		}
	}
	// per tutto il computer: lo script, con la voce di menu di tutti
	e, err = Installa(p.richiesta("linux", Sistema, "v1", file))
	if err != nil {
		t.Fatal(err)
	}
	scrivi(t, e.Percorso, "installata dallo script")
	p.script = nil
	d := Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "linux", privilegiato: p.richiesta("linux", Sistema, "", "").privilegiato}
	if _, err := Disinstalla(d); err != nil || len(p.script) != 1 {
		t.Fatalf("sistema: %v, script %d", err, len(p.script))
	}
	dir := filepath.Dir(e.Percorso)
	for _, atteso := range []string{"set -e", "rm -f '" + e.Percorso + "' '" + e.Percorso + ".precedente' '" + filepath.Join(dir, "Prova.png") + "'",
		"rmdir '" + dir + "' 2>/dev/null || true", "rm -f '/usr/share/applications/centralhub-prova.desktop'"} {
		if !strings.Contains(p.script[0], atteso) {
			t.Errorf("nello script di rimozione manca %q:\n%s", atteso, p.script[0])
		}
	}
	// un programma messo a mano si toglie, ma una voce di menu non sua non si tocca
	scrivi(t, filepath.Join(p.c.Utente, "Prova.AppImage"), "a mano")
	if es, err := Disinstalla(Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "linux"}); err != nil || esiste(es.Percorso) {
		t.Errorf("programma messo a mano: %+v %v", es, err)
	}
}

func TestDatiProgrammaMac(t *testing.T) {
	base := t.TempDir()
	app := filepath.Join(base, "Applications", "Prova-AppleSilicon.app")
	scrivi(t, filepath.Join(app, "Contents", "Info.plist"), "<key>CFBundleIdentifier</key>\n<string>com.wails.prova.arm64</string>")
	lib := filepath.Join(base, "Library")
	scrivi(t, filepath.Join(lib, "Application Support", "Prova", "profiles.json"), "{}")
	scrivi(t, filepath.Join(lib, "Preferences", "com.wails.prova.arm64.plist"), "x")
	scrivi(t, filepath.Join(lib, "Caches", "com.wails.prova.arm64", "x"), "x")
	scrivi(t, filepath.Join(lib, "Caches", "com.apple.Safari", "x"), "x")
	basi := []string{filepath.Join(lib, "Application Support"), filepath.Join(lib, "Caches"), filepath.Join(lib, "Preferences")}
	if d := DatiProgramma(basi, "Prova", app, "darwin"); len(d) != 3 {
		t.Errorf("dati di un'app di macOS: %v", d)
	}
}

// debFinto sostituisce dpkg: ciò che c'è "installato" e ciò che dice un .deb.
func debFinto(t *testing.T, pacchetto string) map[string]bool {
	inst := map[string]bool{}
	vInfo, vInst := debInfo, debInstallato
	t.Cleanup(func() { debInfo, debInstallato = vInfo, vInst })
	debInfo = func(file string) (string, string, error) { return pacchetto, "1.0", nil }
	debInstallato = func(p string) bool { return inst[p] }
	return inst
}

func TestDebInstallaEDisinstalla(t *testing.T) {
	p := nuovaProva(t)
	inst := debFinto(t, "prova-app")
	file := filepath.Join(p.base, "it's a prova_1.0_amd64.deb")
	scrivi(t, file, "deb")

	// un'installazione che apt non porta a termine non si annota
	r := p.richiesta("linux", "", "v1", file)
	r.Tipo = TipoDeb
	if _, err := Installa(r); err == nil || !strings.Contains(err.Error(), "non risulta installato") {
		t.Fatalf("apt non ha installato niente: %v", err)
	}
	if esiste(fileRegistro(p.reg, "Prova")) {
		t.Error("annotata un'installazione non riuscita")
	}
	p.rifiuta = true
	if _, err := Installa(r); err == nil {
		t.Fatal("password rifiutata: deve fallire")
	}
	p.rifiuta, p.script = false, nil

	// installazione: l'ambito non serve, lo script è apt con il percorso protetto
	r.privilegiato = func(goos, script string) error {
		p.script = append(p.script, script)
		inst["prova-app"] = true
		return nil
	}
	if _, err := Installa(r); err != nil {
		t.Fatal(err)
	}
	if len(p.script) != 1 || !strings.Contains(p.script[0], "apt-get install -y --allow-downgrades -- '"+strings.ReplaceAll(file, "'", `'\''`)+"'") {
		t.Fatalf("script di installazione:\n%v", p.script)
	}
	s := LeggiStato(p.reg, "Prova", "linux", p.c)
	if !s.Presente || !s.Nostro || s.Tag != "v1" || s.Tipo != TipoDeb || s.Asset != filepath.Base(file) {
		t.Fatalf("stato dopo l'installazione: %+v", s)
	}
	// tolto da fuori (apt remove a mano): per CentralHub non c'è più
	inst["prova-app"] = false
	if s := LeggiStato(p.reg, "Prova", "linux", p.c); s.Presente {
		t.Errorf("pacchetto tolto da fuori ma ancora presente: %+v", s)
	}
	inst["prova-app"] = true

	// disinstallazione: apt remove, poi l'annotazione sparisce
	p.script = nil
	d := Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "linux", privilegiato: func(goos, script string) error {
		p.script = append(p.script, script)
		inst["prova-app"] = false
		return nil
	}}
	if _, err := Disinstalla(d); err != nil {
		t.Fatal(err)
	}
	if len(p.script) != 1 || !strings.Contains(p.script[0], "apt-get remove -y -- 'prova-app'") {
		t.Errorf("script di rimozione:\n%v", p.script)
	}
	if esiste(fileRegistro(p.reg, "Prova")) {
		t.Error("annotazione rimasta")
	}
}

func TestNomeDebValido(t *testing.T) {
	for n, ok := range map[string]bool{"prova-app": true, "libfoo2.1+dfsg": true, "": false, "a": false, "-x": false, "Foo": false, "a b": false, "a;rm": false} {
		if reNomeDeb.MatchString(n) != ok {
			t.Errorf("%q: atteso %v", n, ok)
		}
	}
}
