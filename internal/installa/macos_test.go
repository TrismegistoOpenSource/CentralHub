// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCandidatiMac(t *testing.T) {
	tipi := func(nomi ...string) map[string]string {
		c, err := Candidati(nomi, "darwin", "arm64", "")
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, p := range c {
			m[p.Nome] = p.Tipo
			if p.Consigliato {
				m[p.Nome] += "*"
			}
		}
		return m
	}
	// solo .dmg (è il caso dei repository Storytold)
	if got := tipi("ArtCraft_0.1.0_aarch64.dmg", "ArtCraft_0.1.0_x64.dmg", "ArtCraft-setup.exe", "ArtCraft_amd64.AppImage"); len(got) != 1 || got["ArtCraft_0.1.0_aarch64.dmg"] != "dmg*" {
		t.Errorf("solo dmg: %v", got)
	}
	// zip, dmg e pkg: il consigliato è lo zip che CentralHub gestisce da sé
	got := tipi("App-AppleSilicon.zip", "App-arm64.dmg", "App-arm64.pkg")
	if got["App-AppleSilicon.zip"] != "app*" || got["App-arm64.dmg"] != "dmg" || got["App-arm64.pkg"] != "pkg" {
		t.Errorf("zip, dmg e pkg: %v", got)
	}
	// senza lo zip il consigliato è il dmg, non il pkg
	got = tipi("App-arm64.dmg", "App-arm64.pkg")
	if got["App-arm64.dmg"] != "dmg*" || got["App-arm64.pkg"] != "pkg" {
		t.Errorf("dmg e pkg: %v", got)
	}
	// un .pkg solo è consigliato perché è l'unico; i file di altri sistemi non contano
	if got = tipi("App-win64.msi", "App-linux.deb", "App.pkg"); got["App.pkg"] != "pkg*" || len(got) != 1 {
		t.Errorf("solo pkg: %v", got)
	}
	// file di un altro sistema che finiscono in .dmg/.pkg
	if got = tipi("App-windows.pkg", "App-linux.dmg"); len(got) != 0 {
		t.Errorf("altri sistemi: %v", got)
	}
	// con un modello decide l'utente, per estensione
	for nome, tipo := range map[string]string{"x.dmg": TipoDmg, "x.pkg": TipoPkg, "x.bin": TipoApp} {
		c, err := Candidati([]string{nome}, "darwin", "arm64", "x.*")
		if err != nil || len(c) != 1 || c[0].Tipo != tipo {
			t.Errorf("modello su %s: %v %v", nome, c, err)
		}
	}
	// l'app di un .dmg si aggiorna anche con lo zip, e viceversa
	c, _ := Candidati([]string{"App-AppleSilicon.zip"}, "darwin", "arm64", "")
	if p, err := Scegli(c, Pacchetto{}, Pacchetto{Nome: "App-arm64.dmg", Tipo: TipoDmg}); err != nil || p.Nome != "App-AppleSilicon.zip" {
		t.Errorf("da dmg a zip: %v %v", p, err)
	}
	c, _ = Candidati([]string{"App-arm64.pkg"}, "darwin", "arm64", "")
	if _, err := Scegli(c, Pacchetto{}, Pacchetto{Nome: "App-arm64.dmg", Tipo: TipoDmg}); err == nil {
		t.Error("da dmg a pkg: va rifiutato, serve prima disinstallare")
	}
}

// scriviApp crea una finta app di macOS.
func scriviApp(t *testing.T, cartella, nome, versione string) string {
	app := filepath.Join(cartella, nome+".app")
	exe := filepath.Join(app, "Contents", "MacOS", "Prova")
	scrivi(t, exe, versione)
	os.Chmod(exe, 0o755)
	scrivi(t, filepath.Join(app, "Contents", "Info.plist"),
		`<plist><dict><key>CFBundleIdentifier</key><string>it.prova.centralhub.dmg</string><key>CFBundleShortVersionString</key><string>`+versione+`</string></dict></plist>`)
	os.Remove(filepath.Join(app, "Contents", "collegamento"))
	if err := os.Symlink("MacOS/Prova", filepath.Join(app, "Contents", "collegamento")); err != nil {
		t.Fatal(err)
	}
	return app
}

func creaDmg(t *testing.T, sorgente, dmg, volume string) {
	os.MkdirAll(filepath.Dir(dmg), 0o755)
	if out, err := exec.Command("/usr/bin/hdiutil", "create", "-quiet", "-srcfolder", sorgente, "-volname", volume, "-format", "UDZO", "-ov", dmg).CombinedOutput(); err != nil {
		t.Fatalf("hdiutil create: %v %s", err, out)
	}
}

// Il giro vero su macOS con un'immagine disco: monta, copia con ditto,
// aggiorna senza lasciare la versione vecchia, disinstalla. Il nome ha
// accento e spazio, la cartella del programma un apostrofo.
func TestMacDmgInstallaAggiornaDisinstalla(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	p := nuovaProva(t)
	p.c.Utente = filepath.Join(p.base, "Applicazioni d'Utènte")
	fai := func(tag, versione string) string {
		src := filepath.Join(p.base, "src-"+tag)
		scriviApp(t, src, "Prova Accènto", versione)
		os.Symlink("/Applications", filepath.Join(src, "Applications")) // come nei .dmg veri
		dmg := filepath.Join(p.base, tag, "Prova-arm64.dmg")
		creaDmg(t, src, dmg, "Prova "+versione)
		return dmg
	}
	r := p.richiesta("darwin", Utente, "v1.0.0", fai("v1.0.0", "1.0.0"))
	r.Tipo = TipoDmg
	e, err := Installa(r)
	if err != nil {
		t.Fatal(err)
	}
	bersaglio := filepath.Join(p.c.Utente, "Prova Accènto.app")
	exe := filepath.Join(bersaglio, "Contents", "MacOS", "Prova")
	if e.Percorso != bersaglio || leggi(t, exe) != "1.0.0" {
		t.Fatalf("installata: %+v", e)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o111 == 0 {
		t.Error("ditto deve conservare il permesso di esecuzione")
	}
	if l, err := os.Readlink(filepath.Join(bersaglio, "Contents", "collegamento")); err != nil || l != "MacOS/Prova" {
		t.Errorf("ditto deve conservare i collegamenti: %q %v", l, err)
	}
	if esiste(filepath.Join(p.c.Utente, "Applications")) {
		t.Error("il collegamento ad Applicazioni dell'immagine non va copiato")
	}
	if s := LeggiStato(p.reg, "Prova", "darwin", p.c); !s.Nostro || s.Tipo != TipoDmg || s.Tag != "v1.0.0" || s.Versione != "1.0.0" {
		t.Errorf("stato: %+v", s)
	}

	// aggiornamento: niente .precedente, niente appoggi rimasti
	r = p.richiesta("darwin", Utente, "v2.0.0", fai("v2.0.0", "2.0.0"))
	r.Tipo = TipoDmg
	if _, err := Installa(r); err != nil {
		t.Fatal(err)
	}
	if leggi(t, exe) != "2.0.0" || esiste(bersaglio+".precedente") {
		t.Error("aggiornamento da .dmg non riuscito")
	}
	voci, _ := os.ReadDir(p.c.Utente)
	if len(voci) != 1 {
		t.Errorf("in %s doveva restare solo l'app: %v", p.c.Utente, voci)
	}
	// nessun volume lasciato montato
	if out, _ := exec.Command("/sbin/mount").Output(); strings.Contains(string(out), "centralhub-dmg-") {
		t.Error("immagine disco rimasta montata")
	}

	// disinstallazione, con le impostazioni nel cestino (finto)
	dati := filepath.Join(p.base, "dati", "Prova")
	scrivi(t, filepath.Join(dati, "impostazioni.json"), "{}")
	var cestinati []string
	es, err := Disinstalla(Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, Dati: []string{dati}, GOOS: "darwin",
		Cestina: func(_, percorso string) error { cestinati = append(cestinati, percorso); return os.RemoveAll(percorso) }})
	if err != nil || es.Percorso != bersaglio || esiste(bersaglio) || len(cestinati) != 1 {
		t.Fatalf("disinstallazione: %+v %v %v", es, err, cestinati)
	}
	if s := LeggiStato(p.reg, "Prova", "darwin", p.c); s.Presente {
		t.Errorf("dopo la disinstallazione: %+v", s)
	}
}

func TestMacDmgCasiStorti(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	p := nuovaProva(t)
	prova := func(nome string, riempi func(src string), atteso string) {
		t.Helper()
		src := filepath.Join(p.base, "src-"+nome)
		os.MkdirAll(src, 0o755)
		riempi(src)
		dmg := filepath.Join(p.base, nome+".dmg")
		creaDmg(t, src, dmg, nome)
		r := p.richiesta("darwin", Utente, "v1", dmg)
		r.Tipo = TipoDmg
		_, err := Installa(r)
		if atteso == "" {
			if err != nil {
				t.Errorf("%s: %v", nome, err)
			}
			return
		}
		if err == nil || !strings.Contains(err.Error(), atteso) {
			t.Errorf("%s: errore %v, atteso %q", nome, err, atteso)
		}
	}
	prova("vuoto", func(src string) { scrivi(t, filepath.Join(src, "LEGGIMI.txt"), "niente") }, "non contiene nessuna app")
	prova("conpkg", func(src string) { scrivi(t, filepath.Join(src, "Installa.pkg"), "x") }, "installatore (.pkg)")
	prova("duapp", func(src string) {
		scriviApp(t, src, "Altro", "1")
		scriviApp(t, src, "Prova", "1")
	}, "") // sceglie quella che porta il nome del programma
	if !esiste(filepath.Join(p.c.Utente, "Prova.app")) {
		t.Error("fra due app doveva scegliere quella col nome del programma")
	}
	prova("tre", func(src string) {
		scriviApp(t, src, "Uno", "1")
		scriviApp(t, src, "Due", "1")
	}, "più app")
	// un file che non è un'immagine
	finto := filepath.Join(p.base, "finto.dmg")
	scrivi(t, finto, "non sono un'immagine disco")
	r := p.richiesta("darwin", Utente, "v1", finto)
	r.Tipo = TipoDmg
	if _, err := Installa(r); err == nil || !strings.Contains(err.Error(), "non si apre") {
		t.Errorf("immagine finta: %v", err)
	}
}

// ---------------------------------------------------------------- .pkg

// costruisciPkg fa un .pkg vero con pkgbuild (e, se prodotto, con productbuild).
// pkgbuild non regge accenti nei nomi dei file (mkbom): il nome qui è ASCII.
func costruisciPkg(t *testing.T, cartella, id string, prodotto bool) string {
	root := filepath.Join(cartella, "root-"+id)
	scriviApp(t, filepath.Join(root, "Applications"), "Prova Pkg", "1.0")
	plist := filepath.Join(cartella, id+".plist")
	if out, err := exec.Command("/usr/bin/pkgbuild", "--analyze", "--root", root, plist).CombinedOutput(); err != nil {
		t.Skipf("pkgbuild non disponibile: %v %s", err, out)
	}
	comp := filepath.Join(cartella, id+".pkg")
	if out, err := exec.Command("/usr/bin/pkgbuild", "--root", root, "--component-plist", plist, "--identifier", id, "--version", "1.0",
		"--install-location", "/", comp).CombinedOutput(); err != nil {
		t.Fatalf("pkgbuild: %v %s", err, out)
	}
	if !prodotto {
		return comp
	}
	prod := filepath.Join(cartella, id+"-prodotto.pkg")
	if out, err := exec.Command("/usr/bin/productbuild", "--package", comp, prod).CombinedOutput(); err != nil {
		t.Fatalf("productbuild: %v %s", err, out)
	}
	return prod
}

func TestLeggiPkgVero(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	d := t.TempDir()
	for _, prodotto := range []bool{false, true} {
		f := costruisciPkg(t, d, "it.prova.centralhub.uno", prodotto)
		ids, err := leggiPkg(f)
		if err != nil || len(ids) != 1 || ids[0] != "it.prova.centralhub.uno" {
			t.Errorf("prodotto=%v: %v %v", prodotto, ids, err)
		}
	}
	scrivi(t, filepath.Join(d, "finto.pkg"), "non sono un pacchetto")
	if _, err := leggiPkg(filepath.Join(d, "finto.pkg")); err == nil {
		t.Error("un file qualunque non è un .pkg")
	}
}

// finteRicevute sostituisce il sistema: le ricevute di pkgutil e l'Installer.
type finteRicevute struct {
	ricevute  map[string]string // id -> "versione|quando"
	contenuto map[string]PkgContenuto
	avvii     int
	installa  func(r map[string]string) // cosa fa l'Installer (nil = l'utente annulla)
}

func (f *finteRicevute) usa(t *testing.T, ids []string) {
	oInfo, oStato, oCont, oAvvia := pkgInfo, pkgStato, pkgContenuto, avviaPkg
	t.Cleanup(func() { pkgInfo, pkgStato, pkgContenuto, avviaPkg = oInfo, oStato, oCont, oAvvia })
	pkgInfo = func(string) ([]string, error) { return ids, nil }
	pkgStato = func(id string) (string, string, bool) {
		v, ok := f.ricevute[id]
		a, b, _ := strings.Cut(v, "|")
		return a, b, ok
	}
	pkgContenuto = func(id string) (PkgContenuto, error) {
		c, ok := f.contenuto[id]
		if !ok {
			return PkgContenuto{}, errors.New("nessuna ricevuta")
		}
		return c, nil
	}
	avviaPkg = func(ctx context.Context, file string) error {
		f.avvii++
		if f.installa != nil {
			f.installa(f.ricevute)
		}
		return nil
	}
}

func TestPkgInstallaAnnullaDisinstalla(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("percorsi e script di macOS")
	}
	p := nuovaProva(t)
	file := filepath.Join(p.base, "Prova.pkg")
	scrivi(t, file, "pkg")
	f := &finteRicevute{ricevute: map[string]string{}, contenuto: map[string]PkgContenuto{
		"it.prova.uno": {ID: "it.prova.uno", Volume: "/", Radice: "/", Cartelle: []string{"Applications", "Applications/Prova.app", "Applications/Prova.app/Contents"}, File: []string{"Applications/Prova.app/Contents/Info.plist"}},
		"it.prova.due": {ID: "it.prova.due", Volume: "/", Radice: "/", Cartelle: []string{"usr", "usr/local", "usr/local/prova"}, File: []string{"usr/local/prova/aiuto"}},
	}}
	f.usa(t, []string{"it.prova.uno", "it.prova.due"})
	r := p.richiesta("darwin", "", "v1.0.0", file)
	r.Tipo = TipoPkg

	// l'utente chiude l'Installer senza installare
	if _, err := Installa(r); err == nil || !strings.Contains(err.Error(), "non risulta installato") || f.avvii != 1 {
		t.Fatalf("annullata: %v (avvii %d)", err, f.avvii)
	}
	if _, ok := leggiRegistro(p.reg, "Prova"); ok {
		t.Error("annullata: non si annota niente")
	}

	// installa: ricevute nuove
	f.installa = func(r map[string]string) { r["it.prova.uno"], r["it.prova.due"] = "1.0|100", "1.0|100" }
	e, err := Installa(r)
	if err != nil {
		t.Fatal(err)
	}
	if e.Percorso != "/Applications/Prova.app" {
		t.Errorf("percorso dell'app: %q", e.Percorso)
	}
	s := LeggiStato(p.reg, "Prova", "darwin", p.c)
	if !s.Presente || !s.Nostro || s.Tipo != TipoPkg || s.Tag != "v1.0.0" || s.Ambito != "" {
		t.Errorf("stato: %+v", s)
	}
	if reg, _ := leggiRegistro(p.reg, "Prova"); reg.Chiave != "it.prova.uno,it.prova.due" {
		t.Errorf("registro: %+v", reg)
	}

	// aggiornamento annullato: le ricevute vecchie ci sono ancora ma non sono cambiate
	f.installa = nil
	r.Tag = "v2.0.0"
	if _, err := Installa(r); err == nil {
		t.Fatal("aggiornamento annullato: doveva dare errore")
	}
	if s := LeggiStato(p.reg, "Prova", "darwin", p.c); s.Tag != "v1.0.0" {
		t.Errorf("annullato, la versione annotata è cambiata: %+v", s)
	}
	// aggiornamento riuscito: ricevute rifatte (stessi id, altra data)
	f.installa = func(r map[string]string) { r["it.prova.uno"], r["it.prova.due"] = "2.0|200", "2.0|200" }
	if _, err := Installa(r); err != nil {
		t.Fatal(err)
	}
	if s := LeggiStato(p.reg, "Prova", "darwin", p.c); s.Tag != "v2.0.0" {
		t.Errorf("aggiornato: %+v", s)
	}

	// disinstallazione: lo script con i permessi, poi l'annotazione sparisce
	var script string
	d := Rimozione{Nome: "Prova", Cartelle: p.c, CartellaRegistro: p.reg, GOOS: "darwin",
		privilegiato: func(goos, sc string) error {
			if goos != "darwin" {
				t.Errorf("sistema dello script: %s", goos)
			}
			script = sc
			delete(f.ricevute, "it.prova.uno")
			delete(f.ricevute, "it.prova.due")
			return nil
		}}
	if _, err := Disinstalla(d); err != nil {
		t.Fatal(err)
	}
	for _, atteso := range []string{
		"rm -f -- '/Applications/Prova.app/Contents/Info.plist'", "rm -f -- '/usr/local/prova/aiuto'",
		"rmdir -- '/Applications/Prova.app/Contents'", "rmdir -- '/Applications/Prova.app'", "rmdir -- '/usr/local/prova'",
		"pkgutil --forget 'it.prova.uno'", "pkgutil --forget 'it.prova.due'",
	} {
		if !strings.Contains(script, atteso) {
			t.Errorf("lo script non contiene %q:\n%s", atteso, script)
		}
	}
	// /Applications e /usr/local non si toccano, nemmeno vuote
	for _, vietato := range []string{"rmdir -- '/Applications'", "rmdir -- '/usr/local'", "rmdir -- '/usr'"} {
		if strings.Contains(script, vietato+" ") {
			t.Errorf("lo script non deve contenere %q", vietato)
		}
	}
	if _, ok := leggiRegistro(p.reg, "Prova"); ok {
		t.Error("dopo la disinstallazione l'annotazione deve sparire")
	}

	// disinstallazione rifiutata (password negata): resta tutto com'era
	f.ricevute["it.prova.uno"], f.ricevute["it.prova.due"] = "2.0|200", "2.0|200"
	scriviRegistro(r, Registro{Nome: "Prova", Tipo: TipoPkg, Chiave: "it.prova.uno,it.prova.due", Tag: "v2.0.0"}, &Esito{})
	d.privilegiato = func(string, string) error { return errors.New("password negata") }
	if _, err := Disinstalla(d); err == nil {
		t.Fatal("password negata: doveva dare errore")
	}
	if _, ok := leggiRegistro(p.reg, "Prova"); !ok {
		t.Error("disinstallazione fallita: l'annotazione deve restare")
	}
}

func TestScriptPkgRimuoviProteggeIlSistema(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("percorsi e script di macOS")
	}
	buono := PkgContenuto{ID: "it.x", Volume: "/", Radice: "/", File: []string{"Applications/X.app/a"}}
	for nome, c := range map[string]PkgContenuto{
		"file di sistema":    {ID: "it.x", Volume: "/", Radice: "/", File: []string{"usr/bin/ls"}},
		"System":             {ID: "it.x", Volume: "/", Radice: "/", File: []string{"System/Library/x"}},
		"fuori dalla radice": {ID: "it.x", Volume: "/", Radice: "/Applications", File: []string{"../usr/bin/ls"}},
		"altro volume":       {ID: "it.x", Volume: "/Volumes/Esterno", Radice: "/Volumes/Esterno", File: []string{"Applications/X.app/a"}},
	} {
		if _, err := ScriptPkgRimuovi([]PkgContenuto{buono, c}); err == nil {
			t.Errorf("%s: doveva rifiutare", nome)
		}
	}
	// percorsi con apostrofi, spazi e accenti
	c := PkgContenuto{ID: "it.x", Volume: "/", Radice: "/", File: []string{"Applications/L'App Accènto.app/a b"}, Cartelle: []string{"Applications/L'App Accènto.app"}}
	s, err := ScriptPkgRimuovi([]PkgContenuto{c})
	if err != nil || !strings.Contains(s, `rm -f -- '/Applications/L'\''App Accènto.app/a b'`) {
		t.Errorf("percorsi speciali: %v\n%s", err, s)
	}
	if out, err := exec.Command("/bin/sh", "-n", "-c", s).CombinedOutput(); err != nil {
		t.Errorf("lo script non è valido per sh: %v %s", err, out)
	}
}

// Lo script, eseguito davvero su un albero di prova (e un pkgutil finto):
// toglie i file e le cartelle vuote, lascia quelle con dentro altro.
func TestScriptPkgRimuoviEseguito(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("percorsi e script di macOS")
	}
	b := t.TempDir()
	old := pkgPrefissiAmmessi
	pkgPrefissiAmmessi = []string{b}
	defer func() { pkgPrefissiAmmessi = old }()
	for _, f := range []string{"App d'Prova.app/Contents/uno", "App d'Prova.app/Contents/Res/due", "Altro/tre", "Comune/mio", "Comune/di-un-altro"} {
		scrivi(t, filepath.Join(b, "Applications", f), "x")
	}
	c := PkgContenuto{ID: "it.x", Volume: "/", Radice: filepath.Join(b, "Applications"),
		File:     []string{"App d'Prova.app/Contents/uno", "App d'Prova.app/Contents/Res/due", "Comune/mio"},
		Cartelle: []string{"App d'Prova.app", "App d'Prova.app/Contents", "App d'Prova.app/Contents/Res", "Comune"}}
	s, err := ScriptPkgRimuovi([]PkgContenuto{c})
	if err != nil {
		t.Fatal(err)
	}
	s = strings.ReplaceAll(s, "/usr/sbin/pkgutil", "true") // niente ricevute vere da dimenticare
	if out, err := exec.Command("/bin/sh", "-c", s).CombinedOutput(); err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	if esiste(filepath.Join(b, "Applications", "App d'Prova.app")) || esiste(filepath.Join(b, "Applications", "Comune", "mio")) {
		t.Error("i file del pacchetto dovevano sparire, con le cartelle vuote")
	}
	if !esiste(filepath.Join(b, "Applications", "Comune", "di-un-altro")) || !esiste(filepath.Join(b, "Applications", "Altro", "tre")) {
		t.Error("i file di altri non si toccano")
	}
}

// CentralHub che aggiorna sé stesso su macOS: un processo vero, lanciato
// dall'interno di un'app finta (Contents/MacOS), sostituisce la propria app
// con una nuova; al riavvio la versione vecchia viene tolta. Il "processo" è
// questo stesso binario di test, che si rilancia con CH_AIUTO.
func TestMacSeStessoAggiornaESiPulisce(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	base := os.Getenv("CH_BASE")
	switch os.Getenv("CH_AIUTO") {
	case "aggiorna":
		r := Richiesta{ID: "id1", Nome: "Prova", Repo: "a/Prova", Tag: "v2.0.0", File: os.Getenv("CH_ZIP"), Ambito: Utente,
			Cartelle:         Cartelle{Utente: filepath.Join(base, "utente"), Sistema: filepath.Join(base, "sistema")},
			CartellaRegistro: filepath.Join(base, "registro"), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
		e, err := Installa(r)
		if err != nil || !e.SeStesso {
			fmt.Printf("AIUTO-KO: %+v %v\n", e, err)
			os.Exit(3)
		}
		fmt.Println("AIUTO-OK")
		os.Exit(0)
	case "pulisci":
		PulisciDopoAggiornamento()
		fmt.Println("AIUTO-OK")
		os.Exit(0)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base = t.TempDir()
	// l'app "installata": contiene una copia di questo binario
	faiApp := func(cartella, versione string) string {
		app := filepath.Join(cartella, "Prova.app")
		dest := filepath.Join(app, "Contents", "MacOS", "prova.test")
		os.MkdirAll(filepath.Dir(dest), 0o755)
		if err := copia(exe, dest, 0o755); err != nil {
			t.Fatal(err)
		}
		scrivi(t, filepath.Join(app, "Contents", "versione"), versione)
		return app
	}
	installata := faiApp(filepath.Join(base, "utente"), "1")
	nuova := faiApp(filepath.Join(base, "src"), "2")
	zip := filepath.Join(base, "Prova-AppleSilicon.zip")
	if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", nuova, zip).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v %s", err, out)
	}
	lancia := func(aiuto string) string {
		cmd := exec.Command(filepath.Join(installata, "Contents", "MacOS", "prova.test"), "-test.run=^TestMacSeStessoAggiornaESiPulisce$")
		cmd.Env = append(os.Environ(), "CH_AIUTO="+aiuto, "CH_BASE="+base, "CH_ZIP="+zip)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "AIUTO-OK") {
			t.Fatalf("%s: %v\n%s", aiuto, err, out)
		}
		return string(out)
	}
	lancia("aggiorna")
	if leggi(t, filepath.Join(installata, "Contents", "versione")) != "2" {
		t.Error("l'app non è stata sostituita")
	}
	if leggi(t, filepath.Join(installata+".precedente", "Contents", "versione")) != "1" {
		t.Error("mentre gira, la vecchia deve restare accanto come .precedente")
	}
	lancia("pulisci") // il riavvio, dalla nuova app
	if esiste(installata + ".precedente") {
		t.Error("al riavvio la versione vecchia deve sparire")
	}
	if !esiste(installata) {
		t.Error("la nuova app non c'è più")
	}
}
