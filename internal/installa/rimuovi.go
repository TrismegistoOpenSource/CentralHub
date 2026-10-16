// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Rimozione è la richiesta di disinstallare un programma.
type Rimozione struct {
	Nome string
	// Cartelle di installazione del sistema.
	Cartelle Cartelle
	// CartellaRegistro è dove CentralHub annota ciò che installa.
	CartellaRegistro string
	// Dati sono le cartelle e i file di impostazioni del programma che
	// l'utente ha chiesto di togliere (quelli trovati da DatiProgramma):
	// vanno nel cestino. Vuoto = restano dove sono.
	Dati []string
	// Ctx ferma l'attesa di un disinstallatore; nil = nessun limite.
	Ctx  context.Context
	GOOS string
	// Cestina sposta nel cestino; nil = il cestino del sistema.
	Cestina func(goos, percorso string) error
	// privilegiato esegue uno script con i permessi di amministratore;
	// sostituibile nelle prove.
	privilegiato func(goos, script string) error
}

// EsitoRimozione di una disinstallazione riuscita.
type EsitoRimozione struct {
	// Percorso da cui il programma è stato tolto ("" se non si sa dov'era).
	Percorso string
	// DatiTolti sono le cartelle di impostazioni finite nel cestino.
	DatiTolti []string
	// Avvisi: ciò che non è riuscito ma non ha impedito la disinstallazione.
	Avvisi []string
}

// Disinstalla toglie il programma dal computer: i suoi file, la versione
// precedente se è rimasta, la voce di menu e ciò che CentralHub aveva
// annotato. Un programma messo dal suo installatore lo toglie il suo
// disinstallatore. Le impostazioni del programma si toccano solo se sono
// state indicate in Dati.
func Disinstalla(d Rimozione) (EsitoRimozione, error) {
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	if d.privilegiato == nil {
		d.privilegiato = eseguiPrivilegiato
	}
	if d.Cestina == nil {
		d.Cestina = Cestina
	}
	if d.Ctx == nil {
		d.Ctx = context.Background()
	}
	s := LeggiStato(d.CartellaRegistro, d.Nome, d.GOOS, d.Cartelle)
	if !s.Presente {
		return EsitoRimozione{}, errors.New(d.Nome + " non risulta installato")
	}
	reg, _ := leggiRegistro(d.CartellaRegistro, d.Nome)
	esito := EsitoRimozione{Percorso: s.Percorso}
	if s.Percorso != "" {
		if seStesso(s.Percorso) {
			return EsitoRimozione{}, errors.New("CentralHub non può disinstallare sé stesso: è il programma che stai usando")
		}
		if inEsecuzione(percorsoProcessi(s.Percorso, d.GOOS), d.GOOS) {
			return EsitoRimozione{}, &ErrInEsecuzione{d.Nome}
		}
	}
	// la voce di menu si toglie solo se l'ha creata CentralHub
	voceMenu := ""
	if s.Nostro {
		voceMenu = reg.MenuVoce
	}
	precedente := s.Percorso + suffissoPrecedente
	cancella := func(p string) error {
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("%s non si può cancellare (è in uso, o servono altri permessi): %w", p, err)
		}
		return nil
	}

	var err error
	switch {
	case s.Tipo == TipoDeb:
		if debInstallato(reg.Chiave) {
			err = d.privilegiato("linux", ScriptDebRimuovi(reg.Chiave))
		}
	case s.Tipo == TipoPkg:
		err = disinstallaPkg(d, reg.Chiave)
	case s.Tipo == TipoInstallatore:
		if v, c := leggiVoci()[reg.Chiave]; c {
			err = avviaDisinstallatore(d.Ctx, v)
		}
	case d.GOOS == "windows":
		// un programma messo a mano può essere arrivato con un installatore:
		// in quel caso lo toglie il suo disinstallatore
		if v, c := voceDiCartella(leggiVoci(), s.Percorso); c && !s.Nostro {
			err = avviaDisinstallatore(d.Ctx, v)
			break
		}
		if s.Ambito == Sistema {
			err = d.privilegiato("windows", ScriptWindowsRimuovi(s.Percorso, voceMenu))
			break
		}
		if err = cancella(s.Percorso); err == nil {
			err = cancella(precedente)
		}
		if err == nil && voceMenu != "" {
			os.Remove(voceMenu)
		}
	case d.GOOS == "linux":
		icona := strings.TrimSuffix(s.Percorso, ".AppImage") + ".png"
		if s.Ambito == Sistema {
			err = d.privilegiato("linux", ScriptLinuxRimuovi(s.Percorso, icona, voceMenu))
			break
		}
		if err = cancella(s.Percorso); err == nil {
			err = cancella(precedente)
		}
		if err == nil && voceMenu != "" {
			os.Remove(voceMenu)
			aggiornaMenuLinux(filepath.Dir(voceMenu))
		}
	case d.GOOS == "darwin":
		// /Applications è scrivibile da un utente amministratore
		if err = cancella(s.Percorso); err == nil {
			err = cancella(precedente)
		}
	default:
		err = fmt.Errorf("disinstallazione non prevista su %s", d.GOOS)
	}
	if err != nil {
		return EsitoRimozione{}, err
	}

	// ciò che CentralHub aveva annotato: la versione installata e l'icona
	// estratta per la voce di menu
	os.Remove(fileRegistro(d.CartellaRegistro, d.Nome))
	os.Remove(filepath.Join(d.CartellaRegistro, NomeProgramma(d.Nome)+".png"))

	for _, p := range d.Dati {
		if !esiste(p) {
			continue
		}
		if err := d.Cestina(d.GOOS, p); err != nil {
			esito.Avvisi = append(esito.Avvisi, "impostazioni non spostate nel cestino, restano in "+p+": "+err.Error())
			continue
		}
		esito.DatiTolti = append(esito.DatiTolti, p)
	}
	return esito, nil
}

// voceDiCartella trova, fra le app installate secondo il sistema, quella che
// dichiara di stare in una cartella.
func voceDiCartella(voci map[string]VoceSistema, cartella string) (VoceSistema, bool) {
	if cartella == "" {
		return VoceSistema{}, false
	}
	pulita := func(p string) string { return strings.ToLower(strings.TrimRight(filepath.Clean(p), `\/`)) }
	var trovate []VoceSistema
	for _, v := range voci {
		if v.Posizione != "" && pulita(v.Posizione) == pulita(cartella) {
			trovate = append(trovate, v)
		}
	}
	if len(trovate) == 1 {
		return trovate[0], true
	}
	return VoceSistema{}, false
}

// ScriptWindowsRimuovi è lo script PowerShell che, con i permessi di
// amministratore, toglie da %ProgramFiles% la cartella del programma (e la
// versione precedente, se è rimasta) e il collegamento nel menu Start di
// tutti gli utenti.
func ScriptWindowsRimuovi(bersaglio, collegamento string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	b.WriteString("$t = " + apicePS(bersaglio) + "\r\n")
	b.WriteString("foreach ($x in @($t, ($t + '.precedente'))) { if (Test-Path -LiteralPath $x) { Remove-Item -LiteralPath $x -Recurse -Force } }\r\n")
	if collegamento != "" {
		b.WriteString("$l = " + apicePS(collegamento) + "\r\n")
		b.WriteString("if (Test-Path -LiteralPath $l) { Remove-Item -LiteralPath $l -Force }\r\n")
	}
	return b.String()
}

// ScriptLinuxRimuovi è lo script che, con i permessi di amministratore,
// toglie da /opt/<Nome>/ l'AppImage (e la versione precedente, se è
// rimasta), la sua icona, la cartella se resta vuota e la voce nel menu di
// tutti gli utenti.
func ScriptLinuxRimuovi(bersaglio, icona, voce string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")
	b.WriteString("rm -f " + apice(bersaglio) + " " + apice(bersaglio+suffissoPrecedente))
	if icona != "" {
		b.WriteString(" " + apice(icona))
	}
	b.WriteString("\n")
	b.WriteString("rmdir " + apice(filepath.Dir(bersaglio)) + " 2>/dev/null || true\n")
	if voce != "" {
		b.WriteString("rm -f " + apice(voce) + "\n")
	}
	return b.String()
}

// aggiornaMenuLinux avvisa il menu delle applicazioni, dove c'è lo strumento
// per farlo (non è un errore se manca: il menu si aggiorna da solo).
func aggiornaMenuLinux(cartella string) {
	if u, err := exec.LookPath("update-desktop-database"); err == nil {
		ctx, annulla := context.WithTimeout(context.Background(), 10*time.Second)
		defer annulla()
		exec.CommandContext(ctx, u, cartella).Run()
	}
}

// ---------------------------------------------------------------- impostazioni del programma

// BasiDati sono le cartelle del sistema in cui i programmi tengono
// impostazioni e dati dell'utente.
func BasiDati(goos string) []string {
	casa, _ := os.UserHomeDir()
	var basi []string
	metti := func(p ...string) {
		for _, x := range p {
			if x != "" && filepath.IsAbs(x) {
				basi = append(basi, x)
			}
		}
	}
	switch goos {
	case "windows":
		metti(os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"))
	case "darwin":
		if casa != "" {
			l := filepath.Join(casa, "Library")
			metti(filepath.Join(l, "Application Support"), filepath.Join(l, "Caches"), filepath.Join(l, "Preferences"),
				filepath.Join(l, "Saved Application State"), filepath.Join(l, "WebKit"), filepath.Join(l, "HTTPStorages"))
		}
	default:
		cfg, cache := os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_CACHE_HOME")
		if cfg == "" && casa != "" {
			cfg = filepath.Join(casa, ".config")
		}
		if cache == "" && casa != "" {
			cache = filepath.Join(casa, ".cache")
		}
		dati, _ := cartellaDati()
		metti(cfg, cache, dati)
	}
	return basi
}

var reIDApp = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)

// nomiCondivisi sono cartelle di sistema o di più programmi: non sono mai
// "le impostazioni di un programma", anche se un repository si chiama così.
var nomiCondivisi = map[string]bool{
	"microsoft": true, "programs": true, "packages": true, "temp": true, "windows": true, "google": true, "mozilla": true,
	"applications": true, "autostart": true, "icons": true, "fonts": true, "trash": true, "apple": true, "systemd": true,
}

// DatiProgramma cerca nelle cartelle di base ciò che porta il nome del
// programma: le sue impostazioni e i suoi dati. percorso è dove il programma
// è installato (serve a conoscere il nome del suo exe o l'identificativo
// dell'app, e non viene mai restituito); escludi sono percorsi da non
// proporre mai.
func DatiProgramma(basi []string, nome, percorso, goos string, escludi ...string) []string {
	nomi := map[string]bool{}
	metti := func(s string) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" && !nomiCondivisi[s] {
			nomi[s] = true
		}
	}
	metti(nome)
	metti(NomeProgramma(nome))
	metti(strings.ReplaceAll(nome, " ", ""))
	switch goos {
	case "windows":
		// le app con la webview di sistema tengono il profilo in <Exe>.exe
		metti(NomeProgramma(nome) + ".exe")
		if exe, err := EseguibileWindows(percorso, nome); err == nil && percorso != "" {
			b := filepath.Base(exe)
			metti(b)
			metti(strings.TrimSuffix(b, filepath.Ext(b)))
		}
	case "darwin":
		if b, err := os.ReadFile(filepath.Join(percorso, "Contents", "Info.plist")); err == nil && percorso != "" {
			if m := reIDApp.FindSubmatch(b); m != nil {
				id := strings.TrimSpace(string(m[1]))
				metti(id)
				metti(id + ".plist")
				metti(id + ".savedState")
				metti(id + ".binarycookies")
			}
		}
	}
	uguale := func(a, b string) bool {
		a, b = filepath.Clean(a), filepath.Clean(b)
		if goos == "linux" {
			return a == b
		}
		return strings.EqualFold(a, b)
	}
	var trovati []string
	for _, base := range basi {
		voci, err := os.ReadDir(base)
		if err != nil {
			continue
		}
	voci:
		for _, v := range voci {
			if !nomi[strings.ToLower(v.Name())] {
				continue
			}
			p := filepath.Join(base, v.Name())
			for _, e := range append([]string{percorso}, escludi...) {
				if e != "" && uguale(p, e) {
					continue voci
				}
			}
			trovati = append(trovati, p)
		}
	}
	sort.Strings(trovati)
	return trovati
}

// Cestina sposta un file o una cartella nel cestino del sistema: le
// impostazioni di un programma disinstallato non si cancellano per sempre.
func Cestina(goos, percorso string) error {
	fi, err := os.Lstat(percorso)
	if err != nil {
		return err
	}
	ctx, annulla := context.WithTimeout(context.Background(), 2*time.Minute)
	defer annulla()
	switch goos {
	case "windows":
		// Il percorso passa da una variabile d'ambiente, non dal testo del comando.
		metodo := "DeleteFile"
		if fi.IsDir() {
			metodo = "DeleteDirectory"
		}
		script := `$ErrorActionPreference = 'Stop'; Add-Type -AssemblyName Microsoft.VisualBasic; ` +
			`[Microsoft.VisualBasic.FileIO.FileSystem]::` + metodo + `($env:CENTRALHUB_CESTINO, 'OnlyErrorDialogs', 'SendToRecycleBin')`
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
		cmd.Env = append(os.Environ(), "CENTRALHUB_CESTINO="+percorso)
		nascondi(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
		}
	case "darwin":
		// Non basta una rinomina in ~/.Trash: la cartella è protetta dal
		// sistema (a un'app senza «Accesso completo al disco» dà "Operation
		// not permitted"). NSFileManager la sposta nel cestino vero, anche
		// da una cartella di sistema, e il Finder la mostra; ci arriva
		// osascript (JavaScript), senza chiedere di controllare il Finder.
		// Il percorso passa come argomento, non nel testo.
		const js = `ObjC.import('Foundation');
function run(argv) {
  var errore = Ref();
  var u = $.NSURL.fileURLWithPath(argv[0]);
  if (!$.NSFileManager.defaultManager.trashItemAtURLResultingItemURLError(u, null, errore)) throw new Error('non spostato nel cestino');
  return 'ok';
}`
		if out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", js, percorso).CombinedOutput(); err != nil {
			return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
		}
	default:
		gio, err := exec.LookPath("gio")
		if err != nil {
			return errors.New("su questo sistema manca lo strumento per il cestino (gio)")
		}
		if out, err := exec.CommandContext(ctx, gio, "trash", "--", percorso).CombinedOutput(); err != nil {
			return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
		}
	}
	if esiste(percorso) {
		return errors.New("il sistema non l'ha spostato nel cestino")
	}
	return nil
}
