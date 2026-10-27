// SPDX-License-Identifier: MIT

// Package autoverifica controlla, all'avvio, che il programma sia integro e
// pubblicato da chi dice di averlo pubblicato.
//
// La catena di fiducia parte dalla firma che chi pubblica mette a ogni release
// (scripts/firma-release.sh) e arriva ai file che l'utente ha sul disco:
//
//	chiave pubblica incorporata (internal/firma)
//	  └─ verifica SHA256SUMS.txt.sig  →  SHA256SUMS.txt (firmato)
//	       └─ elenca INTEGRITA-<sistema>.txt  (con il suo SHA-256)
//	            └─ elenca lo SHA-256 di ogni file del pacchetto, eseguibile compreso
//
// Senza rete, o con un repository privato, resta il confronto con l'elenco
// INTEGRITA.txt che il pacchetto porta con sé: dice che i file sono quelli
// confezionati, non da chi.
//
// Cosa può dare e cosa no: un programma che controlla sé stesso può essere
// aggirato da chi lo ricompila (toglie il controllo o cambia la chiave).
// Protegge da download corrotti o troncati, antivirus che mutila un file,
// risorse sostituite, pacchetti ricaricati altrove con file cambiati; mostra
// «firmato da Trismegisto» oppure «non confermato». Non protegge da un
// eseguibile sostituito con uno che non controlla niente: per quello restano la
// firma verificata prima di installare (CentralHub) e quella del sistema
// operativo.
//
// Il pacchetto usa solo la libreria standard ed è identico in ogni progetto
// (l'unica cosa del progetto è Configurazione). Non scrive niente nella cartella
// del programma e non manda dati: le uniche richieste sono tre file pubblici
// della release, una volta per versione.
package autoverifica

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"centralhub/internal/firma"
)

// Esiti possibili.
const (
	// EsitoIntegroFirmato: i file coincidono con l'elenco della release e
	// l'elenco è coperto dalla firma di chi pubblica.
	EsitoIntegroFirmato = "integro_firmato"
	// EsitoIntegro: i file coincidono con l'elenco del pacchetto, ma la firma
	// non si è potuta confermare (il motivo è nel titolo).
	EsitoIntegro = "integro"
	// EsitoDiversi: almeno un file è diverso da quello originale o manca.
	EsitoDiversi = "diversi"
	// EsitoFirmaNonValida: la release ha una firma che non corrisponde a
	// nessuna chiave fidata, o un elenco non coperto dalla firma.
	EsitoFirmaNonValida = "firma_non_valida"
	// EsitoNonVerificabile: niente da confrontare (versione di sviluppo).
	EsitoNonVerificabile = "non_verificabile"
)

// Nomi dei file.
const (
	// NomeElenco è l'elenco che il pacchetto porta con sé, alla radice del programma.
	NomeElenco = "INTEGRITA.txt"
	// NomeFileStato è dove l'app scrive l'ultimo esito, nella cartella dei dati.
	NomeFileStato = "autoverifica-stato.json"
	// nomeCache è la catena già verificata per una versione, nella cartella dei dati.
	nomeCache = "autoverifica-cache.json"
	nomeSomme = "SHA256SUMS.txt"
)

// NomeElencoRelease è il nome, fra gli asset della release, dell'elenco
// completo dei file di un sistema («macos-arm64», «linux-amd64»…).
func NomeElencoRelease(sistema string) string { return "INTEGRITA-" + sistema + ".txt" }

// SistemaCorrente è il sistema su cui gira il programma, come compare nei
// nomi degli asset: macos-arm64, macos-amd64, linux-amd64, windows-amd64.
func SistemaCorrente() string {
	sis := runtime.GOOS
	if sis == "darwin" {
		sis = "macos"
	}
	return sis + "-" + runtime.GOARCH
}

// Stato è l'esito della verifica, pronto per l'interfaccia.
type Stato struct {
	Esito       string   `json:"esito"`
	Titolo      string   `json:"titolo"`
	Dettaglio   string   `json:"dettaglio,omitempty"`
	Controllati int      `json:"controllati"`
	Diversi     []string `json:"diversi,omitempty"`
	Mancano     []string `json:"mancano,omitempty"`
}

// Configurazione è l'unica cosa che cambia da un progetto all'altro.
type Configurazione struct {
	// Versione senza la «v» (la release è il tag «v<Versione>»).
	Versione string
	// Repo è «proprietario/repository» delle release pubbliche. Vuoto = non
	// toccare la rete (repository privato): resta il confronto locale.
	Repo string
	// Sistema, se vuoto, è SistemaCorrente().
	Sistema string
	// Radice è la cartella del programma. Se vuota, RadiceProgramma().
	Radice string
	// CartellaDati è dove tenere la cache della catena verificata. Vuota = niente cache.
	CartellaDati string
	// Chiavi fidate. Se vuote, quelle incorporate (firma.Fidate()).
	Chiavi []ed25519.PublicKey
	// URLBase dei download, di norma «https://github.com» (le prove usano un server locale).
	URLBase string
	// Client HTTP; se nil, uno con un tetto di 8 secondi.
	Client *http.Client
	// Avanzamento, se non nil, riceve l'esito del confronto locale appena è
	// pronto, prima del controllo della firma in rete (che con una rete lenta
	// può durare): l'app lo usa per mostrare e scrivere subito lo stato.
	Avanzamento func(Stato)
}

func (c Configurazione) conDefault() Configurazione {
	if c.Sistema == "" {
		c.Sistema = SistemaCorrente()
	}
	if c.Radice == "" {
		c.Radice = RadiceProgramma()
	}
	if len(c.Chiavi) == 0 {
		c.Chiavi = firma.Fidate()
	}
	if c.URLBase == "" {
		c.URLBase = "https://github.com"
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 8 * time.Second}
	}
	return c
}

// RadiceProgramma è la cartella rispetto alla quale sono scritti i percorsi
// dell'elenco: il bundle «.app» su macOS, la AppDir dell'AppImage su Linux, la
// cartella dell'eseguibile su Windows.
func RadiceProgramma() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return radiceDi(exe, runtime.GOOS, os.Getenv("APPDIR"))
}

func radiceDi(exe, goos, appdir string) string {
	dir := filepath.Dir(exe)
	switch goos {
	case "darwin":
		// <radice>.app/Contents/MacOS/<eseguibile>
		for p := dir; p != filepath.Dir(p); p = filepath.Dir(p) {
			if strings.HasSuffix(strings.ToLower(p), ".app") {
				return p
			}
		}
	case "linux":
		// AppImage: $APPDIR, oppure <AppDir>/usr/bin/<eseguibile>
		if appdir != "" && esiste(filepath.Join(appdir, NomeElenco)) {
			return appdir
		}
		if filepath.Base(dir) == "bin" {
			if cand := filepath.Dir(filepath.Dir(dir)); esiste(filepath.Join(cand, NomeElenco)) {
				return cand
			}
		}
	}
	return dir
}

// ubicazioniElenco: dove il pacchetto tiene INTEGRITA.txt rispetto alla radice.
// Nel bundle macOS sta fra le risorse (sigillate dalla firma del sistema), non
// alla radice; i percorsi dell'elenco restano comunque relativi alla radice.
var ubicazioniElenco = []string{NomeElenco, "Contents/Resources/" + NomeElenco}

func leggiElencoDelPacchetto(radice string) ([]byte, error) {
	var ultimo error
	for _, u := range ubicazioniElenco {
		b, err := os.ReadFile(filepath.Join(radice, filepath.FromSlash(u)))
		if err == nil {
			return b, nil
		}
		ultimo = err
	}
	return nil, ultimo
}

func esiste(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// voce è una riga dell'elenco: lo SHA-256 e il percorso relativo alla radice.
type voce struct{ Somma, Percorso string }

// leggiElenco legge righe «<sha256>  <percorso>». I percorsi sono relativi,
// con la barra «/», senza «..»: un elenco che esce dalla radice è rifiutato.
func leggiElenco(dati []byte) ([]voce, error) {
	var out []voce
	visti := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(dati))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		riga := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(riga) == "" || strings.HasPrefix(riga, "#") {
			continue
		}
		if len(riga) < 67 || riga[64] != ' ' || riga[65] != ' ' {
			return nil, fmt.Errorf("riga %d: formato non valido", n)
		}
		somma, p := strings.ToLower(riga[:64]), riga[66:]
		if _, err := hex.DecodeString(somma); err != nil {
			return nil, fmt.Errorf("riga %d: somma non valida", n)
		}
		if !percorsoSicuro(p) {
			return nil, fmt.Errorf("riga %d: percorso non ammesso «%s»", n, p)
		}
		if visti[p] {
			return nil, fmt.Errorf("riga %d: «%s» compare due volte", n, p)
		}
		visti[p] = true
		out = append(out, voce{somma, p})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("elenco vuoto")
	}
	return out, nil
}

func percorsoSicuro(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, e := range strings.Split(p, "/") {
		if e == ".." || e == "." || e == "" {
			return false
		}
	}
	return true
}

func sommaFile(percorso string) (string, error) {
	f, err := os.Open(percorso)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// confronta calcola lo SHA-256 dei file su disco e li confronta con l'elenco.
func confronta(radice string, voci []voce) (diversi, mancano []string) {
	for _, v := range voci {
		s, err := sommaFile(filepath.Join(radice, filepath.FromSlash(v.Percorso)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			mancano = append(mancano, v.Percorso)
		case err != nil || s != v.Somma:
			diversi = append(diversi, v.Percorso)
		}
	}
	sort.Strings(diversi)
	sort.Strings(mancano)
	return
}

// Verifica esegue il controllo. Non fallisce mai: ogni caso è uno Stato.
func Verifica(ctx context.Context, c Configurazione) Stato {
	c = c.conDefault()
	dati, err := leggiElencoDelPacchetto(c.Radice)
	if err != nil {
		return Stato{
			Esito:     EsitoNonVerificabile,
			Titolo:    "Non verificabile (versione di sviluppo o pacchetto non confezionato)",
			Dettaglio: "Questa copia non porta l'elenco dei file originali (" + NomeElenco + "): non c'è nulla con cui confrontarla.",
		}
	}
	locale, err := leggiElenco(dati)
	if err != nil {
		return Stato{
			Esito:     EsitoNonVerificabile,
			Titolo:    "Non verificabile (elenco dei file illeggibile)",
			Dettaglio: NomeElenco + ": " + err.Error() + ". Riscarica il programma dalla pagina delle release.",
		}
	}
	if d, m := confronta(c.Radice, locale); len(d)+len(m) > 0 {
		return statoDiversi(len(locale), d, m)
	}

	if c.Repo == "" {
		return statoIntegro(len(locale), "repository privato",
			"I file coincidono con l'elenco del pacchetto. Il repository delle release è privato: non c'è una firma pubblica da scaricare. Se il programma è installato con CentralHub, la firma la verifica CentralHub.")
	}

	if c.Avanzamento != nil {
		c.Avanzamento(statoIntegro(len(locale), "verifica della firma in corso",
			"I file coincidono con l'elenco del pacchetto; si sta controllando la firma della release."))
	}
	elenco, err := c.catenaVerificata(ctx)
	switch {
	case err == nil:
	case errors.Is(err, errSenzaRete):
		return statoIntegro(len(locale), "nessuna rete",
			"I file coincidono con l'elenco del pacchetto; la firma della release si confermerà al prossimo avvio con la rete.")
	case errors.Is(err, errNonFirmata):
		return statoIntegro(len(locale), "release non firmata",
			"I file coincidono con l'elenco del pacchetto, ma la release non risulta firmata da chi pubblica ("+strings.TrimPrefix(err.Error(), errNonFirmata.Error()+": ")+").")
	default:
		return Stato{
			Esito:       EsitoFirmaNonValida,
			Titolo:      "Firma della release non valida",
			Dettaglio:   err.Error() + ". Questa copia non è confermata come pubblicata da Trismegisto: riscaricala dalla pagina ufficiale delle release.",
			Controllati: len(locale),
		}
	}
	if d, m := confronta(c.Radice, elenco); len(d)+len(m) > 0 {
		return statoDiversi(len(elenco), d, m)
	}
	return Stato{
		Esito:       EsitoIntegroFirmato,
		Titolo:      "Integro e firmato da Trismegisto",
		Dettaglio:   fmt.Sprintf("%d file coincidono con l'elenco firmato della release v%s.", len(elenco), c.Versione),
		Controllati: len(elenco),
	}
}

func statoIntegro(n int, motivo, dettaglio string) Stato {
	return Stato{Esito: EsitoIntegro, Titolo: "Integro, firma non confermata (" + motivo + ")", Dettaglio: dettaglio, Controllati: n}
}

func statoDiversi(n int, diversi, mancano []string) Stato {
	var parti []string
	if len(diversi) > 0 {
		parti = append(parti, "cambiati: "+elencoBreve(diversi))
	}
	if len(mancano) > 0 {
		parti = append(parti, "mancanti: "+elencoBreve(mancano))
	}
	return Stato{
		Esito:       EsitoDiversi,
		Titolo:      "File diversi dall'originale",
		Dettaglio:   strings.Join(parti, "; ") + ". Riscarica il programma dalla pagina delle release.",
		Controllati: n,
		Diversi:     diversi,
		Mancano:     mancano,
	}
}

func elencoBreve(v []string) string {
	if len(v) <= 4 {
		return strings.Join(v, ", ")
	}
	return strings.Join(v[:4], ", ") + fmt.Sprintf(" e altri %d", len(v)-4)
}

// Errori della catena: distinguono ciò che non è un problema (niente rete,
// release non firmata) da ciò che lo è (firma sbagliata).
var (
	errSenzaRete  = errors.New("nessuna rete")
	errNonFirmata = errors.New("release non firmata")
)

func nonFirmata(formato string, a ...any) error {
	return fmt.Errorf("%w: "+formato, append([]any{errNonFirmata}, a...)...)
}

type cache struct {
	Versione string `json:"versione"`
	Sistema  string `json:"sistema"`
	Repo     string `json:"repo"`
	Somma    string `json:"somma"`
	Elenco   string `json:"elenco"`
}

func (c Configurazione) leggiCache() ([]voce, bool) {
	if c.CartellaDati == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(c.CartellaDati, nomeCache))
	if err != nil {
		return nil, false
	}
	var k cache
	if json.Unmarshal(b, &k) != nil || k.Versione != c.Versione || k.Sistema != c.Sistema || !strings.EqualFold(k.Repo, c.Repo) {
		return nil, false
	}
	if somma(k.Elenco) != k.Somma {
		return nil, false
	}
	voci, err := leggiElenco([]byte(k.Elenco))
	return voci, err == nil
}

func (c Configurazione) scriviCache(elenco []byte) {
	if c.CartellaDati == "" {
		return
	}
	b, err := json.Marshal(cache{Versione: c.Versione, Sistema: c.Sistema, Repo: c.Repo, Somma: somma(string(elenco)), Elenco: string(elenco)})
	if err != nil {
		return
	}
	_ = scriviAtomico(filepath.Join(c.CartellaDati, nomeCache), b)
}

func somma(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// catenaVerificata restituisce l'elenco completo dei file della release,
// dopo aver verificato la firma. Se questa versione è già stata verificata,
// usa la cache e non tocca la rete.
func (c Configurazione) catenaVerificata(ctx context.Context) ([]voce, error) {
	if voci, ok := c.leggiCache(); ok {
		return voci, nil
	}
	tag := "v" + c.Versione
	base := strings.TrimRight(c.URLBase, "/") + "/" + c.Repo + "/releases/download/" + tag + "/"
	somme, err := c.scarica(ctx, base+nomeSomme)
	if err != nil {
		return nil, err
	}
	sig, err := c.scarica(ctx, base+firma.NomeFile)
	if err != nil {
		return nil, err
	}
	if !firma.Riconosce(string(sig)) {
		return nil, nonFirmata("%s non è una firma di questo formato", firma.NomeFile)
	}
	if err := firma.Verifica(c.Repo, tag, somme, string(sig), c.Chiavi); err != nil {
		return nil, err
	}
	nome := NomeElencoRelease(c.Sistema)
	atteso, err := sommaDi(somme, nome)
	if err != nil {
		return nil, err
	}
	elenco, err := c.scarica(ctx, base+nome)
	if err != nil {
		if errors.Is(err, errNonFirmata) {
			return nil, fmt.Errorf("la release firmata non ha %s", nome)
		}
		return nil, err
	}
	if somma(string(elenco)) != atteso {
		return nil, fmt.Errorf("%s non è quello coperto dalla firma", nome)
	}
	voci, err := leggiElenco(elenco)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nome, err)
	}
	c.scriviCache(elenco)
	return voci, nil
}

// sommaDi cerca nelle righe di SHA256SUMS.txt lo SHA-256 del file `nome`.
func sommaDi(somme []byte, nome string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(somme))
	for sc.Scan() {
		riga := strings.TrimRight(sc.Text(), "\r")
		if len(riga) > 66 && riga[64] == ' ' && strings.TrimLeft(riga[65:], " *") == nome {
			return strings.ToLower(riga[:64]), nil
		}
	}
	return "", fmt.Errorf("la firma non copre %s", nome)
}

const tettoDownload = 8 << 20

// scarica legge un file pubblico. 404 e simili = la release non c'è o non è
// raggiungibile (errNonFirmata); errori di rete e del server = errSenzaRete.
func (c Configurazione) scarica(ctx context.Context, url string) ([]byte, error) {
	ctx, annulla := context.WithTimeout(ctx, 8*time.Second)
	defer annulla()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errSenzaRete
	}
	req.Header.Set("User-Agent", "autoverifica")
	r, err := c.Client.Do(req)
	if err != nil {
		return nil, errSenzaRete
	}
	defer r.Body.Close()
	switch {
	case r.StatusCode == http.StatusOK:
	case r.StatusCode == http.StatusNotFound, r.StatusCode == http.StatusForbidden, r.StatusCode == http.StatusGone:
		return nil, nonFirmata("%s non raggiungibile (HTTP %d)", path.Base(url), r.StatusCode)
	default:
		return nil, errSenzaRete
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, tettoDownload+1))
	if err != nil || len(b) > tettoDownload {
		return nil, errSenzaRete
	}
	return b, nil
}

// ScriviStato salva l'ultimo esito nella cartella dei dati (per diagnosi e
// per le prove del pacchetto, che non vedono la finestra).
func ScriviStato(cartella string, s Stato) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return scriviAtomico(filepath.Join(cartella, NomeFileStato), append(b, '\n'))
}

func scriviAtomico(percorso string, dati []byte) error {
	if err := os.MkdirAll(filepath.Dir(percorso), 0o755); err != nil {
		return err
	}
	tmp := percorso + ".tmp"
	if err := os.WriteFile(tmp, dati, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, percorso)
}
