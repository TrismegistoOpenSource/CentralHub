// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"centralhub/internal/config"
	"centralhub/internal/github"
	"centralhub/internal/installa"
	"centralhub/internal/integrita"
	"centralhub/internal/scarica"
)

// Le due modalità del programma.
const (
	modoArchivio = "archivio" // scarica tutti i pacchetti di ogni release in una cartella master
	modoUpdate   = "update"   // installa e aggiorna i programmi
)

// App è ciò che l'interfaccia può chiamare. Qui non c'è logica propria: si
// mettono in fila configurazione, GitHub, download e installazione.
type App struct {
	ctx context.Context

	mu       sync.Mutex
	cfg      config.Config
	esiti    map[string]config.Esito
	cartelle installa.Cartelle
	lavori   map[string]*lavoro         // per ID
	ultime   map[string]*github.Release // ultima release vista (si ricorda anche fra una sessione e l'altra: ultime.json)
	visteIl  map[string]time.Time       // quando l'ultimo controllo è andato a buon fine
	prese    map[string]time.Time       // quando l'elenco dei pacchetti è stato chiesto all'API
	errori   map[string]string          // errore dell'ultimo controllo
	elenchi  map[string]elenco          // le release fra cui scegliere, viste da poco
	integri  map[string]integrita.Esito // com'è ogni programma installato rispetto alle somme annotate
	// basiDati: dove si cercano le impostazioni di un programma da disinstallare
	basiDati []string

	// esito del controllo che CentralHub fa su sé stesso all'avvio (autoverifica.go)
	autocontrollo autocontrollo

	// sostituibili nelle prove
	nuovoCli func(token string) *github.Client
	cestina  func(goos, percorso string) error // nil = il cestino del sistema
}

// elenco sono le release di un repository, con il momento in cui sono state
// chieste a GitHub.
type elenco struct {
	release []github.Release
	quando  time.Time
}

type lavoro struct {
	modo    string
	fase    string // controllo | download | installazione | installatore | disinstallazione
	annulla context.CancelFunc
	file    string
	fatti   int64
	totale  int64
}

// variabileAPI sposta l'indirizzo dell'API di GitHub, ma solo in modalità di
// prova (configurazione spostata): cmd/githubfinto. Con la configurazione
// vera i token vanno sempre e solo a GitHub.
const variabileAPI = "CENTRALHUB_API"

// variabileCartelleVere: in modalità di prova i programmi si installano in
// due cartelle dentro la configurazione di prova; con questa variabile
// impostata si usano quelle vere del sistema (serve a provare menu, permessi
// di amministratore e collegamenti su un computer vero).
const variabileCartelleVere = "CENTRALHUB_CARTELLE_VERE"

// NuovaApp prepara l'app.
func NuovaApp() *App {
	return &App{
		esiti:   map[string]config.Esito{},
		lavori:  map[string]*lavoro{},
		ultime:  map[string]*github.Release{},
		visteIl: map[string]time.Time{},
		prese:   map[string]time.Time{},
		errori:  map[string]string{},
		elenchi: map[string]elenco{},
		integri: map[string]integrita.Esito{},
		nuovoCli: func(token string) *github.Client {
			c := github.Nuovo(token, "CentralHub/"+Versione)
			if _, isolata, _ := config.Cartella(); isolata {
				if base := strings.TrimRight(os.Getenv(variabileAPI), "/"); base != "" {
					c.Base = base
				}
			}
			return c
		},
	}
}

func (a *App) avvio(ctx context.Context) {
	a.ctx = ctx
	a.carica()
}

// carica legge configurazione ed esiti. Separata da avvio per le prove.
func (a *App) carica() {
	installa.PulisciDopoAggiornamento()
	cfg, err := config.Carica()
	cartelle, errC := installa.CartellePredefinite(runtime.GOOS)
	basiDati := installa.BasiDati(runtime.GOOS)
	// In modalità di prova non si installa nelle cartelle vere del computer,
	// salvo richiesta esplicita (le prove sul sistema vero); e le
	// impostazioni dei programmi da disinstallare non si cercano mai fra
	// quelle vere dell'utente, ma in una cartella della prova.
	if d, isolata, _ := config.Cartella(); isolata && os.Getenv(variabileCartelleVere) == "" {
		cartelle = installa.Cartelle{Utente: filepath.Join(d, "prova-utente"), Sistema: filepath.Join(d, "prova-sistema")}
		basiDati = []string{filepath.Join(d, "prova-dati")}
	}
	a.mu.Lock()
	a.cfg, a.cartelle, a.basiDati = cfg, cartelle, basiDati
	a.esiti = config.CaricaEsiti()
	delete(a.esiti, "@controllo") // della 0.2.1: il controllo ora è gratis per i pubblici
	a.ritrovaEsitiVecchi()
	a.caricaUltime()
	a.mu.Unlock()
	if err != nil {
		a.log("Configurazione non letta: " + err.Error())
	}
	if errC != nil {
		a.log(errC.Error())
	}
	if d, e := a.cartellaLavoro(); e == nil {
		os.RemoveAll(d) // pacchetti scaricati per un'installazione non finita
	}
}

func (a *App) chiusura(context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range a.lavori {
		l.annulla()
	}
}

// cartellaRegistro è dove si annota ciò che CentralHub installa.
func (a *App) cartellaRegistro() (string, error) {
	d, _, err := config.Cartella()
	return filepath.Join(d, "installati"), err
}

// cartellaLavoro è dove la modalità Update scarica il pacchetto da installare.
func (a *App) cartellaLavoro() (string, error) {
	d, _, err := config.Cartella()
	return filepath.Join(d, "scaricati"), err
}

// ---------------------------------------------------------------- avvisi

func (a *App) emetti(evento string, dati ...interface{}) {
	if a.ctx != nil {
		wr.EventsEmit(a.ctx, evento, dati...)
	}
}

func (a *App) log(riga string) {
	a.emetti("log", time.Now().Format("15:04:05")+"  "+riga)
}

// ---------------------------------------------------------------- voci

// voce è un repository come lo vede il programma, con la sua chiave.
type voce struct {
	ID, Nome, Repo, Asset string
	Privato               bool
	PretendiFirma         bool
	config.Locale
	token string
}

// senzaChiave: il repository è segnato come privato e la chiave non c'è
// (importato da un file esportato senza chiavi, o ancora da aggiungere).
// Si prova lo stesso, senza chiave: se GitHub lo mostra il repository è
// diventato pubblico (nonPiuPrivato); se no, manca la chiave.
func (v voce) senzaChiave() bool { return v.Privato && v.token == "" }

var errMancaChiave = errors.New("manca la chiave di accesso: senza, GitHub non mostra questo repository. Se è privato, aggiungila da «Modifica»")

// siSpiegaConLaChiave dice se l'errore di GitHub per un repository senza
// chiave è quello di un repository che non si vede: lo si spiega con la
// chiave che manca, non con un 404.
func siSpiegaConLaChiave(v voce, err error) bool {
	var ge *github.Errore
	return v.senzaChiave() && errors.As(err, &ge) && ge.Tipo == "nessuna-release"
}

// nonPiuPrivato toglie la spunta «privato» a un repository che GitHub ha
// mostrato senza chiave: è diventato pubblico. Va chiamata con il lucchetto.
func (a *App) nonPiuPrivato(v voce) {
	nuovo := a.cfg
	nuovo.Repo = append([]config.Repo(nil), a.cfg.Repo...)
	for i := range nuovo.Repo {
		if nuovo.Repo[i].ID == v.ID && nuovo.Repo[i].Privato && nuovo.Repo[i].Token == "" {
			nuovo.Repo[i].Privato = false
			if err := config.Salva(nuovo); err != nil {
				return
			}
			a.cfg = nuovo
			go a.log(v.Nome + ": ora è pubblico, non serve più la chiave")
		}
	}
}

// ritrovaEsitiVecchi riporta sotto il nuovo ID gli esiti dei repository che
// la 0.1.0 teneva incorporati nel programma. Va chiamata con il lucchetto.
func (a *App) ritrovaEsitiVecchi() {
	cambiato := false
	for _, r := range a.cfg.Repo {
		if r.ID != config.IDDaRepo(r.Repo) {
			continue
		}
		for _, modo := range []string{modoArchivio, modoUpdate} {
			vecchia, nuova := config.ChiaveEsitoVecchia(r.Repo)+"|"+modo, r.ID+"|"+modo
			if e, ok := a.esiti[vecchia]; ok {
				if _, c := a.esiti[nuova]; !c {
					a.esiti[nuova] = e
				}
				delete(a.esiti, vecchia)
				cambiato = true
			}
		}
	}
	if cambiato {
		_ = config.SalvaEsiti(a.esiti)
	}
}

// voci va chiamata con il lucchetto preso, nell'ordine della configurazione.
func (a *App) voci() []voce {
	var out []voce
	for _, r := range a.cfg.Repo {
		out = append(out, voce{ID: r.ID, Nome: r.Nome, Repo: r.Repo, Asset: r.Asset, Privato: r.Privato, PretendiFirma: r.PretendiFirma, Locale: r.Locale, token: r.Token})
	}
	return out
}

func (a *App) voce(id string) (voce, bool) {
	for _, v := range a.voci() {
		if v.ID == id {
			return v, true
		}
	}
	return voce{}, false
}

// Vista è un repository come lo mostra l'interfaccia. Il token non c'è mai.
type Vista struct {
	ID    string `json:"id"`
	Nome  string `json:"nome"`
	Repo  string `json:"repo"`
	Asset string `json:"asset"`
	// Privato: il repository richiede una chiave. ConChiave: ce l'ha.
	// MancaChiave: è privato e la chiave non c'è. La chiave stessa non arriva
	// mai all'interfaccia.
	Privato       bool `json:"privato"`
	PretendiFirma bool `json:"pretendiFirma"`
	// FirmaRichiesta: per questo repository una release non firmata si
	// rifiuta (interruttore acceso, o proprietario che firma).
	FirmaRichiesta   bool   `json:"firmaRichiesta"`
	ConChiave        bool   `json:"conChiave"`
	MancaChiave      bool   `json:"manca"`
	Tag              string `json:"tag"`
	CartellaArchivio string `json:"cartellaArchivio"`
	Aggiorna         bool   `json:"aggiorna"`
	Ambito           string `json:"ambito"`

	UltimaRelease string `json:"ultimaRelease"` // "" = non ancora controllata
	NomeRelease   string `json:"nomeRelease"`
	// Integrita: "" = non verificata, "ok" = i file sono quelli annotati
	// all'installazione, "diversa" = qualcuno è cambiato o sparito (i primi
	// nomi in IntegritaFile).
	Integrita     string   `json:"integrita"`
	IntegritaFile []string `json:"integritaFile"`
	HaNote        bool     `json:"haNote"`  // la release ha un testo da mostrare
	VistaIl       string   `json:"vistaIl"` // quando è stata controllata (RFC 3339)
	Errore        string   `json:"errore"`

	// Archiviazione
	Archiviata string `json:"archiviata"` // la più recente nella cartella master
	// ArchivioAggiornato e UpdateAggiornato: "si", "no", oppure "" se non si
	// è potuto controllare (o se la modalità non riguarda questo repository).
	ArchivioAggiornato string        `json:"archivioAggiornato"`
	EsitoArchivio      *config.Esito `json:"esitoArchivio"`

	// Update
	// Installabile: "si", "no" (la release non ha un pacchetto per questo
	// sistema) oppure "" se non si sa ancora.
	Installabile string `json:"installabile"`
	// Scelta: l'ultima release ha più pacchetti adatti a questo sistema e
	// CentralHub non sa decidere da sé quale (il programma non è installato,
	// o nessuno è l'erede evidente di quello installato): va chiesto.
	Scelta           bool           `json:"scelta"`
	Installato       installa.Stato `json:"installato"`
	UpdateAggiornato string         `json:"updateAggiornato"`
	EsitoUpdate      *config.Esito  `json:"esitoUpdate"`

	Modo   string `json:"modo"` // la modalità del lavoro in corso
	Fase   string `json:"fase"`
	File   string `json:"file"`
	Fatti  int64  `json:"fatti"`
	Totale int64  `json:"totale"`
}

func nomiAsset(r *github.Release) []string {
	nomi := make([]string, len(r.Assets))
	for i, as := range r.Assets {
		nomi[i] = as.Nome
	}
	return nomi
}

// stessaVersione confronta un tag ("v2.0.1") con una versione letta dal
// programma ("2.0.1").
func stessaVersione(tag, versione string) bool {
	return versione != "" && strings.TrimPrefix(strings.ToLower(tag), "v") == strings.TrimPrefix(strings.ToLower(versione), "v")
}

// vista va chiamata con il lucchetto preso.
func (a *App) vista(v voce) Vista {
	reg, _ := a.cartellaRegistro()
	o := Vista{ID: v.ID, Nome: v.Nome, Repo: v.Repo, Asset: v.Asset, Privato: v.Privato, PretendiFirma: v.PretendiFirma, FirmaRichiesta: v.PretendiFirma || proprietarioFirmato(v.Repo), ConChiave: v.token != "", MancaChiave: v.senzaChiave(),
		Tag: v.Tag, CartellaArchivio: v.CartellaArchivio, Aggiorna: v.Aggiorna, Ambito: v.Ambito, Errore: a.errori[v.ID]}
	if v.CartellaArchivio != "" {
		if s := scarica.Scaricate(v.CartellaArchivio); len(s) > 0 {
			o.Archiviata = s[0]
		}
	}
	o.Installato = installa.LeggiStato(reg, v.Nome, runtime.GOOS, a.cartelle)
	if i, ok := a.integri[v.ID]; ok && o.Installato.Presente {
		o.Integrita = "ok"
		if !i.Integro() {
			o.Integrita = "diversa"
			o.IntegritaFile = append(append([]string{}, i.Diversi...), i.Mancano...)
			if len(o.IntegritaFile) > 8 {
				o.IntegritaFile = o.IntegritaFile[:8]
			}
		}
	}
	if e, ok := a.esiti[v.ID+"|"+modoArchivio]; ok {
		o.EsitoArchivio = &e
	}
	if e, ok := a.esiti[v.ID+"|"+modoUpdate]; ok {
		o.EsitoUpdate = &e
	}
	if r := a.ultime[v.ID]; r != nil {
		o.UltimaRelease, o.NomeRelease = r.Tag, r.Nome
		o.HaNote = strings.TrimSpace(r.Note) != ""
		if q := a.visteIl[v.ID]; !q.IsZero() {
			o.VistaIl = q.Format(time.RFC3339)
		}
		if v.CartellaArchivio != "" {
			o.ArchivioAggiornato = "no"
			if scarica.Presente(v.CartellaArchivio, r.Tag) {
				o.ArchivioAggiornato = "si"
			}
		}
		if c, err := installa.Candidati(nomiAsset(r), runtime.GOOS, runtime.GOARCH, v.Asset); err == nil && len(c) > 0 {
			o.Installabile = "si"
			var installato installa.Pacchetto
			if o.Installato.Presente {
				installato = installa.Pacchetto{Nome: o.Installato.Asset, Tipo: o.Installato.Tipo}
			}
			if _, err := installa.Scegli(c, installa.Pacchetto{}, installato); err != nil {
				o.Scelta = true
			}
			if o.Installato.Presente || v.Aggiorna {
				o.UpdateAggiornato = "no"
				if (o.Installato.Nostro && o.Installato.Tag == r.Tag) || stessaVersione(r.Tag, o.Installato.Versione) {
					o.UpdateAggiornato = "si"
				}
			}
		} else {
			o.Installabile = "no"
		}
	}
	if l := a.lavori[v.ID]; l != nil {
		o.Modo, o.Fase, o.File, o.Fatti, o.Totale = l.modo, l.fase, l.file, l.fatti, l.totale
	}
	return o
}

// avvisa manda all'interfaccia la vista aggiornata di un repository.
func (a *App) avvisa(id string) {
	a.mu.Lock()
	v, ok := a.voce(id)
	var o Vista
	if ok {
		o = a.vista(v)
	}
	a.mu.Unlock()
	if ok {
		a.emetti("repo", o)
	}
}

// Panoramica è lo stato completo all'apertura.
type Panoramica struct {
	Versione   string            `json:"versione"`
	Sistema    string            `json:"sistema"`
	Repo       []Vista           `json:"repo"`
	Tag        []string          `json:"tag"`
	FileConfig string            `json:"fileConfig"`
	Cartelle   installa.Cartelle `json:"cartelle"`
	Prova      bool              `json:"prova"`  // configurazione spostata con la variabile d'ambiente
	Chiavi     int               `json:"chiavi"` // quanti repository hanno una chiave salvata
}

// Stato restituisce tutto ciò che serve a disegnare la finestra, senza
// toccare la rete.
func (a *App) Stato() Panoramica {
	a.mu.Lock()
	defer a.mu.Unlock()
	file, _ := config.PercorsoFile()
	_, isolata, _ := config.Cartella()
	p := Panoramica{Versione: Versione, Sistema: runtime.GOOS, FileConfig: file, Cartelle: a.cartelle, Prova: isolata, Chiavi: a.cfg.Chiavi(), Repo: []Vista{}, Tag: []string{}}
	visti := map[string]bool{}
	for _, v := range a.voci() {
		p.Repo = append(p.Repo, a.vista(v))
		if v.Tag != "" && !visti[strings.ToLower(v.Tag)] {
			visti[strings.ToLower(v.Tag)] = true
			p.Tag = append(p.Tag, v.Tag)
		}
	}
	sort.Slice(p.Tag, func(i, j int) bool { return strings.ToLower(p.Tag[i]) < strings.ToLower(p.Tag[j]) })
	return p
}

// ---------------------------------------------------------------- repository

func (a *App) occupato(id string) error {
	if a.lavori[id] != nil {
		return errors.New("il repository sta lavorando: aspetta che finisca")
	}
	return nil
}

// SalvaRepo aggiunge un repository (ID vuoto) o ne modifica uno. Per un
// repository privato la chiave nuova sta in r.Token; se è vuota resta quella
// già salvata. Togliere la spunta «privato» toglie anche la chiave.
func (a *App) SalvaRepo(r config.Repo) (Vista, error) {
	if err := r.Normalizza(); err != nil {
		return Vista{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.occupato(r.ID); err != nil {
		return Vista{}, err
	}
	for _, v := range a.voci() {
		if v.ID == r.ID {
			continue
		}
		if strings.EqualFold(v.Repo, r.Repo) {
			return Vista{}, fmt.Errorf("%s c'è già nella lista, come %q", v.Repo, v.Nome)
		}
		if strings.EqualFold(installa.NomeProgramma(v.Nome), installa.NomeProgramma(r.Nome)) {
			return Vista{}, fmt.Errorf("c'è già un repository che si chiama %q: i due programmi finirebbero nello stesso posto", v.Nome)
		}
	}
	nuovo := a.cfg
	nuovo.Repo = append([]config.Repo(nil), a.cfg.Repo...)
	trovato := false
	for i := range nuovo.Repo {
		if nuovo.Repo[i].ID == r.ID && r.ID != "" {
			vecchio := nuovo.Repo[i]
			// senza una chiave nuova resta quella salvata, ma solo per lo
			// stesso repository
			if r.Privato && r.Token == "" && strings.EqualFold(vecchio.Repo, r.Repo) {
				r.Token = vecchio.Token
			}
			if vecchio.Repo != r.Repo || vecchio.Token != r.Token {
				// ciò che si è visto era di un altro repository o con un'altra chiave
				delete(a.ultime, r.ID)
				delete(a.visteIl, r.ID)
				delete(a.elenchi, r.ID)
				delete(a.errori, r.ID)
			}
			nuovo.Repo[i], trovato = r, true
		}
	}
	if !trovato {
		r.ID = config.NuovoID()
		nuovo.Repo = append(nuovo.Repo, r)
	}
	if err := config.Salva(nuovo); err != nil {
		return Vista{}, fmt.Errorf("configurazione non salvata: %w", err)
	}
	a.cfg = nuovo
	v, _ := a.voce(r.ID)
	return a.vista(v), nil
}

// ImpostaLocale salva ciò che di un repository riguarda questo computer:
// tag, cartella d'archivio, se tenerlo aggiornato e dove installarlo.
func (a *App) ImpostaLocale(id string, l config.Locale) (Vista, error) {
	if err := l.Normalizza(); err != nil {
		return Vista{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.occupato(id); err != nil {
		return Vista{}, err
	}
	v, ok := a.voce(id)
	if !ok {
		return Vista{}, errors.New("repository non trovato")
	}
	if err := a.salvaLocale(v, l); err != nil {
		return Vista{}, err
	}
	v, _ = a.voce(id)
	return a.vista(v), nil
}

// localeSalvato restituisce le impostazioni locali di un repository come
// stanno nella configurazione. Va chiamata con il lucchetto preso.
func (a *App) localeSalvato(v voce) config.Locale {
	for _, r := range a.cfg.Repo {
		if r.ID == v.ID {
			return r.Locale
		}
	}
	return v.Locale
}

// salvaLocale scrive nella configurazione le impostazioni locali di un
// repository. Va chiamata con il lucchetto preso.
func (a *App) salvaLocale(v voce, l config.Locale) error {
	nuovo := a.cfg
	nuovo.Repo = append([]config.Repo(nil), a.cfg.Repo...)
	for i := range nuovo.Repo {
		if nuovo.Repo[i].ID == v.ID {
			nuovo.Repo[i].Locale = l
		}
	}
	if err := config.Salva(nuovo); err != nil {
		return fmt.Errorf("configurazione non salvata: %w", err)
	}
	a.cfg = nuovo
	return nil
}

// RimuoviRepo toglie un repository dalla lista, con la sua chiave. Non
// cancella niente dal disco: le release archiviate e il programma installato
// restano dove sono.
func (a *App) RimuoviRepo(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.occupato(id); err != nil {
		return err
	}
	nuovo := a.cfg
	nuovo.Repo = nil
	for _, r := range a.cfg.Repo {
		if r.ID != id {
			nuovo.Repo = append(nuovo.Repo, r)
		}
	}
	if err := config.Salva(nuovo); err != nil {
		return fmt.Errorf("configurazione non salvata: %w", err)
	}
	a.cfg = nuovo
	delete(a.esiti, id+"|"+modoArchivio)
	delete(a.esiti, id+"|"+modoUpdate)
	delete(a.ultime, id)
	delete(a.elenchi, id)
	delete(a.errori, id)
	_ = config.SalvaEsiti(a.esiti)
	return nil
}

// ScegliCartella apre la finestra di sistema per scegliere una cartella.
func (a *App) ScegliCartella(titolo, partenza string) (string, error) {
	o := wr.OpenDialogOptions{Title: titolo, CanCreateDirectories: true}
	if fi, err := os.Stat(partenza); err == nil && fi.IsDir() {
		o.DefaultDirectory = partenza
	}
	return wr.OpenDirectoryDialog(a.ctx, o)
}

// ApriCartella mostra una cartella nel gestore dei file.
func (a *App) ApriCartella(percorso string) error {
	if fi, err := os.Stat(percorso); err != nil || !fi.IsDir() {
		return fmt.Errorf("la cartella %s non esiste ancora", percorso)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("/usr/bin/open", percorso)
	case "windows":
		cmd = exec.Command("explorer.exe", percorso)
	default:
		cmd = exec.Command("xdg-open", percorso)
	}
	return cmd.Start()
}

// Esporta scrive la configurazione in un file scelto dall'utente: i
// repository, i tag e le impostazioni. Le chiavi di accesso dei repository
// privati ci finiscono solo con conChiavi; senza, il file ha tutto il resto e
// chi lo importa vede quali repository gli chiedono una chiave.
func (a *App) Esporta(conChiavi bool) (string, error) {
	percorso, err := wr.SaveFileDialog(a.ctx, wr.SaveDialogOptions{
		Title: "Esporta la configurazione", DefaultFilename: nomeEsportazione(time.Now(), conChiavi),
		Filters: []wr.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || percorso == "" {
		return "", err
	}
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	return percorso, config.Esporta(percorso, cfg, conChiavi)
}

// nomeEsportazione è il nome proposto per il file esportato: porta la data e
// dice se dentro ci sono le chiavi, così due esportazioni non si confondono.
func nomeEsportazione(quando time.Time, conChiavi bool) string {
	chiavi := "senza-chiavi"
	if conChiavi {
		chiavi = "con-chiavi"
	}
	return "CentralHub-configurazione-" + quando.Format("2006-01-02") + "-" + chiavi + ".json"
}

// Unione dice cosa ha fatto un'importazione.
type Unione struct {
	Aggiunti    int `json:"aggiunti"`
	Aggiornati  int `json:"aggiornati"`
	Saltati     int `json:"saltati"`     // un altro repository ha già lo stesso nome
	SenzaChiave int `json:"senzaChiave"` // privati che nel file non avevano la chiave e qui non ce l'hanno
}

// Importa aggiunge alla lista i repository di un file scelto dall'utente. Non
// toglie niente: quelli già presenti si completano con le impostazioni del
// file, e la chiave locale resta se il file non ne porta una. Della
// configurazione attuale resta la copia config.json.bak. Ritorna nil se
// l'utente ha chiuso la finestra senza scegliere.
func (a *App) Importa() (*Unione, error) {
	percorso, err := wr.OpenFileDialog(a.ctx, wr.OpenDialogOptions{
		Title:   "Importa una configurazione",
		Filters: []wr.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || percorso == "" {
		return nil, err
	}
	nuova, err := config.Importa(percorso)
	if err != nil {
		return nil, err
	}
	return a.importa(nuova)
}

// completa porta nel repository già in lista ciò che dice il file: i valori
// vuoti del file non cancellano quelli di qui.
func completa(r config.Repo, f config.Repo) config.Repo {
	if f.Nome != "" {
		r.Nome = f.Nome
	}
	if f.Asset != "" {
		r.Asset = f.Asset
	}
	r.Privato = r.Privato || f.Privato
	r.PretendiFirma = r.PretendiFirma || f.PretendiFirma
	if f.Token != "" {
		r.Token = f.Token
	}
	if f.Tag != "" {
		r.Tag = f.Tag
	}
	if f.CartellaArchivio != "" {
		r.CartellaArchivio = f.CartellaArchivio
	}
	if f.Ambito != "" {
		r.Ambito = f.Ambito
	}
	r.Aggiorna = r.Aggiorna || f.Aggiorna
	return r
}

func (a *App) importa(nuova config.Config) (*Unione, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.lavori) > 0 {
		return nil, errors.New("c'è un lavoro in corso: aspetta che finisca")
	}
	cfg := a.cfg
	cfg.Repo = append([]config.Repo(nil), a.cfg.Repo...)
	var u Unione
	for _, f := range nuova.Repo {
		trovato := -1
		for i, r := range cfg.Repo {
			if strings.EqualFold(r.Repo, f.Repo) {
				trovato = i
			}
		}
		if trovato >= 0 {
			cfg.Repo[trovato] = completa(cfg.Repo[trovato], f)
			u.Aggiornati++
		} else {
			// due repository con lo stesso nome finirebbero nello stesso posto
			doppio := false
			for _, r := range cfg.Repo {
				doppio = doppio || strings.EqualFold(installa.NomeProgramma(r.Nome), installa.NomeProgramma(f.Nome))
			}
			if doppio {
				u.Saltati++
				continue
			}
			for _, r := range cfg.Repo {
				if r.ID == f.ID {
					f.ID = config.NuovoID()
				}
			}
			cfg.Repo = append(cfg.Repo, f)
			u.Aggiunti++
			trovato = len(cfg.Repo) - 1
		}
		if r := cfg.Repo[trovato]; r.Privato && r.Token == "" {
			u.SenzaChiave++
		}
	}
	if err := config.Salva(cfg); err != nil {
		return nil, fmt.Errorf("configurazione non salvata: %w", err)
	}
	a.cfg = cfg
	a.ultime, a.visteIl, a.errori = map[string]*github.Release{}, map[string]time.Time{}, map[string]string{}
	a.elenchi = map[string]elenco{}
	return &u, nil
}

// ---------------------------------------------------------------- lavori

// inizia segna il repository come occupato. Falso se lo è già.
func (a *App) inizia(id, modo string) (context.Context, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lavori[id] != nil {
		return nil, false
	}
	padre := a.ctx
	if padre == nil {
		padre = context.Background()
	}
	ctx, annulla := context.WithCancel(padre)
	a.lavori[id] = &lavoro{modo: modo, fase: "controllo", annulla: annulla}
	return ctx, true
}

func (a *App) fase(id, fase string) {
	a.mu.Lock()
	if l := a.lavori[id]; l != nil {
		l.fase, l.file, l.fatti, l.totale = fase, "", 0, 0
	}
	a.mu.Unlock()
	a.avvisa(id)
}

func (a *App) finisci(id string) {
	a.mu.Lock()
	if l := a.lavori[id]; l != nil {
		l.annulla()
		delete(a.lavori, id)
	}
	a.mu.Unlock()
	a.avvisa(id)
}

// Annulla ferma il lavoro in corso di un repository.
func (a *App) Annulla(id string) {
	a.mu.Lock()
	if l := a.lavori[id]; l != nil {
		l.annulla()
	}
	a.mu.Unlock()
}

// validitaElenco: per quanto tempo, fra un cambio di tag e l'altro, ci si
// fida dell'elenco dei pacchetti già chiesto all'API. Il tag lo dice il sito
// gratis a ogni controllo; l'elenco (con i digest) si richiede se il tag
// cambia o se è più vecchio di così, perché i file di una release possono
// essere ricaricati senza cambiare il tag.
const validitaElenco = 24 * time.Hour

// scorciatoia: un controllo fatto da meno di così si riusa nel lavoro che
// segue (la finestra di scelta e poi l'installazione), invece di rifarlo.
const scorciatoia = 30 * time.Second

// gratis dice se il controllo di questo repository non consuma richieste
// dell'API: sì per un pubblico senza chiave, il cui ultimo tag si legge dal
// sito. Un privato senza chiave si prova anonimo e dà un 404: costa.
func (a *App) gratis(v voce) bool {
	return !v.senzaChiave() && a.nuovoCli(v.token).Web() != ""
}

// controlla guarda qual è l'ultima release di un repository e la ricorda.
// Per un pubblico senza chiave chiede prima il tag al sito (gratis): se è
// quello già noto e l'elenco dei pacchetti è recente, non chiede altro. Negli
// altri casi, o se il sito non risponde, chiede l'ultima release all'API: ogni
// repository usa la propria chiave; un privato senza chiave si prova lo
// stesso, anonimo (se è diventato pubblico funziona).
func (a *App) controlla(ctx context.Context, v voce) (*github.Release, error) {
	return a.controllaConTasto(ctx, v, false)
}

// controllaDaTasto è il controllo dei tasti «Controlla» (quello della scheda e
// quello generale): come controlla, ma una release ricordata senza firma, in un
// repository in cui la firma è attesa, si rilegge da GitHub anche se l'elenco
// è recente. Chi pubblica carica la firma dopo il run di Actions: un controllo
// fatto in mezzo ricorda la release senza SHA256SUMS.txt.sig e per 24 ore la
// riusa, mentre su GitHub la firma ormai c'è. Si rifanno solo i repository in
// questo caso: gli altri restano sull'elenco in cache.
func (a *App) controllaDaTasto(ctx context.Context, v voce) (*github.Release, error) {
	return a.controllaConTasto(ctx, v, true)
}

// firmaDaRiverificare dice se una release ricordata potrebbe essere stata
// firmata dopo che la si è guardata: non ha il file di firma e per quel
// repository la firma è attesa.
func firmaDaRiverificare(v voce, r *github.Release) bool {
	if r == nil {
		return false
	}
	if _, c := r.FileFirma(); c {
		return false
	}
	quando, _ := time.Parse(time.RFC3339, r.Pubblicata)
	return firmaAttesa(v, quando)
}

func (a *App) controllaConTasto(ctx context.Context, v voce, daTasto bool) (*github.Release, error) {
	cli := a.nuovoCli(v.token)
	if cli.Web() != "" && !v.senzaChiave() {
		if tag, err := cli.TagPubblico(ctx, v.Repo); err == nil {
			a.mu.Lock()
			noto, preso := a.ultime[v.ID], a.prese[v.ID]
			if noto != nil && noto.Tag == tag && time.Since(preso) < validitaElenco && !(daTasto && firmaDaRiverificare(v, noto)) && cli.IndirizziLeciti(v.Repo, noto) {
				a.visteIl[v.ID] = time.Now()
				delete(a.errori, v.ID)
				a.salvaUltime()
				a.mu.Unlock()
				return noto, nil
			}
			a.mu.Unlock()
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	r, err := cli.Ultima(ctx, v.Repo)
	if siSpiegaConLaChiave(v, err) {
		err = errMancaChiave
	}
	a.mu.Lock()
	if err == nil && v.senzaChiave() {
		a.nonPiuPrivato(v)
	}
	if err != nil {
		if ctx.Err() == nil {
			a.errori[v.ID] = err.Error()
		}
	} else {
		if len(r.Note) > maxNote {
			r.Note = r.Note[:maxNote]
		}
		prima := a.ultime[v.ID]
		a.ultime[v.ID] = r
		a.visteIl[v.ID] = time.Now()
		a.prese[v.ID] = time.Now()
		delete(a.errori, v.ID)
		a.salvaUltime()
		if prima != nil && prima.Tag == r.Tag && firmaDaRiverificare(v, prima) && !firmaDaRiverificare(v, r) {
			a.firmaComparsa(v, r)
		}
	}
	a.mu.Unlock()
	return r, err
}

// firmaComparsa: la firma di una release è stata caricata dopo l'ultima volta
// che la si era guardata. Un lavoro fallito per «manca la firma» non è più
// vero: il suo esito si toglie, così la scheda non resta rossa per una
// ragione che non c'è più. Va chiamata con il lucchetto.
func (a *App) firmaComparsa(v voce, r *github.Release) {
	tolto := false
	for _, modo := range []string{modoUpdate, modoArchivio} {
		k := v.ID + "|" + modo
		if e, ok := a.esiti[k]; ok && !e.OK && e.Tag == r.Tag && strings.Contains(e.Messaggio, testoMancaFirma) {
			delete(a.esiti, k)
			tolto = true
		}
	}
	if tolto {
		_ = config.SalvaEsiti(a.esiti)
	}
	go a.log(v.Nome + ": la firma di " + r.Tag + " è stata pubblicata dopo l'ultimo controllo")
}

// ultima restituisce l'ultima release per un lavoro (installare, aggiornare,
// archiviare): prima controlla quel repository, e solo se l'ha già fatto da
// meno di mezzo minuto (la finestra di scelta e poi il clic) riusa l'esito.
func (a *App) ultima(ctx context.Context, v voce) (*github.Release, error) {
	a.mu.Lock()
	r, quando := a.ultime[v.ID], a.visteIl[v.ID]
	a.mu.Unlock()
	// una release ricordata con indirizzi fuori posto (ultime.json alterato) non si riusa
	if r != nil && time.Since(quando) < scorciatoia && a.nuovoCli(v.token).IndirizziLeciti(v.Repo, r) {
		return r, nil
	}
	return a.controlla(ctx, v)
}

// Controlla guarda se c'è una release nuova per un repository. È il tasto
// «Controlla» (anche quello generale, ControllaTutti lo chiama per ognuno):
// rilegge da GitHub anche le release ricordate senza firma (controllaDaTasto).
func (a *App) Controlla(id string) {
	a.avviaControllo(id, true)
}

// avviaControllo lancia il controllo di un repository. daTasto dice se l'ha
// chiesto l'utente (controllaDaTasto) o se è il controllo automatico
// dell'apertura (controlla).
func (a *App) avviaControllo(id string, daTasto bool) {
	a.mu.Lock()
	v, ok := a.voce(id)
	a.mu.Unlock()
	if !ok {
		return
	}
	ctx, libero := a.inizia(id, "")
	if !libero {
		return
	}
	a.avvisa(id)
	go func() {
		defer a.finisci(id)
		controllo := a.controlla
		if daTasto {
			controllo = a.controllaDaTasto
		}
		if _, err := controllo(ctx, v); err != nil && ctx.Err() == nil {
			a.log(v.Nome + ": " + err.Error())
		}
	}()
}

// intervalloApertura: all'apertura i repository che per essere controllati
// consumano richieste dell'API (quelli con una chiave, o che il sito non sa
// dire) si controllano solo se l'ultimo controllo è più vecchio di così. I
// pubblici si controllano sempre: il sito non conta.
const intervalloApertura = time.Hour

// ControllaAllApertura è il controllo che si fa aprendo l'app. Restituisce
// se è partito qualcosa. «Controlla» chiama invece ControllaTutti, che
// controlla tutto sempre.
func (a *App) ControllaAllApertura() bool {
	a.mu.Lock()
	var scelti []voce
	for _, v := range a.voci() {
		if a.gratis(v) {
			scelti = append(scelti, v)
			continue
		}
		// un orologio spostato indietro non deve bloccare i controlli
		if d := time.Since(a.visteIl[v.ID]); a.visteIl[v.ID].IsZero() || d < 0 || d >= intervalloApertura {
			scelti = append(scelti, v)
		}
	}
	a.mu.Unlock()
	for _, v := range scelti {
		a.avviaControllo(v.ID, false)
	}
	return len(scelti) > 0
}

// ControllaTutti lo fa per tutti i repository, senza guardare quando è stato
// fatto l'ultima volta.
func (a *App) ControllaTutti() {
	a.mu.Lock()
	voci := a.voci()
	a.mu.Unlock()
	for _, v := range voci {
		a.Controlla(v.ID)
	}
}

func (a *App) annota(id, modo string, ok bool, tag, messaggio string) {
	a.mu.Lock()
	a.esiti[id+"|"+modo] = config.Esito{OK: ok, Messaggio: messaggio, Tag: tag, Quando: time.Now().Format(time.RFC3339)}
	_ = config.SalvaEsiti(a.esiti)
	a.mu.Unlock()
}

// avanzamento restituisce la funzione che riceve i byte scaricati: aggiorna
// lo stato e avvisa l'interfaccia al massimo ogni 150 ms.
func (a *App) avanzamento(id string) scarica.Avanzamento {
	var ultimo time.Time
	return func(file string, fatti, totale int64) {
		a.mu.Lock()
		if l := a.lavori[id]; l != nil {
			l.file, l.fatti, l.totale = file, fatti, totale
		}
		a.mu.Unlock()
		if time.Since(ultimo) > 150*time.Millisecond || fatti == totale {
			ultimo = time.Now()
			a.avvisa(id)
		}
	}
}

// uno avvia un lavoro su un repository, se è libero.
func (a *App) uno(id, modo string, lavoro func(ctx context.Context, v voce)) error {
	a.mu.Lock()
	v, ok := a.voce(id)
	a.mu.Unlock()
	if !ok {
		return errors.New("repository non trovato")
	}
	ctx, libero := a.inizia(id, modo)
	if !libero {
		return errors.New("il repository sta già lavorando")
	}
	a.avvisa(id)
	go func() {
		defer a.finisci(id)
		lavoro(ctx, v)
	}()
	return nil
}

// tutti fa lo stesso lavoro su più repository, uno alla volta.
func (a *App) tutti(modo string, scelti func(Vista) bool, lavoro func(ctx context.Context, v voce)) {
	a.mu.Lock()
	var voci []voce
	for _, v := range a.voci() {
		if scelti(a.vista(v)) {
			voci = append(voci, v)
		}
	}
	a.mu.Unlock()
	go func() {
		for _, v := range voci {
			ctx, libero := a.inizia(v.ID, modo)
			if !libero {
				continue
			}
			a.avvisa(v.ID)
			lavoro(ctx, v)
			a.finisci(v.ID)
			if a.ctx != nil && a.ctx.Err() != nil {
				return
			}
		}
	}()
}

func (a *App) fallito(ctx context.Context, v voce, modo, tag string, err error) {
	// l'errore di questo lavoro è più preciso di quello, vecchio, di un
	// controllo (per esempio «GitHub non raggiungibile» prima di
	// un'installazione dall'archivio che poi non torna): la scheda mostra
	// il primo; il secondo ricompare al prossimo controllo, se c'è ancora
	a.mu.Lock()
	if a.errori[v.ID] != err.Error() {
		delete(a.errori, v.ID)
	}
	a.mu.Unlock()
	if ctx.Err() != nil {
		a.log(v.Nome + ": interrotto")
		a.annota(v.ID, modo, false, tag, "Interrotto")
		return
	}
	a.log(v.Nome + ": " + err.Error())
	a.annota(v.ID, modo, false, tag, err.Error())
}

// ---------------------------------------------------------------- archiviazione

// Archivia scarica l'ultima release di un repository nella sua cartella
// master: una sottocartella con il nome della release, dentro ogni pacchetto
// come file separato. Una release già scaricata si salta.
func (a *App) Archivia(id string) error {
	return a.uno(id, modoArchivio, a.archivia)
}

// ArchiviaTutti lo fa per tutti i repository che hanno una cartella d'archivio.
func (a *App) ArchiviaTutti() {
	a.tutti(modoArchivio, func(v Vista) bool { return v.CartellaArchivio != "" }, a.archivia)
}

func (a *App) archivia(ctx context.Context, v voce) {
	if v.CartellaArchivio == "" {
		a.fallito(ctx, v, modoArchivio, "", errors.New("non è stata scelta la cartella in cui archiviare"))
		return
	}
	r, err := a.ultima(ctx, v)
	if err != nil {
		a.fallito(ctx, v, modoArchivio, "", err)
		return
	}
	// i file senza digest di GitHub si verificano con SHA256SUMS.txt, se la
	// release lo pubblica; senza, si archiviano lo stesso (nessun obbligo:
	// l'archivio non installa niente)
	// la firma si controlla sempre: una release firmata male, o che doveva
	// esserlo e non lo è, non si archivia
	fr, err := a.verificaRelease(ctx, v, r)
	if err != nil {
		a.fallito(ctx, v, modoArchivio, r.Tag, err)
		return
	}
	if fr.Firmata() {
		r = fr.Release
		a.log(v.Nome + ": firma di SHA256SUMS.txt valida")
	} else if somme, err := a.nuovoCli(v.token).Somme(ctx, v.Repo, r); err != nil {
		a.log(v.Nome + ": SHA256SUMS.txt non si è letto (" + err.Error() + "): i file senza digest non saranno verificati")
	} else if somme != nil {
		r = r.ConSomme(somme)
	}
	a.fase(v.ID, "download")
	ris, err := scarica.Scarica(ctx, a.nuovoCli(v.token), v.Repo, r, v.CartellaArchivio, a.avanzamento(v.ID))
	if err != nil {
		if n := scarica.TenutiDaParte(v.CartellaArchivio, r.Tag); n > 0 && ctx.Err() == nil {
			err = fmt.Errorf("%w. I %d file già scaricati sono tenuti da parte: riprovando si riparte da lì", err, n)
		}
		a.fallito(ctx, v, modoArchivio, r.Tag, err)
		return
	}
	messaggio := fmt.Sprintf("Release %s archiviata (%d file)", r.Tag, len(ris.File))
	if ris.GiaPresente {
		messaggio = "Release " + r.Tag + " già archiviata"
		// archiviata prima che la si firmasse: Scarica non la rifà, e senza la
		// firma l'installazione dall'archivio si rifiuta
		if fr.Firmata() {
			scritti, err := scarica.CompletaFirma(v.CartellaArchivio, r.Tag, fr.Grezzo, fr.Firma)
			if err != nil {
				a.fallito(ctx, v, modoArchivio, r.Tag, fmt.Errorf("la release è nell'archivio ma non si è potuta completare con la firma: %w", err))
				return
			}
			if len(scritti) > 0 {
				messaggio += "; aggiunta la firma (" + strings.Join(scritti, ", ") + ")"
			}
		}
	} else {
		messaggio += fmt.Sprintf(", %d pacchetti verificati con SHA-256", len(ris.Verificati))
		if n := len(ris.NonVerificati); n > 0 {
			messaggio += fmt.Sprintf(", %d senza somma pubblicata", n)
			a.log(v.Nome + ": non verificabili (la release non pubblica la loro somma): " + strings.Join(ris.NonVerificati, ", "))
		}
	}
	a.log(v.Nome + ": " + messaggio + " in " + ris.Cartella)
	a.annota(v.ID, modoArchivio, true, r.Tag, messaggio)
}

// ---------------------------------------------------------------- update

// Opzioni di un'installazione: ciò che l'utente ha scelto nella finestra.
// Tutte vuote = l'ultima release, con il pacchetto che decide CentralHub.
type Opzioni struct {
	// Tag della release da installare; vuoto = l'ultima.
	Tag string `json:"tag"`
	// Asset e Tipo del pacchetto scelto; vuoti = lo decide CentralHub
	// (l'unico adatto, o quello dello stesso genere di ciò che è installato).
	Asset string `json:"asset"`
	Tipo  string `json:"tipo"`
	// Da dice da dove prendere il pacchetto: "" o "github" = dal repository,
	// "archivio" = dalla cartella d'archivio del repository (Tag allora è il
	// nome della sua sottocartella).
	Da string `json:"da"`
	// Sostituisci va messo a vero solo dopo la conferma dell'utente, quando
	// sul computer c'è un programma con quel nome che non ha installato
	// CentralHub.
	Sostituisci bool `json:"sostituisci"`
}

// SceltaRelease è una release fra cui scegliere, con i pacchetti che su
// questo sistema si possono installare.
type SceltaRelease struct {
	Tag        string               `json:"tag"`
	Nome       string               `json:"nome"`
	Pubblicata string               `json:"pubblicata"`
	Prerelease bool                 `json:"prerelease"`
	Ultima     bool                 `json:"ultima"`
	Pacchetti  []installa.Pacchetto `json:"pacchetti"`
}

// Scelte è ciò fra cui l'utente sceglie prima di installare.
type Scelte struct {
	Release []SceltaRelease `json:"release"`
	// Archivio: le release già nella cartella d'archivio che hanno un
	// pacchetto per questo sistema, dalla più recente. Vuoto se il
	// repository non ha un archivio, o non ha niente di installabile.
	Archivio []SceltaRelease `json:"archivio"`
	// Avviso spiega perché l'elenco è ridotto alla sola ultima release.
	Avviso string `json:"avviso"`
}

// elenco restituisce le release di un repository fra cui scegliere: quelle
// viste da meno di due minuti si riusano (le richieste a GitHub sono contate).
func (a *App) elenco(ctx context.Context, v voce) ([]github.Release, error) {
	a.mu.Lock()
	e, ok := a.elenchi[v.ID]
	a.mu.Unlock()
	if ok && time.Since(e.quando) < 2*time.Minute {
		return e.release, nil
	}
	r, err := a.nuovoCli(v.token).Elenco(ctx, v.Repo)
	if err != nil {
		if siSpiegaConLaChiave(v, err) {
			err = errMancaChiave
		}
		return nil, err
	}
	a.mu.Lock()
	a.elenchi[v.ID] = elenco{r, time.Now()}
	a.mu.Unlock()
	return r, nil
}

// release trova la release con un certo tag.
func (a *App) release(ctx context.Context, v voce, tag string) (*github.Release, error) {
	tutte, err := a.elenco(ctx, v)
	if err != nil {
		return nil, err
	}
	for i := range tutte {
		if tutte[i].Tag == tag {
			return &tutte[i], nil
		}
	}
	return nil, fmt.Errorf("la release %s non c'è (più) fra quelle pubblicate", tag)
}

func (a *App) pacchetti(v voce, r *github.Release) []installa.Pacchetto {
	c, err := installa.Candidati(nomiAsset(r), runtime.GOOS, runtime.GOARCH, v.Asset)
	if err != nil {
		return nil
	}
	for i := range c {
		for _, as := range r.Assets {
			if as.Nome == c[i].Nome {
				c[i].Dimensione = as.Dimensione
			}
		}
	}
	return c
}

// Scelte chiede a GitHub le release di un repository e dice, per ognuna,
// quali pacchetti si possono installare su questo sistema: è ciò che la
// finestra di installazione fa scegliere (quale versione, quale pacchetto).
// Le release senza niente di installabile non compaiono.
func (a *App) Scelte(id string) (Scelte, error) {
	a.mu.Lock()
	v, ok := a.voce(id)
	a.mu.Unlock()
	if !ok {
		return Scelte{}, errors.New("repository non trovato")
	}
	padre := a.ctx
	if padre == nil {
		padre = context.Background()
	}
	ctx, annulla := context.WithTimeout(padre, time.Minute)
	defer annulla()
	arch := a.scelteArchivio(v)
	ultima, err := a.ultima(ctx, v)
	if err != nil {
		if len(arch) == 0 {
			return Scelte{}, err
		}
		// Release vuoto e non nil: nel JSON nil diventa null e l'interfaccia lo scorre
		return Scelte{Release: []SceltaRelease{}, Archivio: arch, Avviso: "GitHub non risponde (" + err.Error() + "): si può installare dall'archivio."}, nil
	}
	s := Scelte{Release: []SceltaRelease{}, Archivio: arch}
	tutte, err := a.elenco(ctx, v)
	if err != nil {
		// l'ultima si conosce già: si può installare almeno quella
		tutte = nil
		s.Avviso = "Non ho potuto leggere l'elenco delle release (" + err.Error() + "): si può installare solo l'ultima."
	}
	vista := false
	for i := range tutte {
		vista = vista || tutte[i].Tag == ultima.Tag
	}
	if !vista {
		tutte = append([]github.Release{*ultima}, tutte...)
	}
	for i := range tutte {
		r := &tutte[i]
		if p := a.pacchetti(v, r); len(p) > 0 {
			s.Release = append(s.Release, SceltaRelease{Tag: r.Tag, Nome: r.Nome, Pubblicata: r.Pubblicata, Prerelease: r.Prerelease,
				Ultima: r.Tag == ultima.Tag, Pacchetti: p})
		}
	}
	if len(s.Release) == 0 && len(arch) == 0 {
		return Scelte{}, installa.ErrNessunAsset
	}
	a.avvisa(id)
	return s, nil
}

// Aggiorna installa il programma di un repository, o lo aggiorna se c'è già:
// prende la release scelta (l'ultima, se non è indicata), scarica il
// pacchetto scelto o quello adatto a questo sistema e lo installa.
func (a *App) Aggiorna(id string, o Opzioni) error {
	return a.uno(id, modoUpdate, func(ctx context.Context, v voce) { a.aggiorna(ctx, v, o) })
}

// AggiornaTutti lo fa per i programmi già installati e per quelli seguiti.
// I programmi messi a mano non vengono sostituiti: per quelli serve la
// conferma, uno per uno.
func (a *App) AggiornaTutti() {
	a.tutti(modoUpdate, daAggiornare, func(ctx context.Context, v voce) { a.aggiorna(ctx, v, Opzioni{}) })
}

// daAggiornare dice se «Aggiorna tutti» lavora su un programma: quelli già
// installati (tranne quelli fermi su una versione scelta dall'utente, e
// quelli per cui va chiesto quale pacchetto) e quelli seguiti. Un programma seguito ma non ancora installato entra solo
// se non c'è più niente da chiedere: dove installarlo e, quando la release
// ha più pacchetti adatti, quale. L'interfaccia lo chiede prima di partire,
// e se l'utente rinuncia quel programma si salta, senza segnarlo come errore.
func daAggiornare(v Vista) bool {
	if v.Installato.Presente {
		return !v.Installato.Fissata && !v.Scelta
	}
	return v.Aggiorna && v.Ambito != "" && !v.Scelta
}

// scelteArchivio elenca le release della cartella d'archivio che si possono
// installare su questo sistema. Legge solo i nomi dei file.
func (a *App) scelteArchivio(v voce) []SceltaRelease {
	if v.CartellaArchivio == "" {
		return nil
	}
	var out []SceltaRelease
	for _, nome := range scarica.Scaricate(v.CartellaArchivio) {
		if len(out) >= 30 {
			break
		}
		file := scarica.FileDellaRelease(v.CartellaArchivio, nome)
		nomi := make([]string, len(file))
		for i, f := range file {
			nomi[i] = f.Nome
		}
		c, err := installa.Candidati(nomi, runtime.GOOS, runtime.GOARCH, v.Asset)
		if err != nil || len(c) == 0 {
			continue
		}
		for i := range c {
			for _, f := range file {
				if f.Nome == c[i].Nome {
					c[i].Dimensione = f.Dimensione
				}
			}
		}
		out = append(out, SceltaRelease{Tag: nome, Nome: nome, Ultima: len(out) == 0, Pacchetti: c})
	}
	return out
}

// piano è ciò che resta deciso prima di scaricare e installare.
type piano struct {
	tag      string
	fissata  bool // una versione scelta che non è l'ultima
	p        installa.Pacchetto
	stato    installa.Stato
	ambito   string
	cartelle installa.Cartelle
	reg      string
}

// pianifica fa i controlli comuni a ogni installazione: se la versione c'è
// già, quale pacchetto, se il programma c'è già in un altro modo o non è
// nostro, dove metterlo. Se qualcosa non va lo annota e restituisce nil.
// vecchia dice che la release non è l'ultima (solo per i messaggi).
func (a *App) pianifica(ctx context.Context, v voce, o Opzioni, tag string, pacchetti []installa.Pacchetto, fissata, vecchia bool) *piano {
	reg, err := a.cartellaRegistro()
	if err != nil {
		a.fallito(ctx, v, modoUpdate, tag, err)
		return nil
	}
	a.mu.Lock()
	cartelle := a.cartelle
	a.mu.Unlock()
	stato := installa.LeggiStato(reg, v.Nome, runtime.GOOS, cartelle)
	if (stato.Nostro && stato.Tag == tag) || stessaVersione(tag, stato.Versione) {
		messaggio := "Già aggiornato alla " + tag
		if vecchia {
			messaggio = "La " + tag + " è già installata"
		}
		a.log(v.Nome + ": " + messaggio)
		a.annota(v.ID, modoUpdate, true, tag, messaggio)
		return nil
	}
	// niente discese di versione di nascosto: se l'ultima release è più
	// vecchia di quella installata (un attacco che ripubblica una versione
	// vecchia e vulnerabile, o un errore), si installa solo se l'utente sceglie
	// quella versione
	if o.Tag == "" && !fissata && stato.Presente {
		corrente := stato.Versione
		if stato.Nostro {
			corrente = stato.Tag
		}
		if scarica.ConfrontaVersioni(tag, corrente) < 0 {
			a.fallito(ctx, v, modoUpdate, tag, fmt.Errorf("la release più recente (%s) è più vecchia di quella installata (%s): non scendo di versione da solo; se la vuoi davvero, scegli la versione dalla finestra di installazione", tag, corrente))
			return nil
		}
	}
	// quale pacchetto: quello scelto dall'utente; altrimenti, se il
	// programma c'è già, uno dello stesso genere; altrimenti l'unico adatto
	var installato installa.Pacchetto
	if stato.Presente {
		installato = installa.Pacchetto{Nome: stato.Asset, Tipo: stato.Tipo}
	}
	p, err := installa.Scegli(pacchetti, installa.Pacchetto{Nome: o.Asset, Tipo: o.Tipo}, installato)
	if err != nil {
		a.fallito(ctx, v, modoUpdate, tag, err)
		return nil
	}
	if stato.Presente && !installa.StessoGenere(p.Tipo, stato.Tipo) {
		a.fallito(ctx, v, modoUpdate, tag, errors.New("il programma è installato in un altro modo ("+stato.Tipo+"): per passare a "+p.Tipo+" va prima disinstallato"))
		return nil
	}
	if stato.Presente && !stato.Nostro && !o.Sostituisci {
		a.fallito(ctx, v, modoUpdate, tag, fmt.Errorf("c'è già un programma con questo nome non installato da CentralHub (%s): va confermata la sostituzione", stato.Percorso))
		return nil
	}
	// dove: se c'è già si resta dov'è, altrimenti dove ha scelto l'utente.
	// Un installatore decide da sé.
	ambito := v.Ambito
	if stato.Presente {
		ambito = stato.Ambito
	}
	if ambito == "" && !installa.DaSistema(p.Tipo) {
		a.fallito(ctx, v, modoUpdate, tag, errors.New("va scelto dove installare: solo per te o per tutto il computer"))
		return nil
	}
	return &piano{tag: tag, fissata: fissata, p: p, stato: stato, ambito: ambito, cartelle: cartelle, reg: reg}
}

// sommaDi cerca la somma SHA-256 di un file di una release nel suo
// SHA256SUMS.txt. Vuota (senza errore) se la release non lo pubblica o non
// elenca quel file.
func (a *App) sommaDi(ctx context.Context, v voce, r *github.Release, nome string) (string, error) {
	somme, err := a.nuovoCli(v.token).Somme(ctx, v.Repo, r)
	if err != nil {
		return "", fmt.Errorf("SHA256SUMS.txt non si è letto: %w", err)
	}
	return somme[nome], nil
}

func (a *App) aggiorna(ctx context.Context, v voce, o Opzioni) {
	if o.Da == "archivio" {
		a.aggiornaDaArchivio(ctx, v, o)
		return
	}
	ultima, err := a.ultima(ctx, v)
	if err != nil {
		a.fallito(ctx, v, modoUpdate, "", err)
		return
	}
	r := ultima
	if o.Tag != "" && o.Tag != ultima.Tag {
		if r, err = a.release(ctx, v, o.Tag); err != nil {
			a.fallito(ctx, v, modoUpdate, o.Tag, err)
			return
		}
	}
	pi := a.pianifica(ctx, v, o, r.Tag, a.pacchetti(v, r), r != ultima, r != ultima)
	if pi == nil {
		return
	}
	a.installaScelto(ctx, v, pi, func(lavoro string) (string, error) {
		a.fase(v.ID, "download")
		// la firma si controlla sempre; le somme firmate valgono più del digest
		fr, err := a.verificaRelease(ctx, v, r)
		if err != nil {
			return "", err
		}
		rr := fr.Release
		if fr.Firmata() {
			if _, ok := fr.Somme[pi.p.Nome]; !ok {
				return "", fmt.Errorf("%s non è nell'elenco firmato delle somme", pi.p.Nome)
			}
			a.log(v.Nome + ": firma di SHA256SUMS.txt valida")
		} else {
			a.log(v.Nome + ": release non firmata (si usa il digest di GitHub o SHA256SUMS.txt)")
		}
		var asset github.Asset
		for _, as := range rr.Assets {
			if as.Nome == pi.p.Nome {
				asset = as
			}
		}
		if asset.Digest == "" {
			// GitHub non dà il digest di questo file (è anteriore al 2025):
			// la somma si cerca in SHA256SUMS.txt. Senza nessuna delle due,
			// scarica.Asset rifiuta di installarlo.
			somma, err := a.sommaDi(ctx, v, r, asset.Nome)
			if err != nil {
				return "", err
			}
			if somma != "" {
				asset.Digest = "sha256:" + somma
			}
		}
		return scarica.Asset(ctx, a.nuovoCli(v.token), v.Repo, asset, lavoro, a.avanzamento(v.ID))
	})
}

// aggiornaDaArchivio installa un pacchetto che sta già nella cartella
// d'archivio del repository: niente rete, e l'archivio non si tocca (il
// pacchetto si copia nella cartella di lavoro). La somma si controlla se si
// conosce: nel SHA256SUMS.txt archiviato accanto ai pacchetti, o nel digest
// della release se è quella che GitHub ha ancora; altrimenti si installa lo
// stesso, perché l'archivio è dell'utente, e si scrive nel registro.
func (a *App) aggiornaDaArchivio(ctx context.Context, v voce, o Opzioni) {
	if v.CartellaArchivio == "" {
		a.fallito(ctx, v, modoUpdate, o.Tag, errors.New("non è stata scelta la cartella d'archivio di questo repository"))
		return
	}
	cartellaRel := o.Tag
	if cartellaRel == "" {
		s := scarica.Scaricate(v.CartellaArchivio)
		if len(s) == 0 {
			a.fallito(ctx, v, modoUpdate, "", errors.New("l'archivio di questo repository è vuoto"))
			return
		}
		cartellaRel = s[0]
	}
	if cartellaRel != filepath.Base(cartellaRel) || strings.HasPrefix(cartellaRel, ".") || !scarica.Presente(v.CartellaArchivio, cartellaRel) {
		a.fallito(ctx, v, modoUpdate, cartellaRel, fmt.Errorf("la release %q non è nell'archivio", cartellaRel))
		return
	}
	file := scarica.FileDellaRelease(v.CartellaArchivio, cartellaRel)
	nomi := make([]string, len(file))
	for i, f := range file {
		nomi[i] = f.Nome
	}
	pacchetti, err := installa.Candidati(nomi, runtime.GOOS, runtime.GOARCH, v.Asset)
	if err != nil {
		a.fallito(ctx, v, modoUpdate, cartellaRel, fmt.Errorf("nell'archivio, %s: %w", cartellaRel, err))
		return
	}
	// il tag vero, se è la release che GitHub ha dato per ultima
	a.mu.Lock()
	nota := a.ultime[v.ID]
	a.mu.Unlock()
	tag, fissata := cartellaRel, false
	if nota != nil {
		if scarica.NomeCartella(nota.Tag) == cartellaRel {
			tag = nota.Tag
		} else {
			fissata = true
		}
	}
	pi := a.pianifica(ctx, v, o, tag, pacchetti, fissata, fissata)
	if pi == nil {
		return
	}
	a.installaScelto(ctx, v, pi, func(lavoro string) (string, error) {
		a.fase(v.ID, "copia")
		atteso := scarica.SommeArchiviate(v.CartellaArchivio, cartellaRel)[pi.p.Nome]
		somme, firmata, err := a.firmateDallArchivio(v, cartellaRel, tag)
		if err != nil {
			return "", err
		}
		if firmata {
			if atteso = somme[pi.p.Nome]; atteso == "" {
				return "", fmt.Errorf("%s non è nell'elenco firmato delle somme", pi.p.Nome)
			}
			a.log(v.Nome + ": firma di SHA256SUMS.txt valida (archivio)")
		} else if atteso == "" && nota != nil && tag == nota.Tag {
			for _, as := range nota.Assets {
				if as.Nome == pi.p.Nome {
					atteso = strings.TrimPrefix(strings.ToLower(as.Digest), "sha256:")
				}
			}
		}
		file, verificato, err := scarica.DallArchivio(ctx, filepath.Join(v.CartellaArchivio, cartellaRel, pi.p.Nome), lavoro, atteso, a.avanzamento(v.ID))
		if err == nil && !verificato {
			a.log(v.Nome + ": " + pi.p.Nome + " dall'archivio non si è potuto verificare (nessuna somma di controllo nota)")
		}
		return file, err
	})
}

// installaScelto porta il pacchetto nella cartella di lavoro (dal sito o
// dall'archivio, lo dice ottieni) e lo installa.
func (a *App) installaScelto(ctx context.Context, v voce, pi *piano, ottieni func(lavoro string) (string, error)) {
	lavoro, err := a.cartellaLavoro()
	if err != nil {
		a.fallito(ctx, v, modoUpdate, pi.tag, err)
		return
	}
	lavoro = filepath.Join(lavoro, installa.NomeProgramma(v.Nome))
	defer os.RemoveAll(lavoro)
	file, err := ottieni(lavoro)
	if err != nil {
		a.fallito(ctx, v, modoUpdate, pi.tag, err)
		return
	}
	p, stato := pi.p, pi.stato
	if installa.DaSistema(p.Tipo) {
		a.fase(v.ID, "installatore")
	} else {
		a.fase(v.ID, "installazione")
	}
	e, err := installa.Installa(installa.Richiesta{ID: v.ID, Nome: v.Nome, Repo: v.Repo, Tag: pi.tag, File: file, Tipo: p.Tipo,
		Ambito: pi.ambito, Fissata: pi.fissata, Cartelle: pi.cartelle, CartellaRegistro: pi.reg, Ctx: ctx})
	if err != nil {
		a.fallito(ctx, v, modoUpdate, pi.tag, fmt.Errorf("installazione non riuscita: %w", err))
		return
	}
	a.annotaIntegrita(v, pi.reg, p.Tipo, e)
	verbo := "Installato"
	if stato.Presente {
		verbo = "Aggiornato"
	}
	messaggio := verbo + " alla " + pi.tag
	switch {
	case e.Percorso != "":
		messaggio += " in " + e.Percorso
	case p.Tipo == installa.TipoDeb:
		messaggio += " con apt"
	case p.Tipo == installa.TipoInstallatore:
		messaggio += " dal suo installatore"
	case p.Tipo == installa.TipoPkg:
		messaggio += " con l'Installer di macOS"
	}
	for _, avv := range e.Avvisi {
		a.log(v.Nome + ": " + avv)
	}
	if e.SeStesso {
		messaggio += ". Chiudi e riapri CentralHub per usare la versione nuova"
	}
	a.log(v.Nome + ": " + messaggio + " (" + e.Asset + ")")
	a.annota(v.ID, modoUpdate, true, pi.tag, messaggio)
}

// ---------------------------------------------------------------- disinstallazione

// InfoRimozione è ciò che la finestra di conferma mostra prima di
// disinstallare un programma.
type InfoRimozione struct {
	Installato installa.Stato `json:"installato"`
	// Dati sono le cartelle di impostazioni e di dati del programma trovate
	// sul computer: l'utente sceglie se tenerle.
	Dati []string `json:"dati"`
}

// datiDi cerca le impostazioni di un programma installato. La cartella di
// configurazione di CentralHub non è mai fra quelle proposte.
func (a *App) datiDi(v voce, s installa.Stato) []string {
	a.mu.Lock()
	basi := a.basiDati
	a.mu.Unlock()
	mia, _, _ := config.Cartella()
	return installa.DatiProgramma(basi, v.Nome, s.Percorso, runtime.GOOS, mia)
}

// InfoRimozione dice cosa verrebbe tolto disinstallando un programma.
func (a *App) InfoRimozione(id string) (InfoRimozione, error) {
	a.mu.Lock()
	v, ok := a.voce(id)
	cartelle := a.cartelle
	a.mu.Unlock()
	if !ok {
		return InfoRimozione{}, errors.New("repository non trovato")
	}
	reg, err := a.cartellaRegistro()
	if err != nil {
		return InfoRimozione{}, err
	}
	s := installa.LeggiStato(reg, v.Nome, runtime.GOOS, cartelle)
	if !s.Presente {
		return InfoRimozione{}, errors.New(v.Nome + " non risulta installato")
	}
	i := InfoRimozione{Installato: s, Dati: a.datiDi(v, s)}
	if i.Dati == nil {
		i.Dati = []string{}
	}
	return i, nil
}

// Disinstalla toglie dal computer il programma di un repository: i suoi
// file, la voce di menu e ciò che CentralHub aveva annotato. Con togliDati
// vanno nel cestino anche le sue impostazioni (quelle elencate da
// InfoRimozione); altrimenti restano dove sono. Il repository resta nella
// lista.
func (a *App) Disinstalla(id string, togliDati bool) error {
	return a.uno(id, modoUpdate, func(ctx context.Context, v voce) { a.disinstalla(ctx, v, togliDati) })
}

func (a *App) disinstalla(ctx context.Context, v voce, togliDati bool) {
	reg, err := a.cartellaRegistro()
	if err != nil {
		a.fallito(ctx, v, modoUpdate, "", err)
		return
	}
	a.mu.Lock()
	cartelle := a.cartelle
	a.mu.Unlock()
	stato := installa.LeggiStato(reg, v.Nome, runtime.GOOS, cartelle)
	var dati []string
	if togliDati && stato.Presente {
		dati = a.datiDi(v, stato)
	}
	a.fase(v.ID, "disinstallazione")
	e, err := installa.Disinstalla(installa.Rimozione{Nome: v.Nome, Cartelle: cartelle, CartellaRegistro: reg, Dati: dati, Ctx: ctx, Cestina: a.cestina})
	if err != nil {
		a.fallito(ctx, v, modoUpdate, stato.Tag, fmt.Errorf("disinstallazione non riuscita: %w", err))
		return
	}
	// un programma che l'utente ha tolto non è più fra quelli da tenere
	// aggiornati: altrimenti «Aggiorna tutti» lo rimetterebbe
	if v.Aggiorna {
		a.mu.Lock()
		l := a.localeSalvato(v)
		l.Aggiorna = false
		err := a.salvaLocale(v, l)
		a.mu.Unlock()
		if err != nil {
			a.log(v.Nome + ": " + err.Error())
		}
	}
	integrita.Elimina(reg, installa.NomeProgramma(v.Nome))
	a.mu.Lock()
	delete(a.integri, v.ID)
	a.mu.Unlock()
	messaggio := "Disinstallato"
	if e.Percorso != "" {
		messaggio += " da " + e.Percorso
	}
	switch {
	case len(e.DatiTolti) > 0:
		messaggio += ". Impostazioni spostate nel cestino: " + strings.Join(e.DatiTolti, ", ")
	case !togliDati:
		messaggio += ". Le sue impostazioni non sono state toccate"
	}
	for _, avv := range e.Avvisi {
		a.log(v.Nome + ": " + avv)
	}
	a.log(v.Nome + ": " + messaggio)
	a.annota(v.ID, modoUpdate, true, "", messaggio)
}
