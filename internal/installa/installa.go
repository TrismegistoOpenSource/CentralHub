// SPDX-License-Identifier: MIT

// Package installa mette sul computer il programma di una release, lo
// aggiorna se c'è già e lo toglie. Riceve il pacchetto scelto, già
// scaricato, e si comporta secondo il suo tipo: i pacchetti che gestisce da
// sé (zip portable, AppImage, zip o .dmg di un'app) li porta nella cartella
// di installazione; un installatore (setup.exe, .msi, .deb, .pkg) lo fa
// eseguire al sistema e guarda cosa ha lasciato (app installate, dpkg,
// ricevute di pkgutil). Per i primi l'utente sceglie l'ambito: solo per sé
// ("appdata") o per tutto il computer ("Programmi").
//
//	            solo per l'utente                  tutto il computer
//	Linux       ~/App/<Nome>.AppImage              /opt/<Nome>/<Nome>.AppImage
//	Windows     %LOCALAPPDATA%\Programs\<Nome>\     %ProgramFiles%\<Nome>\
//	macOS       ~/Applications/<Nome…>.app         /Applications/<Nome…>.app
//
// Lo scambio fra versione vecchia e nuova è una rinomina: o c'è la vecchia
// intera, o la nuova intera. Riuscito lo scambio, la vecchia si cancella.
// Per tutto il computer servono i permessi di amministratore: li chiede il
// sistema (UAC, pkexec) solo per il passo che sposta i file.
package installa

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const suffissoPrecedente = ".precedente"

// Ambiti di installazione (gli stessi valori di config).
const (
	Utente  = "utente"
	Sistema = "sistema"
)

// Tipi di pacchetto: dicono come ci si comporta per installare, aggiornare
// e togliere il programma.
const (
	TipoPortable     = "portable"     // Windows: zip con la cartella del programma
	TipoEseguibile   = "eseguibile"   // Windows: un solo exe, che è il programma
	TipoInstallatore = "installatore" // Windows: setup.exe o .msi, con la sua procedura
	TipoAppImage     = "appimage"     // Linux
	TipoDeb          = "deb"          // Linux (Debian e derivate): lo installa apt, che chiede i permessi
	TipoApp          = "app"          // macOS: zip con dentro l'app
	TipoDmg          = "dmg"          // macOS: immagine disco con dentro l'app
	TipoPkg          = "pkg"          // macOS: pacchetto del sistema, lo avvia l'Installer
)

// tipoPredefinito è il tipo che CentralHub gestisce da sé su un sistema: è
// anche quello delle installazioni annotate prima che il tipo esistesse.
func tipoPredefinito(goos string) string {
	switch goos {
	case "linux":
		return TipoAppImage
	case "windows":
		return TipoPortable
	}
	return TipoApp
}

// DaSistema dice se, per questo tipo di pacchetto, dove e come installare lo
// decide il sistema (un installatore, apt, l'Installer di macOS): all'utente
// non si chiede l'ambito.
func DaSistema(tipo string) bool {
	return tipo == TipoInstallatore || tipo == TipoDeb || tipo == TipoPkg
}

// StessoGenere dice se due tipi di pacchetto portano allo stesso risultato
// sul disco: un'app di macOS è la stessa cosa sia che arrivi da uno zip sia
// da un'immagine disco, e si può aggiornare con l'una o con l'altra.
func StessoGenere(a, b string) bool {
	app := func(t string) bool { return t == TipoApp || t == TipoDmg }
	return a == b || (app(a) && app(b))
}

// Cartelle sono le due cartelle di installazione di un sistema.
type Cartelle struct {
	Utente  string `json:"utente"`
	Sistema string `json:"sistema"`
}

// CartellePredefinite dice dove si installa su questo sistema. Su Linux la
// cartella dell'utente è ~/App (deciso dall'utente del programma).
func CartellePredefinite(goos string) (Cartelle, error) {
	casa, err := os.UserHomeDir()
	if err != nil {
		return Cartelle{}, fmt.Errorf("cartella dell'utente non trovata: %w", err)
	}
	switch goos {
	case "darwin":
		return Cartelle{filepath.Join(casa, "Applications"), "/Applications"}, nil
	case "windows":
		u := os.Getenv("LOCALAPPDATA")
		if u == "" {
			u = filepath.Join(casa, "AppData", "Local")
		}
		s := os.Getenv("ProgramFiles")
		if s == "" {
			s = `C:\Program Files`
		}
		return Cartelle{filepath.Join(u, "Programs"), s}, nil
	}
	return Cartelle{filepath.Join(casa, "App"), "/opt"}, nil
}

var reNome = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// NomeProgramma ricava dal nome del repository il nome del file installato:
// solo lettere, numeri e trattini (regola del workspace: niente spazi né
// parentesi nei nomi).
func NomeProgramma(nome string) string {
	n := strings.Trim(reNome.ReplaceAllString(strings.TrimSpace(nome), "-"), "-")
	if n == "" {
		return "Programma"
	}
	return n
}

// Pacchetto è un file di una release che su questo sistema si può
// installare, con il modo in cui lo si installa. Uno stesso file può
// comparire due volte, con due tipi, quando dal nome non si capisce cos'è
// (un .exe senza altre indicazioni: il programma o il suo installatore?).
type Pacchetto struct {
	Nome string `json:"nome"`
	Tipo string `json:"tipo"`
	// Consigliato: è quello che CentralHub gestisce da sé (o è l'unico).
	Consigliato bool `json:"consigliato"`
	// Dimensione in byte: la mette chi conosce la release.
	Dimensione int64 `json:"dimensione"`
}

func contiene(n string, parole ...string) bool {
	for _, p := range parole {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// tipiDi dice in quali modi un file (nome in minuscolo) si può installare su
// un sistema; nessuno se non è un pacchetto per quel sistema. Con forzato il
// file l'ha indicato l'utente con un modello: si guarda solo l'estensione.
func tipiDi(l, goos string, forzato bool) []string {
	switch goos {
	case "linux":
		switch {
		case strings.HasSuffix(l, ".deb"):
			return []string{TipoDeb}
		case forzato || strings.HasSuffix(l, ".appimage"):
			return []string{TipoAppImage}
		}
	case "windows":
		altroSistema := !forzato && contiene(l, "darwin", "macos", "linux")
		switch {
		case altroSistema:
		case strings.HasSuffix(l, ".msi"):
			return []string{TipoInstallatore}
		case strings.HasSuffix(l, ".exe"):
			if reInstallatore.MatchString(l) {
				return []string{TipoInstallatore}
			}
			if strings.Contains(l, "portable") {
				return []string{TipoEseguibile}
			}
			return []string{TipoEseguibile, TipoInstallatore} // dal nome non si capisce
		case forzato || (strings.HasSuffix(l, ".zip") && reWindows.MatchString(l)):
			return []string{TipoPortable}
		}
	case "darwin":
		altroSistema := !forzato && (reWindows.MatchString(l) || strings.Contains(l, "linux"))
		switch {
		case altroSistema:
		case strings.HasSuffix(l, ".dmg"):
			return []string{TipoDmg}
		case strings.HasSuffix(l, ".pkg"):
			return []string{TipoPkg}
		case forzato || (strings.HasSuffix(l, ".zip") &&
			contiene(l, "macos", "darwin", "osx", "applesilicon", "apple-silicon", "intel", "arm64", "aarch64", "x86_64", "x86-64", "amd64", "x64")):
			return []string{TipoApp}
		}
	}
	return nil
}

// Candidati elenca, fra i file di una release, quelli che su questo sistema
// si possono installare. Con un modello (caratteri * e ?) decide l'utente;
// altrimenti si riconoscono dal nome.
func Candidati(nomi []string, goos, goarch, modello string) ([]Pacchetto, error) {
	var buoni, ripiego []Pacchetto
	if modello != "" {
		m := strings.ToLower(modello)
		for _, n := range nomi {
			if ok, _ := filepath.Match(m, strings.ToLower(n)); ok {
				for _, t := range tipiDi(strings.ToLower(n), goos, true) {
					buoni = append(buoni, Pacchetto{Nome: n, Tipo: t})
				}
				break
			}
		}
		if len(buoni) == 0 {
			return nil, fmt.Errorf("nessun file della release corrisponde a %q", modello)
		}
	} else {
		arm := goarch == "arm64"
		for _, n := range nomi {
			l := strings.ToLower(n)
			if strings.Contains(l, "sorgente") || strings.Contains(l, "source") || reNonProgramma.MatchString(l) {
				continue
			}
			archARM := contiene(l, "arm64", "aarch64", "applesilicon", "apple-silicon")
			archX86 := contiene(l, "x86_64", "x86-64", "amd64", "x64", "intel")
			giusta := (arm && archARM) || (!arm && archX86)
			altra := (arm && archX86 && !archARM) || (!arm && archARM && !archX86)
			if altra {
				continue
			}
			for _, t := range tipiDi(l, goos, false) {
				if giusta {
					buoni = append(buoni, Pacchetto{Nome: n, Tipo: t})
				} else {
					ripiego = append(ripiego, Pacchetto{Nome: n, Tipo: t}) // nessuna architettura nel nome
				}
			}
		}
		if len(buoni) == 0 {
			buoni = ripiego
		}
	}
	// consigliato: l'unico, oppure l'unico del tipo che CentralHub gestisce da
	// sé. Su macOS è lo zip con l'app; senza, l'immagine disco (l'app la copia
	// da sé), mentre un .pkg lo fa fare all'Installer del sistema.
	gestito := tipoPredefinito(goos)
	if goos == "darwin" {
		haApp := false
		for _, p := range buoni {
			haApp = haApp || p.Tipo == TipoApp
		}
		if !haApp {
			gestito = TipoDmg
		}
	}
	gestiti := 0
	for _, p := range buoni {
		if p.Tipo == gestito {
			gestiti++
		}
	}
	for i := range buoni {
		buoni[i].Consigliato = len(buoni) == 1 || (gestiti == 1 && buoni[i].Tipo == gestito)
	}
	return buoni, nil
}

// ErrScelta: più pacchetti adatti a questo sistema, deve scegliere l'utente.
type ErrScelta struct{ Pacchetti []Pacchetto }

func (e *ErrScelta) Error() string {
	var nomi []string
	for _, p := range e.Pacchetti {
		if len(nomi) == 0 || nomi[len(nomi)-1] != p.Nome {
			nomi = append(nomi, p.Nome)
		}
	}
	return "più pacchetti adatti a questo sistema (" + strings.Join(nomi, ", ") + "): va scelto quale installare, dal pulsante del programma"
}

var reVersioneNelNome = regexp.MustCompile(`\d+(\.\d+)*`)

// forma toglie da un nome i numeri di versione: due pacchetti dello stesso
// genere di due release diverse hanno la stessa forma.
func forma(nome string) string {
	return reVersioneNelNome.ReplaceAllString(strings.ToLower(nome), "#")
}

// Scegli decide quale pacchetto installare. voluto è la scelta esplicita
// dell'utente (nome, ed eventualmente tipo); installato è ciò che c'è già
// sul computer: un aggiornamento resta dello stesso genere. Senza l'una né
// l'altro va bene solo un candidato unico.
func Scegli(c []Pacchetto, voluto, installato Pacchetto) (Pacchetto, error) {
	if len(c) == 0 {
		return Pacchetto{}, ErrNessunAsset
	}
	if voluto.Nome != "" {
		var trovati []Pacchetto
		for _, p := range c {
			if p.Nome == voluto.Nome && (voluto.Tipo == "" || p.Tipo == voluto.Tipo) {
				trovati = append(trovati, p)
			}
		}
		switch len(trovati) {
		case 0:
			return Pacchetto{}, fmt.Errorf("il pacchetto scelto (%s) non è fra quelli installabili di questa release", voluto.Nome)
		case 1:
			return trovati[0], nil
		}
		return Pacchetto{}, &ErrScelta{trovati}
	}
	if installato.Tipo != "" {
		var stessi []Pacchetto
		for _, p := range c {
			if p.Tipo == installato.Tipo {
				stessi = append(stessi, p)
			}
		}
		if len(stessi) == 0 { // la stessa app può arrivare da un altro contenitore (zip, .dmg)
			for _, p := range c {
				if StessoGenere(p.Tipo, installato.Tipo) {
					stessi = append(stessi, p)
				}
			}
		}
		if len(stessi) > 1 && installato.Nome != "" {
			var uguali []Pacchetto
			for _, p := range stessi {
				if forma(p.Nome) == forma(installato.Nome) {
					uguali = append(uguali, p)
				}
			}
			if len(uguali) > 0 {
				stessi = uguali
			}
		}
		switch len(stessi) {
		case 0:
			return Pacchetto{}, fmt.Errorf("questa release non ha un pacchetto come quello installato (%s): per cambiare genere di pacchetto il programma va prima disinstallato", installato.Tipo)
		case 1:
			return stessi[0], nil
		}
		return Pacchetto{}, &ErrScelta{stessi}
	}
	if len(c) == 1 {
		return c[0], nil
	}
	return Pacchetto{}, &ErrScelta{c}
}

// ScegliAsset trova, fra i file di una release, l'unico da installare su
// questo sistema; se ce n'è più d'uno dà ErrScelta.
func ScegliAsset(nomi []string, goos, goarch, modello string) (string, error) {
	c, err := Candidati(nomi, goos, goarch, modello)
	if err != nil {
		return "", err
	}
	p, err := Scegli(c, Pacchetto{}, Pacchetto{})
	return p.Nome, err
}

// reNonProgramma riconosce i pacchetti che non sono il programma da
// installare: strumenti da riga di comando, versioni web, simboli di debug.
var reNonProgramma = regexp.MustCompile(`(^|[-_.])(cli|web|sdk|debug|dbg|symbols)([-_.]|$)`)

// reWindows riconosce "windows", "win64", "win32" o "win" come parola a sé
// (non dentro "darwin").
var reWindows = regexp.MustCompile(`windows|(^|[-_.])win(64|32)?([-_.]|$)`)

// reInstallatore riconosce dal nome un exe che è un installatore.
var reInstallatore = regexp.MustCompile(`setup|install`)

// ErrNessunAsset: la release non ha niente di installabile su questo sistema.
var ErrNessunAsset = errors.New("la release non contiene un pacchetto installabile su questo sistema")

// Registro è ciò che CentralHub ricorda di un programma che ha installato.
// Sta nella cartella di configurazione, non accanto al programma: così vale
// anche per le installazioni in cartelle di sistema.
type Registro struct {
	ID       string `json:"id"`
	Nome     string `json:"nome"`
	Repo     string `json:"repo"`
	Tag      string `json:"tag"`
	Asset    string `json:"asset"`
	Ambito   string `json:"ambito"`
	Percorso string `json:"percorso"`
	Quando   string `json:"installatoIl"`
	MenuVoce string `json:"voceDiMenu,omitempty"`
	// Tipo del pacchetto installato; vuoto = quello predefinito del sistema.
	Tipo string `json:"tipo,omitempty"`
	// Fissata: la versione l'ha scelta l'utente e non era l'ultima;
	// «Aggiorna tutti» la lascia stare.
	Fissata bool `json:"fissata,omitempty"`
	// Chiave e NomeMostrato: per un installatore, la voce che ha lasciato
	// fra le app installate del sistema.
	Chiave       string `json:"chiaveDiSistema,omitempty"`
	NomeMostrato string `json:"nomeDiSistema,omitempty"`
}

func fileRegistro(cartellaRegistro, nome string) string {
	return filepath.Join(cartellaRegistro, NomeProgramma(nome)+".json")
}

func leggiRegistro(cartellaRegistro, nome string) (Registro, bool) {
	var r Registro
	b, err := os.ReadFile(fileRegistro(cartellaRegistro, nome))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return Registro{}, false
	}
	return r, true
}

func esiste(p string) bool { _, err := os.Lstat(p); return err == nil }

// Stato dice cosa c'è sul computer per un programma.
type Stato struct {
	// Presente: c'è un programma con quel nome in una delle due cartelle, o
	// la voce lasciata dal suo installatore.
	Presente bool `json:"presente"`
	// Nostro: lo ha installato CentralHub (se ne conosce il tag).
	Nostro bool   `json:"nostro"`
	Tag    string `json:"tag"`
	// Versione letta dal programma stesso, quando si può (app di macOS).
	Versione string `json:"versione"`
	// Ambito e Percorso sono vuoti per un programma messo dal suo
	// installatore che non dice dove sta.
	Ambito     string `json:"ambito"`
	Percorso   string `json:"percorso"`
	Precedente bool   `json:"precedente"`
	// Tipo e Asset del pacchetto installato; Fissata: versione scelta
	// dall'utente, da non aggiornare con le altre.
	Tipo    string `json:"tipo"`
	Asset   string `json:"asset"`
	Fissata bool   `json:"fissata"`
}

// posti è dove sta (o starà) il programma in una cartella di installazione.
// Su macOS il nome dell'app viene dal pacchetto: qui si danno i nomi
// possibili.
func posti(cartella, nome, goos, ambito string) []string {
	n := NomeProgramma(nome)
	switch goos {
	case "linux":
		if ambito == Sistema {
			return []string{filepath.Join(cartella, n, n+".AppImage")}
		}
		return []string{filepath.Join(cartella, n+".AppImage")}
	case "windows":
		return []string{filepath.Join(cartella, n)}
	}
	trovati, _ := filepath.Glob(filepath.Join(cartella, n+"-*.app"))
	return append([]string{filepath.Join(cartella, n+".app")}, trovati...)
}

var reVersionePlist = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)

// versioneApp legge la versione di un'app di macOS dal suo Info.plist.
func versioneApp(app string) string {
	b, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	if m := reVersionePlist.FindSubmatch(b); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// LeggiStato guarda cosa c'è sul computer: prima ciò che CentralHub ha
// installato, poi un programma con quel nome nelle due cartelle (è così che
// rileva i programmi già installati a mano).
func LeggiStato(cartellaRegistro, nome, goos string, c Cartelle) Stato {
	var s Stato
	if r, ok := leggiRegistro(cartellaRegistro, nome); ok {
		tipo := r.Tipo
		if tipo == "" {
			tipo = tipoPredefinito(goos)
		}
		switch {
		case tipo == TipoInstallatore:
			// c'è finché il sistema lo elenca fra le app installate
			if r.Chiave != "" && voceElencata(r.Chiave) {
				s = Stato{Presente: true, Nostro: true, Tag: r.Tag, Tipo: tipo, Asset: r.Asset, Fissata: r.Fissata}
				if r.Percorso != "" && esiste(r.Percorso) {
					s.Percorso = r.Percorso
				}
			}
		case tipo == TipoDeb:
			// c'è finché il pacchetto risulta installato per dpkg
			if r.Chiave != "" && debInstallato(r.Chiave) {
				s = Stato{Presente: true, Nostro: true, Tag: r.Tag, Tipo: tipo, Asset: r.Asset, Fissata: r.Fissata}
			}
		case tipo == TipoPkg:
			// c'è finché il sistema ha le ricevute del pacchetto
			if r.Chiave != "" && pkgInstallato(r.Chiave) {
				s = Stato{Presente: true, Nostro: true, Tag: r.Tag, Tipo: tipo, Asset: r.Asset, Fissata: r.Fissata}
				if r.Percorso != "" && esiste(r.Percorso) {
					s.Percorso = r.Percorso
				}
			}
		case r.Percorso != "" && esiste(r.Percorso):
			s = Stato{Presente: true, Nostro: true, Tag: r.Tag, Ambito: r.Ambito, Percorso: r.Percorso, Tipo: tipo, Asset: r.Asset, Fissata: r.Fissata}
		}
	}
	if !s.Presente {
	cerca:
		for _, a := range []struct{ ambito, dir string }{{Utente, c.Utente}, {Sistema, c.Sistema}} {
			for _, p := range posti(a.dir, nome, goos, a.ambito) {
				if a.dir != "" && esiste(p) {
					s = Stato{Presente: true, Ambito: a.ambito, Percorso: p, Tipo: tipoPredefinito(goos)}
					break cerca
				}
			}
		}
	}
	if s.Percorso != "" && !DaSistema(s.Tipo) {
		s.Precedente = esiste(s.Percorso + suffissoPrecedente)
		if goos == "darwin" {
			s.Versione = versioneApp(s.Percorso)
		}
	}
	return s
}

// Richiesta di installazione.
type Richiesta struct {
	ID, Nome, Repo, Tag string
	// File è il pacchetto da installare, già scaricato.
	File string
	// Tipo del pacchetto; vuoto = quello predefinito del sistema.
	Tipo string
	// Ambito: Utente o Sistema. Non serve per un installatore.
	Ambito string
	// Fissata: la versione l'ha scelta l'utente e non è l'ultima.
	Fissata bool
	// Cartelle di installazione del sistema.
	Cartelle Cartelle
	// CartellaRegistro è dove CentralHub annota ciò che installa.
	CartellaRegistro string
	// Ctx ferma l'attesa di un installatore; nil = nessun limite.
	Ctx context.Context
	// Sistema e architettura: runtime.GOOS e runtime.GOARCH, salvo nelle prove.
	GOOS, GOARCH string
	// privilegiato esegue uno script con i permessi di amministratore;
	// sostituibile nelle prove.
	privilegiato func(goos, script string) error
}

// Esito di un'installazione riuscita.
type Esito struct {
	Percorso string
	Asset    string
	// Avvisi: cose non riuscite che non impediscono di usare il programma
	// (per esempio la voce nel menu).
	Avvisi []string
	// SeStesso: CentralHub ha aggiornato sé stesso e va riavviato.
	SeStesso bool
}

// ErrInEsecuzione: il programma da sostituire o da togliere è aperto.
type ErrInEsecuzione struct{ Nome string }

func (e *ErrInEsecuzione) Error() string {
	return e.Nome + " è aperto: chiudilo e riprova. CentralHub non chiude i programmi degli altri"
}

// percorsoProcessi è il percorso da cui partono i processi di un programma
// installato: su macOS gli eseguibili stanno dentro l'app.
func percorsoProcessi(percorso, goos string) string {
	if goos == "darwin" {
		return filepath.Join(percorso, "Contents", "MacOS") + string(filepath.Separator)
	}
	return percorso
}

// Installa installa o aggiorna il programma.
func Installa(r Richiesta) (Esito, error) {
	if r.GOOS == "" {
		r.GOOS, r.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	if r.privilegiato == nil {
		r.privilegiato = eseguiPrivilegiato
	}
	if r.Ctx == nil {
		r.Ctx = context.Background()
	}
	if r.Tipo == "" {
		r.Tipo = tipoPredefinito(r.GOOS)
	}
	if _, err := os.Stat(r.File); err != nil {
		return Esito{}, fmt.Errorf("il pacchetto da installare non c'è: %w", err)
	}
	if err := os.MkdirAll(r.CartellaRegistro, 0o700); err != nil {
		return Esito{}, err
	}
	if r.Tipo == TipoInstallatore {
		return installaConInstallatore(r)
	}
	if r.Tipo == TipoDeb {
		return installaDeb(r)
	}
	if r.Tipo == TipoPkg {
		return installaPkg(r)
	}
	var cartella string
	switch r.Ambito {
	case Utente:
		cartella = r.Cartelle.Utente
	case Sistema:
		cartella = r.Cartelle.Sistema
	default:
		return Esito{}, errors.New("va scelto dove installare: solo per te o per tutto il computer")
	}
	if cartella == "" {
		return Esito{}, errors.New("cartella di installazione non definita")
	}
	asset := filepath.Base(r.File)
	nome := NomeProgramma(r.Nome)
	esito := Esito{Asset: asset}
	reg := Registro{ID: r.ID, Nome: r.Nome, Repo: r.Repo, Tag: r.Tag, Asset: asset, Ambito: r.Ambito, Tipo: r.Tipo, Fissata: r.Fissata}
	// con i permessi di amministratore il lavoro lo fa uno script
	elevato := r.Ambito == Sistema && r.GOOS != "darwin"
	if !elevato {
		if err := os.MkdirAll(cartella, 0o755); err != nil {
			if r.Ambito == Sistema {
				return Esito{}, fmt.Errorf("in %s può scrivere solo un utente amministratore: %w", cartella, err)
			}
			return Esito{}, fmt.Errorf("la cartella di installazione %s non si può creare: %w", cartella, err)
		}
	}

	switch r.GOOS {
	case "linux":
		if !strings.HasSuffix(strings.ToLower(asset), ".appimage") {
			return Esito{}, fmt.Errorf("su Linux si installano le AppImage: %s non lo è", asset)
		}
		esito.Percorso = posti(cartella, r.Nome, "linux", r.Ambito)[0]
		esito.SeStesso = seStesso(esito.Percorso)
		if !esito.SeStesso && inEsecuzione(esito.Percorso, r.GOOS) {
			return Esito{}, &ErrInEsecuzione{r.Nome}
		}
		if elevato {
			voce, avvisi, err := installaLinuxSistema(r, esito.Percorso)
			if err != nil {
				return Esito{}, err
			}
			reg.MenuVoce, esito.Avvisi = voce, avvisi
			break
		}
		if err := installaFile(r.File, esito.Percorso); err != nil {
			return Esito{}, err
		}
	case "windows":
		esito.Percorso = filepath.Join(cartella, nome)
		esito.SeStesso = seStesso(esito.Percorso)
		if !esito.SeStesso && inEsecuzione(esito.Percorso, r.GOOS) {
			return Esito{}, &ErrInEsecuzione{r.Nome}
		}
		// la versione nuova si prepara in una cartella scrivibile
		preparazione := cartella
		if elevato {
			preparazione = filepath.Join(r.CartellaRegistro, "preparazione")
			if err := os.MkdirAll(preparazione, 0o700); err != nil {
				return Esito{}, err
			}
		}
		nuovo := filepath.Join(preparazione, "."+nome+".nuovo")
		if err := os.RemoveAll(nuovo); err != nil {
			return Esito{}, err
		}
		defer os.RemoveAll(nuovo)
		if r.Tipo == TipoEseguibile {
			// un exe solo: è il programma, e prende il suo nome
			if !strings.HasSuffix(strings.ToLower(asset), ".exe") {
				return Esito{}, fmt.Errorf("%s non è un exe", asset)
			}
			if err := os.MkdirAll(nuovo, 0o755); err != nil {
				return Esito{}, err
			}
			if err := copia(r.File, filepath.Join(nuovo, nome+".exe"), 0o755); err != nil {
				return Esito{}, err
			}
		} else if err := EstraiZip(r.File, nuovo); err != nil {
			return Esito{}, fmt.Errorf("%s: %w", asset, err)
		}
		var err error
		switch {
		case elevato && esito.SeStesso:
			err = errors.New("CentralHub installato per tutto il computer non può aggiornare sé stesso: va chiuso e aggiornato da un'altra copia")
		case elevato:
			exe, _ := EseguibileWindows(nuovo, r.Nome)
			reg.MenuVoce = filepath.Join(menuStartComune(), nome+".lnk")
			err = r.privilegiato("windows", ScriptWindowsSistema(nuovo, esito.Percorso, reg.MenuVoce, filepath.Base(exe)))
		case esito.SeStesso:
			err = scambiaContenuto(nuovo, esito.Percorso)
		default:
			err = scambia(nuovo, esito.Percorso)
		}
		if err != nil {
			return Esito{}, err
		}
	case "darwin":
		nuovo := filepath.Join(cartella, "."+nome+".nuovo")
		if err := os.RemoveAll(nuovo); err != nil {
			return Esito{}, fmt.Errorf("in %s non si può scrivere: %w", cartella, err)
		}
		defer os.RemoveAll(nuovo)
		estrai := estraiApp
		if r.Tipo == TipoDmg {
			estrai = func(file, dest string) error { return estraiDmg(file, dest, r.Nome) }
		}
		if err := estrai(r.File, nuovo); err != nil {
			return Esito{}, fmt.Errorf("%s: %w", asset, err)
		}
		app, err := filepath.Glob(filepath.Join(nuovo, "*.app"))
		if err != nil || len(app) != 1 {
			return Esito{}, fmt.Errorf("%s deve contenere una sola app, ne contiene %d", asset, len(app))
		}
		esito.Percorso = filepath.Join(cartella, filepath.Base(app[0]))
		esito.SeStesso = seStesso(esito.Percorso)
		if !esito.SeStesso && inEsecuzione(percorsoProcessi(esito.Percorso, r.GOOS), r.GOOS) {
			return Esito{}, &ErrInEsecuzione{r.Nome}
		}
		if err := scambia(app[0], esito.Percorso); err != nil {
			return Esito{}, err
		}
	default:
		return Esito{}, fmt.Errorf("installazione non prevista su %s", r.GOOS)
	}

	// La versione vecchia, messa da parte per lo scambio, si cancella. Per
	// tutto il computer l'ha già tolta lo script; quella di CentralHub che
	// aggiorna sé stesso è ancora in uso: la toglie il prossimo avvio
	// (PulisciDopoAggiornamento).
	if !elevato && !esito.SeStesso {
		if err := os.RemoveAll(esito.Percorso + suffissoPrecedente); err != nil {
			esito.Avvisi = append(esito.Avvisi, "la versione vecchia non si è potuta cancellare, resta in "+esito.Percorso+suffissoPrecedente+": "+err.Error())
		}
	}

	reg.Percorso, reg.Quando = esito.Percorso, time.Now().Format(time.RFC3339)
	// La voce di menu dell'utente solo sul sistema vero: nelle prove non si
	// scrive niente fuori dalle cartelle temporanee. Per tutto il computer
	// l'ha già creata lo script.
	if r.GOOS == runtime.GOOS && !elevato {
		voce, err := integra(r, esito.Percorso)
		reg.MenuVoce = voce
		if err != nil {
			esito.Avvisi = append(esito.Avvisi, "voce nel menu non creata: "+err.Error())
		}
	}
	scriviRegistro(r, reg, &esito)
	return esito, nil
}

func scriviRegistro(r Richiesta, reg Registro, esito *Esito) {
	b, _ := json.MarshalIndent(reg, "", "  ")
	if err := os.WriteFile(fileRegistro(r.CartellaRegistro, r.Nome), b, 0o600); err != nil {
		esito.Avvisi = append(esito.Avvisi, "versione installata non annotata: "+err.Error())
	}
}

// VoceSistema è un programma come lo elenca il sistema fra le app
// installate (su Windows: una chiave di disinstallazione del registro).
type VoceSistema struct {
	// Chiave identifica la voce: "<radice>:<nome della chiave>".
	Chiave   string
	Nome     string
	Versione string
	Data     string
	// Posizione è la cartella del programma, se l'installatore la dichiara.
	Posizione string
	// Disinstalla è il comando di disinstallazione dichiarato; MSI dice che
	// se ne occupa Windows Installer.
	Disinstalla string
	MSI         bool
}

// Ciò che parla con il sistema per gli installatori: su Windows lo mette
// voci_windows.go, nelle prove si sostituisce.
var (
	// leggiVoci elenca le app installate secondo il sistema, per chiave.
	leggiVoci = func() map[string]VoceSistema { return nil }
	// voceElencata dice se una voce c'è ancora fra le app installate (lo si
	// chiede a ogni ridisegno: su Windows senza rileggere tutto l'elenco).
	voceElencata = func(chiave string) bool { _, c := leggiVoci()[chiave]; return c }
	// avviaInstallatore lancia un installatore, aspetta che si chiuda e ne
	// restituisce il codice di uscita.
	avviaInstallatore = func(ctx context.Context, file string) (int, error) {
		return 0, errors.New("su questo sistema CentralHub non sa avviare un installatore")
	}
	// avviaDisinstallatore lancia la disinstallazione di una voce e aspetta
	// che la voce sparisca.
	avviaDisinstallatore = func(ctx context.Context, v VoceSistema) error {
		return errors.New("su questo sistema CentralHub non sa avviare un disinstallatore")
	}
	// attesaVoce: quanto si aspetta, dopo la chiusura di un installatore
	// riuscito, che la sua voce compaia (alcuni finiscono in un secondo
	// processo).
	attesaVoce = 20 * time.Second
)

// codiceRiuscito riconosce i codici di uscita di un installatore andato a
// buon fine: 0, e i due di Windows Installer che chiedono un riavvio.
func codiceRiuscito(codice int) bool { return codice == 0 || codice == 3010 || codice == 1641 }

var reSoloLettere = regexp.MustCompile(`[^a-z0-9]+`)

// somiglia dice se il nome mostrato dal sistema contiene quello del programma.
func somiglia(mostrato, nome string) bool {
	m, n := reSoloLettere.ReplaceAllString(strings.ToLower(mostrato), ""), reSoloLettere.ReplaceAllString(strings.ToLower(nome), "")
	return n != "" && strings.Contains(m, n)
}

// unaSola sceglie una voce fra più: l'unica, oppure l'unica con il nome del
// programma.
func unaSola(voci []VoceSistema, nome string) (VoceSistema, bool) {
	if len(voci) == 1 {
		return voci[0], true
	}
	var simili []VoceSistema
	for _, v := range voci {
		if somiglia(v.Nome, nome) {
			simili = append(simili, v)
		}
	}
	if len(simili) == 1 {
		return simili[0], true
	}
	return VoceSistema{}, false
}

// voceInstallata capisce, confrontando le app installate prima e dopo, quale
// voce ha lasciato un installatore: una nuova; quella già annotata (gia), se
// c'è ancora; una che è cambiata; e, se l'installatore dice di essere
// riuscito, quella che porta il nome del programma (era già installato a
// mano, nella stessa versione).
func voceInstallata(prima, dopo map[string]VoceSistema, nome, gia string, riuscito bool) (VoceSistema, bool) {
	var nuove, cambiate, simili []VoceSistema
	for k, v := range dopo {
		p, cEra := prima[k]
		switch {
		case !cEra:
			nuove = append(nuove, v)
		case p.Versione != v.Versione || p.Data != v.Data || p.Posizione != v.Posizione:
			cambiate = append(cambiate, v)
		}
		if somiglia(v.Nome, nome) {
			simili = append(simili, v)
		}
	}
	if v, ok := unaSola(nuove, nome); ok {
		return v, true
	}
	if v, ok := dopo[gia]; ok && gia != "" {
		if _, cEra := prima[gia]; riuscito || !cEra || prima[gia] != v {
			return v, true
		}
	}
	if v, ok := unaSola(cambiate, nome); ok {
		return v, true
	}
	if riuscito && len(simili) == 1 {
		return simili[0], true
	}
	return VoceSistema{}, false
}

// installaConInstallatore avvia l'installatore del programma e lo lascia
// fare: dove mettere i file e cosa chiedere lo decide lui. CentralHub
// annota la voce che ha lasciato fra le app installate, per sapere poi se il
// programma c'è ancora e come toglierlo.
func installaConInstallatore(r Richiesta) (Esito, error) {
	if r.GOOS != "windows" {
		return Esito{}, fmt.Errorf("su %s CentralHub non sa avviare un installatore", r.GOOS)
	}
	asset := filepath.Base(r.File)
	gia, _ := leggiRegistro(r.CartellaRegistro, r.Nome)
	if gia.Tipo != TipoInstallatore {
		gia = Registro{}
	}
	if gia.Percorso != "" && esiste(gia.Percorso) && inEsecuzione(gia.Percorso, r.GOOS) {
		return Esito{}, &ErrInEsecuzione{r.Nome}
	}
	prima := leggiVoci()
	codice, err := avviaInstallatore(r.Ctx, r.File)
	if err != nil {
		return Esito{}, err
	}
	riuscito := codiceRiuscito(codice)
	var voce VoceSistema
	trovata := false
	for fine := time.Now().Add(attesaVoce); ; time.Sleep(time.Second) {
		voce, trovata = voceInstallata(prima, leggiVoci(), r.Nome, gia.Chiave, riuscito)
		if trovata || !riuscito || !time.Now().Before(fine) || r.Ctx.Err() != nil {
			break
		}
	}
	switch {
	case trovata:
	case r.Ctx.Err() != nil:
		return Esito{}, r.Ctx.Err()
	case !riuscito:
		return Esito{}, fmt.Errorf("l'installatore è stato chiuso prima della fine o non è riuscito (codice %d)", codice)
	default:
		return Esito{}, errors.New("l'installatore si è chiuso, ma fra le app installate del sistema non è comparso niente: o non ha installato, o questo programma non si registra e CentralHub non può seguirlo")
	}
	esito := Esito{Asset: asset, Percorso: voce.Posizione}
	if !riuscito {
		esito.Avvisi = append(esito.Avvisi, fmt.Sprintf("l'installatore è uscito con il codice %d, ma il programma risulta installato", codice))
	}
	reg := Registro{ID: r.ID, Nome: r.Nome, Repo: r.Repo, Tag: r.Tag, Asset: asset, Tipo: TipoInstallatore, Fissata: r.Fissata,
		Percorso: voce.Posizione, Chiave: voce.Chiave, NomeMostrato: voce.Nome, Quando: time.Now().Format(time.RFC3339)}
	scriviRegistro(r, reg, &esito)
	return esito, nil
}

// installaFile mette un file solo (AppImage) al suo posto: copia accanto,
// permesso di esecuzione, scambio con una rinomina.
func installaFile(origine, bersaglio string) error {
	nuovo := filepath.Join(filepath.Dir(bersaglio), "."+filepath.Base(bersaglio)+".nuovo")
	if err := copia(origine, nuovo, 0o755); err != nil {
		os.Remove(nuovo)
		return err
	}
	if esiste(bersaglio) {
		// su Unix la rinomina sostituisce una .precedente già presente
		if err := os.Rename(bersaglio, bersaglio+suffissoPrecedente); err != nil {
			os.Remove(nuovo)
			return fmt.Errorf("la versione installata non si può mettere da parte: %w", err)
		}
	}
	if err := os.Rename(nuovo, bersaglio); err != nil {
		os.Rename(bersaglio+suffissoPrecedente, bersaglio) // si torna com'era
		os.Remove(nuovo)
		return err
	}
	return nil
}

func copia(da, a string, permessi os.FileMode) error {
	in, err := os.Open(da)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(a, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, permessi)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(a, permessi) // la umask può aver tolto dei permessi
}

// scambia mette la cartella (o l'app) nuova al posto di quella installata,
// che resta accanto come ".precedente": la cancella chi chiama, a scambio
// riuscito.
func scambia(nuovo, bersaglio string) error {
	precedente := bersaglio + suffissoPrecedente
	cEra := esiste(bersaglio)
	if cEra {
		if err := os.RemoveAll(precedente); err != nil {
			return fmt.Errorf("la copia precedente non si può togliere: %w", err)
		}
		if err := os.Rename(bersaglio, precedente); err != nil {
			return fmt.Errorf("la versione installata non si può mettere da parte (è in uso?): %w", err)
		}
	}
	if err := os.Rename(nuovo, bersaglio); err != nil {
		if cEra {
			os.Rename(precedente, bersaglio)
		}
		return err
	}
	return nil
}

// scambiaContenuto serve a CentralHub per aggiornare sé stesso su Windows:
// la cartella non si può rinominare mentre il suo exe gira, ma i singoli
// file sì. Ogni voce vecchia diventa ".precedente" e la nuova prende il suo
// posto; le vecchie le toglie il prossimo avvio.
func scambiaContenuto(nuovo, bersaglio string) error {
	voci, err := os.ReadDir(nuovo)
	if err != nil {
		return err
	}
	for _, v := range voci {
		vecchio := filepath.Join(bersaglio, v.Name())
		if esiste(vecchio) {
			if err := os.RemoveAll(vecchio + suffissoPrecedente); err != nil {
				return err
			}
			if err := os.Rename(vecchio, vecchio+suffissoPrecedente); err != nil {
				return err
			}
		}
		if err := os.Rename(filepath.Join(nuovo, v.Name()), vecchio); err != nil {
			os.Rename(vecchio+suffissoPrecedente, vecchio)
			return err
		}
	}
	return nil
}

// PulisciDopoAggiornamento toglie, all'avvio, la versione vecchia lasciata
// da CentralHub quando ha aggiornato sé stesso (finché girava non si poteva
// cancellare). Su Windows sono le copie ".precedente" accanto all'exe, e si
// agisce solo se c'è quella dell'exe stesso; su Linux l'AppImage precedente;
// su macOS l'app precedente.
func PulisciDopoAggiornamento() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	switch runtime.GOOS {
	case "windows":
		if !esiste(exe + suffissoPrecedente) {
			return
		}
		voci, _ := os.ReadDir(filepath.Dir(exe))
		for _, v := range voci {
			if strings.HasSuffix(v.Name(), suffissoPrecedente) {
				os.RemoveAll(filepath.Join(filepath.Dir(exe), v.Name()))
			}
		}
	case "linux":
		if a := os.Getenv("APPIMAGE"); strings.HasSuffix(strings.ToLower(a), ".appimage") {
			os.Remove(a + suffissoPrecedente)
		}
	case "darwin":
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i > 0 {
			os.RemoveAll(exe[:i+len(".app")] + suffissoPrecedente)
		}
	}
}

// EstraiZip estrae uno zip in una cartella nuova. Se tutto il contenuto sta
// in un'unica cartella (com'è negli zip per Windows dei progetti), quella
// cartella viene tolta di mezzo. Niente percorsi che escono dalla
// destinazione, niente collegamenti.
func EstraiZip(percorso, dest string) error { return estraiZip(percorso, dest, true) }

func estraiZip(percorso, dest string, togliCima bool) error {
	z, err := zip.OpenReader(percorso)
	if err != nil {
		return fmt.Errorf("zip non leggibile: %w", err)
	}
	defer z.Close()
	if len(z.File) == 0 {
		return errors.New("zip vuoto")
	}
	// c'è un'unica cartella in cima?
	cima, unica := "", togliCima
	for _, f := range z.File {
		if !unica {
			break
		}
		n := strings.TrimLeft(filepath.ToSlash(f.Name), "/")
		primo, _, haResto := strings.Cut(n, "/")
		if (!haResto && !f.FileInfo().IsDir()) || (cima != "" && cima != primo) {
			unica = false // un file al primo livello, o più cartelle
			break
		}
		cima = primo
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	radice := filepath.Clean(dest) + string(filepath.Separator)
	for _, f := range z.File {
		n := strings.TrimLeft(filepath.ToSlash(f.Name), "/")
		if unica {
			_, n, _ = strings.Cut(n, "/")
		}
		if n == "" {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(n)) {
			return fmt.Errorf("lo zip contiene un percorso non ammesso: %s", f.Name)
		}
		bersaglio := filepath.Join(dest, filepath.FromSlash(n))
		if !strings.HasPrefix(bersaglio, radice) {
			return fmt.Errorf("lo zip contiene un percorso non ammesso: %s", f.Name)
		}
		modo := f.Mode()
		switch {
		case modo.IsDir():
			if err := os.MkdirAll(bersaglio, 0o755); err != nil {
				return err
			}
		case modo.IsRegular():
			if err := os.MkdirAll(filepath.Dir(bersaglio), 0o755); err != nil {
				return err
			}
			in, err := f.Open()
			if err != nil {
				return err
			}
			p := os.FileMode(0o644)
			if modo&0o111 != 0 {
				p = 0o755
			}
			out, err := os.OpenFile(bersaglio, os.O_WRONLY|os.O_CREATE|os.O_EXCL, p)
			if err != nil {
				in.Close()
				return err
			}
			_, err = io.Copy(out, in)
			in.Close()
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return fmt.Errorf("zip danneggiato (%s): %w", f.Name, err)
			}
		default:
			// collegamenti e file speciali: non servono in un exe portable
		}
	}
	return nil
}

// estraiApp estrae lo zip di un'app per macOS. Su macOS lo fa `ditto`, che
// conserva permessi, collegamenti e firma; altrove (solo nelle prove) lo zip
// di Go.
func estraiApp(percorso, dest string) error {
	if runtime.GOOS != "darwin" {
		return estraiZip(percorso, dest, false)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", percorso, dest).CombinedOutput(); err != nil {
		return fmt.Errorf("zip non estraibile: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
