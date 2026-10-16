// SPDX-License-Identifier: MIT

package installa

import (
	"archive/zip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// i nomi veri delle release dei progetti
var nomiRelease = []string{
	"RsyncGUI-AppleSilicon.zip", "RsyncGUI-Intel.zip", "RsyncGUI-Linux-x86_64.AppImage",
	"RsyncGUI-Windows.zip", "RsyncGUI-v2.5.1-sorgente.zip", "RsyncGUI-v2.5.1-sorgente.tar.gz",
}

func TestScegliAsset(t *testing.T) {
	casi := []struct{ goos, goarch, atteso string }{
		{"linux", "amd64", "RsyncGUI-Linux-x86_64.AppImage"},
		{"windows", "amd64", "RsyncGUI-Windows.zip"},
		{"darwin", "arm64", "RsyncGUI-AppleSilicon.zip"},
		{"darwin", "amd64", "RsyncGUI-Intel.zip"},
	}
	for _, k := range casi {
		got, err := ScegliAsset(nomiRelease, k.goos, k.goarch, "")
		if err != nil || got != k.atteso {
			t.Errorf("%s/%s: %q, %v; atteso %q", k.goos, k.goarch, got, err, k.atteso)
		}
	}
	// AppImage solo x86_64 su un Linux ARM: non va installata
	if got, err := ScegliAsset(nomiRelease, "linux", "arm64", ""); !errors.Is(err, ErrNessunAsset) {
		t.Errorf("linux/arm64: %q, %v", got, err)
	}
	// niente di installabile (Adocentyn)
	if _, err := ScegliAsset([]string{"adocentyn-0.1.0.tar.gz", "Adocentyn-v0.1.0-sorgente.zip"}, "linux", "amd64", ""); !errors.Is(err, ErrNessunAsset) {
		t.Errorf("solo tar.gz: %v", err)
	}
	// "darwin" contiene "win": non deve passare per Windows
	if got, err := ScegliAsset([]string{"app-darwin-x86_64.zip"}, "windows", "amd64", ""); !errors.Is(err, ErrNessunAsset) {
		t.Errorf("darwin scambiato per Windows: %q, %v", got, err)
	}
	if got, _ := ScegliAsset([]string{"app-darwin-x86_64.zip", "app-win64.zip", "app-linux-amd64.AppImage"}, "windows", "amd64", ""); got != "app-win64.zip" {
		t.Errorf("win64: %q", got)
	}
	if got, _ := ScegliAsset([]string{"app-darwin-x86_64.zip", "app-darwin-arm64.zip", "app-win64.zip"}, "darwin", "arm64", ""); got != "app-darwin-arm64.zip" {
		t.Errorf("darwin arm64: %q", got)
	}
	// i nomi veri di una release con molti pacchetti (storytold/photocraft)
	tanti := []string{
		"photocraft-0.8.2-freebsd-x86_64.tar.gz", "photocraft-0.8.2-linux-aarch64.AppImage", "photocraft-0.8.2-linux-aarch64.AppImage.zsync",
		"photocraft-0.8.2-linux-aarch64.deb", "photocraft-0.8.2-linux-x86_64.AppImage", "photocraft-0.8.2-linux-x86_64.AppImage.zsync",
		"photocraft-0.8.2-linux-x86_64.deb", "photocraft-0.8.2-linux-x86_64.tar.gz", "photocraft-0.8.2-macos-universal.dmg",
		"photocraft-0.8.2-windows-arm64-portable.zip", "photocraft-0.8.2-windows-arm64.msi", "photocraft-0.8.2-windows-x64-portable.zip",
		"photocraft-0.8.2-windows-x64.msi", "photocraft-0.8.2-windows-x86-portable.zip", "photocraft-cli-0.8.2-macos-universal.zip",
		"photocraft-web-0.8.2.zip", "SHA256SUMS.txt",
	}
	// su Linux la stessa release ha l'AppImage e il .deb: due modi di
	// installare, il consigliato è l'AppImage (lo gestisce CentralHub da sé)
	for _, k := range []struct{ goarch, appimage, deb string }{
		{"amd64", "photocraft-0.8.2-linux-x86_64.AppImage", "photocraft-0.8.2-linux-x86_64.deb"},
		{"arm64", "photocraft-0.8.2-linux-aarch64.AppImage", "photocraft-0.8.2-linux-aarch64.deb"},
	} {
		c, err := Candidati(tanti, "linux", k.goarch, "")
		attesi := []Pacchetto{{Nome: k.appimage, Tipo: TipoAppImage, Consigliato: true}, {Nome: k.deb, Tipo: TipoDeb}}
		if err != nil || len(c) != 2 || c[0] != attesi[0] || c[1] != attesi[1] {
			t.Errorf("photocraft linux/%s: %+v, %v; atteso %+v", k.goarch, c, err, attesi)
		}
		var scelta *ErrScelta
		if _, err := ScegliAsset(tanti, "linux", k.goarch, ""); !errors.As(err, &scelta) {
			t.Errorf("photocraft linux/%s: ci si aspetta di dover scegliere, %v", k.goarch, err)
		}
		// chi ha già un .deb continua con il .deb, chi ha l'AppImage con l'AppImage
		if p, err := Scegli(c, Pacchetto{}, Pacchetto{Tipo: TipoDeb}); err != nil || p.Nome != k.deb {
			t.Errorf("aggiornamento di un .deb: %+v, %v", p, err)
		}
	}
	// solo un .deb: è l'unico modo, non c'è niente da scegliere
	if got, err := ScegliAsset([]string{"app_1.0_amd64.deb", "app_1.0_arm64.deb", "app-1.0.tar.gz"}, "linux", "amd64", ""); err != nil || got != "app_1.0_amd64.deb" {
		t.Errorf("solo .deb: %q, %v", got, err)
	}
	// su Windows la stessa release ha lo zip portable e l'installatore .msi:
	// sono due modi di installare, e quale lo sceglie l'utente
	for _, arch := range []string{"x64", "arm64"} {
		goarch := map[string]string{"x64": "amd64", "arm64": "arm64"}[arch]
		c, err := Candidati(tanti, "windows", goarch, "")
		attesi := []Pacchetto{
			{Nome: "photocraft-0.8.2-windows-" + arch + "-portable.zip", Tipo: TipoPortable, Consigliato: true},
			{Nome: "photocraft-0.8.2-windows-" + arch + ".msi", Tipo: TipoInstallatore},
		}
		if err != nil || len(c) != 2 || c[0] != attesi[0] || c[1] != attesi[1] {
			t.Errorf("photocraft windows/%s: %+v, %v", goarch, c, err)
		}
		var scelta *ErrScelta
		if _, err := ScegliAsset(tanti, "windows", goarch, ""); !errors.As(err, &scelta) {
			t.Errorf("photocraft windows/%s: con due pacchetti si chiede, invece: %v", goarch, err)
		}
	}
	// per macOS c'è solo un .dmg, e ora si sa installare: lo strumento da
	// riga di comando (uno zip per macOS) non va scambiato per il programma
	if got, err := ScegliAsset(tanti, "darwin", "arm64", ""); err != nil || got != "photocraft-0.8.2-macos-universal.dmg" {
		t.Errorf("photocraft su macOS: %q, %v", got, err)
	}
	// due candidati: si chiede di scegliere
	if _, err := ScegliAsset([]string{"a-x86_64.AppImage", "b-x86_64.AppImage"}, "linux", "amd64", ""); err == nil || errors.Is(err, ErrNessunAsset) {
		t.Errorf("due AppImage: %v", err)
	}
	// il modello dell'utente vince, senza distinguere maiuscole
	if got, err := ScegliAsset(nomiRelease, "linux", "amd64", "*-intel.ZIP"); err != nil || got != "RsyncGUI-Intel.zip" {
		t.Errorf("modello: %q, %v", got, err)
	}
	if _, err := ScegliAsset(nomiRelease, "linux", "amd64", "*.deb"); err == nil {
		t.Error("modello senza corrispondenze: deve dirlo")
	}
}

func TestNomeProgramma(t *testing.T) {
	for in, atteso := range map[string]string{"RsyncGUI": "RsyncGUI", " Aurora Studio (2) ": "Aurora-Studio-2", "": "Programma", "../x": "x"} {
		if got := NomeProgramma(in); got != atteso {
			t.Errorf("NomeProgramma(%q) = %q, atteso %q", in, got, atteso)
		}
	}
}

func scrivi(t *testing.T, percorso, contenuto string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(percorso), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(percorso, []byte(contenuto), 0o644); err != nil {
		t.Fatal(err)
	}
}

func leggi(t *testing.T, percorso string) string {
	t.Helper()
	b, err := os.ReadFile(percorso)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func creaZip(t *testing.T, percorso string, file map[string]string) {
	t.Helper()
	f, err := os.Create(percorso)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for nome, contenuto := range file {
		w, err := z.Create(nome)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(contenuto))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// ambiente di prova: cartelle di installazione e registro in una cartella
// temporanea; lo script privilegiato non parte mai davvero.
type prova struct {
	base    string
	c       Cartelle
	reg     string
	script  []string
	rifiuta bool
}

func nuovaProva(t *testing.T) *prova {
	b := t.TempDir()
	// voce di menu e collegamento nel menu Start: mai in quelli veri dell'utente
	t.Setenv("XDG_DATA_HOME", filepath.Join(b, "xdg"))
	t.Setenv("APPDATA", filepath.Join(b, "appdata"))
	return &prova{base: b, c: Cartelle{Utente: filepath.Join(b, "utente"), Sistema: filepath.Join(b, "sistema")}, reg: filepath.Join(b, "registro")}
}

func (p *prova) richiesta(goos, ambito, tag, file string) Richiesta {
	return Richiesta{ID: "id1", Nome: "Prova", Repo: "a/Prova", Tag: tag, File: file, Ambito: ambito, Cartelle: p.c,
		CartellaRegistro: p.reg, GOOS: goos, GOARCH: "amd64",
		privilegiato: func(goos, script string) error {
			p.script = append(p.script, script)
			if p.rifiuta {
				return errors.New("autorizzazione negata")
			}
			return nil
		}}
}

func TestLinuxUtenteInstallaEAggiorna(t *testing.T) {
	p := nuovaProva(t)
	uno := filepath.Join(p.base, "v1", "Prova-Linux-x86_64.AppImage")
	due := filepath.Join(p.base, "v2", "Prova-Linux-x86_64.AppImage")
	scrivi(t, uno, "versione uno")
	scrivi(t, due, "versione due")

	if s := LeggiStato(p.reg, "Prova", "linux", p.c); s.Presente {
		t.Fatalf("prima di installare: %+v", s)
	}
	if _, err := Installa(p.richiesta("linux", "", "v1", uno)); err == nil {
		t.Error("senza ambito non si installa: va chiesto all'utente")
	}
	e, err := Installa(p.richiesta("linux", Utente, "v1.0.0", uno))
	if err != nil {
		t.Fatal(err)
	}
	bersaglio := filepath.Join(p.c.Utente, "Prova.AppImage")
	if e.Percorso != bersaglio || leggi(t, bersaglio) != "versione uno" {
		t.Errorf("installato: %+v", e)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(bersaglio); fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("l'AppImage non è eseguibile: %v", fi.Mode())
		}
	}
	s := LeggiStato(p.reg, "Prova", "linux", p.c)
	if !s.Presente || !s.Nostro || s.Tag != "v1.0.0" || s.Ambito != Utente || s.Precedente {
		t.Errorf("stato dopo la prima installazione: %+v", s)
	}
	if _, err := Installa(p.richiesta("linux", Utente, "v1.1.0", due)); err != nil {
		t.Fatal(err)
	}
	if leggi(t, bersaglio) != "versione due" || esiste(bersaglio+".precedente") {
		t.Error("aggiornamento: la nuova al suo posto, la vecchia cancellata")
	}
	if s = LeggiStato(p.reg, "Prova", "linux", p.c); s.Tag != "v1.1.0" || s.Precedente || s.Tipo != TipoAppImage {
		t.Errorf("stato dopo l'aggiornamento: %+v", s)
	}
	voci, _ := os.ReadDir(p.c.Utente)
	for _, v := range voci {
		if strings.Contains(v.Name(), ".nuovo") {
			t.Errorf("rimasto %s", v.Name())
		}
	}
	if leggi(t, due) != "versione due" {
		t.Error("il pacchetto scaricato è stato modificato")
	}
	if len(p.script) != 0 {
		t.Error("per l'utente non servono permessi di amministratore")
	}
}

func TestLinuxSistemaPassaDalloScript(t *testing.T) {
	p := nuovaProva(t)
	pacchetto := filepath.Join(p.base, "Prova-Linux-x86_64.AppImage")
	scrivi(t, pacchetto, "appimage")
	e, err := Installa(p.richiesta("linux", Sistema, "v1", pacchetto))
	if err != nil {
		t.Fatal(err)
	}
	bersaglio := filepath.Join(p.c.Sistema, "Prova", "Prova.AppImage")
	if e.Percorso != bersaglio || len(p.script) != 1 {
		t.Fatalf("esito %+v, script %d", e, len(p.script))
	}
	for _, atteso := range []string{"set -e", "mkdir -p '" + filepath.Join(p.c.Sistema, "Prova") + "'", "install -m 0755 ", "'" + bersaglio + "'",
		"mv -f '" + bersaglio + "' '" + bersaglio + ".precedente'", "/usr/share/applications/centralhub-prova.desktop",
		"rm -f '" + bersaglio + ".precedente'"} {
		if !strings.Contains(p.script[0], atteso) {
			t.Errorf("nello script manca %q:\n%s", atteso, p.script[0])
		}
	}
	// niente è stato scritto nella cartella di sistema da utente normale
	if esiste(p.c.Sistema) {
		t.Error("la cartella di sistema si tocca solo dallo script")
	}
	// se l'utente nega i permessi l'installazione fallisce e non si annota niente
	q := nuovaProva(t)
	q.rifiuta = true
	if _, err := Installa(q.richiesta("linux", Sistema, "v1", pacchetto)); err == nil {
		t.Error("permessi negati: deve fallire")
	}
	if esiste(fileRegistro(q.reg, "Prova")) {
		t.Error("installazione non riuscita annotata come fatta")
	}
}

func TestScriptProteggonoIPercorsi(t *testing.T) {
	sh := ScriptLinuxSistema("/tmp/o'brien/a b.AppImage", "/opt/Prova/Prova.AppImage", "", "/tmp/v.desktop", "/usr/share/applications/centralhub-prova.desktop")
	if !strings.Contains(sh, `'/tmp/o'\''brien/a b.AppImage'`) {
		t.Errorf("apice non protetto nello script sh:\n%s", sh)
	}
	ps := ScriptWindowsSistema(`C:\Users\O'Brien\x`, `C:\Program Files\Prova`, `C:\ProgramData\Microsoft\Windows\Start Menu\Programs\Prova.lnk`, "Prova.exe")
	for _, atteso := range []string{`$n = 'C:\Users\O''Brien\x'`, `$t = 'C:\Program Files\Prova'`, "$ErrorActionPreference = 'Stop'", "Move-Item -LiteralPath $t -Destination $p", "CreateShortcut(", "Join-Path $t 'Prova.exe'",
		"throw }\r\nif (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }"} {
		if !strings.Contains(ps, atteso) {
			t.Errorf("nello script PowerShell manca %q:\n%s", atteso, ps)
		}
	}
	if strings.Contains(ScriptWindowsSistema("a", "b", "", ""), "CreateShortcut") {
		t.Error("senza exe non si crea il collegamento")
	}
}

func TestProgrammaMessoAMano(t *testing.T) {
	p := nuovaProva(t)
	scrivi(t, filepath.Join(p.c.Sistema, "Prova", "Prova.AppImage"), "messo a mano")
	s := LeggiStato(p.reg, "Prova", "linux", p.c)
	if !s.Presente || s.Nostro || s.Tag != "" || s.Ambito != Sistema {
		t.Errorf("programma non installato da CentralHub: %+v", s)
	}
	// quello dell'utente viene prima
	scrivi(t, filepath.Join(p.c.Utente, "Prova.AppImage"), "anche qui")
	if s = LeggiStato(p.reg, "Prova", "linux", p.c); s.Ambito != Utente {
		t.Errorf("con due copie si guarda prima quella dell'utente: %+v", s)
	}
}

func TestWindowsInstallaEAggiorna(t *testing.T) {
	p := nuovaProva(t)
	uno, due := filepath.Join(p.base, "v1", "Prova-Windows.zip"), filepath.Join(p.base, "v2", "Prova-Windows.zip")
	os.MkdirAll(filepath.Dir(uno), 0o755)
	os.MkdirAll(filepath.Dir(due), 0o755)
	creaZip(t, uno, map[string]string{
		"Prova-Windows/Prova.exe": "exe uno", "Prova-Windows/LICENZE/LICENSE": "gpl", "Prova-Windows/ffmpeg/ffmpeg.exe": "ffmpeg"})
	creaZip(t, due, map[string]string{
		"Prova-Windows/Prova.exe": "exe due", "Prova-Windows/LICENZE/LICENSE": "gpl"})

	e, err := Installa(p.richiesta("windows", Utente, "v1", uno))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(p.c.Utente, "Prova")
	if e.Percorso != dir || leggi(t, filepath.Join(dir, "Prova.exe")) != "exe uno" || leggi(t, filepath.Join(dir, "LICENZE", "LICENSE")) != "gpl" {
		t.Errorf("installato: %+v", e)
	}
	if runtime.GOOS == "windows" && (len(e.Avvisi) != 0 || !esiste(filepath.Join(p.base, "appdata", "Microsoft", "Windows", "Start Menu", "Programs", "Prova.lnk"))) {
		t.Errorf("collegamento nel menu Start non creato: %v", e.Avvisi)
	}
	if exe, err := EseguibileWindows(e.Percorso, "Prova"); err != nil || filepath.Base(exe) != "Prova.exe" {
		t.Errorf("exe da lanciare: %q, %v", exe, err)
	}
	if _, err := Installa(p.richiesta("windows", Utente, "v2", due)); err != nil {
		t.Fatal(err)
	}
	if leggi(t, filepath.Join(dir, "Prova.exe")) != "exe due" || esiste(dir+".precedente") {
		t.Error("aggiornamento Windows: nuova al suo posto, vecchia cancellata")
	}
	if esiste(filepath.Join(dir, "ffmpeg")) {
		t.Error("nella versione nuova è rimasto un file della vecchia")
	}
	if s := LeggiStato(p.reg, "Prova", "windows", p.c); !s.Nostro || s.Tag != "v2" || s.Precedente || s.Tipo != TipoPortable || s.Asset != "Prova-Windows.zip" {
		t.Errorf("stato: %+v", s)
	}
	// per tutto il computer: si prepara altrove e lo script sposta
	e, err = Installa(p.richiesta("windows", Sistema, "v2", due))
	if err != nil || len(p.script) != 1 {
		t.Fatalf("sistema: %+v %v, script %d", e, err, len(p.script))
	}
	if e.Percorso != filepath.Join(p.c.Sistema, "Prova") || !strings.Contains(p.script[0], "'"+e.Percorso+"'") || !strings.Contains(p.script[0], "'Prova.exe'") {
		t.Errorf("script per Windows:\n%s", p.script[0])
	}
	if esiste(p.c.Sistema) {
		t.Error("la cartella di sistema si tocca solo dallo script")
	}
}

func TestEstraiZipRifiutaPercorsiFuori(t *testing.T) {
	base := t.TempDir()
	for _, cattivo := range []string{"../fuori.txt", "dentro/../../fuori.txt", "/assoluto.txt"} {
		z := filepath.Join(base, "cattivo.zip")
		creaZip(t, z, map[string]string{cattivo: "x", "buono/altro.txt": "y"})
		dest := filepath.Join(base, "dest")
		os.RemoveAll(dest)
		err := EstraiZip(z, dest)
		if cattivo != "/assoluto.txt" && err == nil {
			t.Errorf("%q: doveva essere rifiutato", cattivo)
		}
		if esiste(filepath.Join(base, "fuori.txt")) || esiste("/assoluto.txt") {
			t.Fatalf("%q: scritto fuori dalla destinazione", cattivo)
		}
	}
	// più cartelle in cima: non si toglie niente
	z := filepath.Join(base, "due.zip")
	creaZip(t, z, map[string]string{"a/1.txt": "1", "b/2.txt": "2"})
	dest := filepath.Join(base, "due")
	if err := EstraiZip(z, dest); err != nil {
		t.Fatal(err)
	}
	if !esiste(filepath.Join(dest, "a", "1.txt")) || !esiste(filepath.Join(dest, "b", "2.txt")) {
		t.Error("zip con due cartelle in cima: struttura non conservata")
	}
	if err := EstraiZip(filepath.Join(base, "non-esiste.zip"), filepath.Join(base, "x")); err == nil {
		t.Error("zip inesistente: deve fallire")
	}
}

func TestScambiaContenuto(t *testing.T) {
	base := t.TempDir()
	inst, nuovo := filepath.Join(base, "CentralHub"), filepath.Join(base, "nuovo")
	scrivi(t, filepath.Join(inst, "CentralHub.exe"), "vecchio")
	scrivi(t, filepath.Join(inst, "LICENZE", "LICENSE"), "vecchia")
	scrivi(t, filepath.Join(nuovo, "CentralHub.exe"), "nuovo")
	scrivi(t, filepath.Join(nuovo, "LICENZE", "LICENSE"), "nuova")
	if err := scambiaContenuto(nuovo, inst); err != nil {
		t.Fatal(err)
	}
	if leggi(t, filepath.Join(inst, "CentralHub.exe")) != "nuovo" || leggi(t, filepath.Join(inst, "CentralHub.exe.precedente")) != "vecchio" ||
		leggi(t, filepath.Join(inst, "LICENZE", "LICENSE")) != "nuova" || leggi(t, filepath.Join(inst, "LICENZE.precedente", "LICENSE")) != "vecchia" {
		t.Error("scambio file per file non riuscito")
	}
}

func TestVoceDesktop(t *testing.T) {
	v := VoceDesktop("Aurora Studio", `/home/u/App/con "virgolette" $e spazi/Aurora-Studio.AppImage`, "/home/u/icone/Aurora-Studio.png", "abc")
	for _, atteso := range []string{
		"[Desktop Entry]\n", "Type=Application\n", "Name=Aurora Studio\n",
		`Exec="/home/u/App/con \"virgolette\" \$e spazi/Aurora-Studio.AppImage" %U` + "\n",
		"Icon=/home/u/icone/Aurora-Studio.png\n", "Terminal=false\n", "StartupWMClass=Aurora-Studio\n", "X-CentralHub-Repo=abc\n",
	} {
		if !strings.Contains(v, atteso) {
			t.Errorf("manca %q in:\n%s", atteso, v)
		}
	}
	if strings.Contains(VoceDesktop("x", "/a", "", "i"), "Icon=") {
		t.Error("senza icona non va scritta la riga Icon")
	}
}

// Il giro vero su macOS: uno zip fatto con ditto come quelli pubblicati.
func TestMacInstallaEAggiorna(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("solo su macOS")
	}
	p := nuovaProva(t)
	arch, nomeApp := "arm64", "Prova-AppleSilicon"
	if runtime.GOARCH == "amd64" {
		arch, nomeApp = "amd64", "Prova-Intel"
	}
	fai := func(tag, versione string) string {
		src := filepath.Join(p.base, "src-"+tag, nomeApp+".app")
		scrivi(t, filepath.Join(src, "Contents", "MacOS", "Prova"), tag)
		os.Chmod(filepath.Join(src, "Contents", "MacOS", "Prova"), 0o755)
		scrivi(t, filepath.Join(src, "Contents", "Info.plist"), "<plist><dict><key>CFBundleShortVersionString</key>\n<string>"+versione+"</string></dict></plist>")
		os.Symlink("MacOS/Prova", filepath.Join(src, "Contents", "collegamento"))
		zip := filepath.Join(p.base, tag, nomeApp+".zip")
		os.MkdirAll(filepath.Dir(zip), 0o755)
		if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", src, zip).CombinedOutput(); err != nil {
			t.Fatalf("ditto: %v %s", err, out)
		}
		return zip
	}
	r := p.richiesta("darwin", Utente, "v1.0.0", fai("v1.0.0", "1.0.0"))
	r.GOARCH = arch
	e, err := Installa(r)
	if err != nil {
		t.Fatal(err)
	}
	bersaglio := filepath.Join(p.c.Utente, nomeApp+".app")
	exe := filepath.Join(bersaglio, "Contents", "MacOS", "Prova")
	if e.Percorso != bersaglio || leggi(t, exe) != "v1.0.0" {
		t.Errorf("installata: %+v", e)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o111 == 0 {
		t.Error("ditto deve conservare il permesso di esecuzione")
	}
	if l, err := os.Readlink(filepath.Join(bersaglio, "Contents", "collegamento")); err != nil || l != "MacOS/Prova" {
		t.Errorf("ditto deve conservare i collegamenti: %q %v", l, err)
	}
	r = p.richiesta("darwin", Utente, "v2.0.0", fai("v2.0.0", "2.0.0"))
	if _, err := Installa(r); err != nil {
		t.Fatal(err)
	}
	if leggi(t, exe) != "v2.0.0" || esiste(bersaglio+".precedente") {
		t.Error("aggiornamento macOS non riuscito")
	}
	if s := LeggiStato(p.reg, "Prova", "darwin", p.c); !s.Nostro || s.Tag != "v2.0.0" || s.Versione != "2.0.0" || s.Precedente || s.Percorso != bersaglio {
		t.Errorf("stato: %+v", s)
	}
	// per tutto il computer su macOS si scrive direttamente (qui la cartella è di prova)
	r = p.richiesta("darwin", Sistema, "v2.0.0", fai("v2.0.1", "2.0.1"))
	if e, err = Installa(r); err != nil || e.Percorso != filepath.Join(p.c.Sistema, nomeApp+".app") || len(p.script) != 0 {
		t.Errorf("macOS, tutto il computer: %+v %v", e, err)
	}
	for _, d := range []string{p.c.Utente, p.c.Sistema} {
		voci, _ := os.ReadDir(d)
		for _, v := range voci {
			if strings.Contains(v.Name(), ".nuovo") {
				t.Errorf("rimasto %s", v.Name())
			}
		}
	}
	// un'app messa a mano viene rilevata, con la sua versione
	q := nuovaProva(t)
	scrivi(t, filepath.Join(q.c.Sistema, nomeApp+".app", "Contents", "Info.plist"), "<key>CFBundleShortVersionString</key><string>0.9.1</string>")
	if s := LeggiStato(q.reg, "Prova", "darwin", q.c); !s.Presente || s.Nostro || s.Versione != "0.9.1" || s.Ambito != Sistema {
		t.Errorf("app messa a mano: %+v", s)
	}
}
