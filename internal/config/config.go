// SPDX-License-Identifier: MIT

// Package config tiene la configurazione di CentralHub: i repository
// aggiunti dall'utente, i loro tag e ciò che di ogni repository riguarda
// questo computer (cartella d'archivio, dove installare). È un JSON leggibile,
// esportabile e importabile, che resta dov'è quando il programma si
// aggiorna.
//
// I repository privati hanno una chiave di accesso (un token GitHub di sola
// lettura): sta nel file di configurazione di questo computer, che solo
// l'utente può leggere, e finisce in un file esportato soltanto se l'utente
// lo chiede. Nel programma non c'è nessun repository né nessuna chiave.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// VariabileCartella sposta la cartella di configurazione: le prove non
// toccano mai quella vera dell'utente.
const VariabileCartella = "CENTRALHUB_CONFIG_DIR"

const (
	nomeFile  = "config.json"
	nomeStato = "stato.json"
	versione  = 1
)

// Ambiti di installazione.
const (
	AmbitoUtente  = "utente"  // solo per chi usa il programma ("appdata")
	AmbitoSistema = "sistema" // per tutto il computer ("Programmi")
)

// Locale è ciò che di un repository riguarda questo computer.
type Locale struct {
	// Tag raggruppa i repository nella lista.
	Tag string `json:"tag,omitempty"`
	// CartellaArchivio è la cartella master della modalità Archiviazione:
	// ogni release in una sua sottocartella. Vuota = non si archivia.
	CartellaArchivio string `json:"cartellaArchivio,omitempty"`
	// Aggiorna: il programma è fra quelli da tenere aggiornati (modalità
	// Update) anche se non è ancora installato.
	Aggiorna bool `json:"aggiorna,omitempty"`
	// Ambito dice dove installarlo: AmbitoUtente, AmbitoSistema, oppure
	// vuoto finché l'utente non ha scelto.
	Ambito string `json:"ambito,omitempty"`
}

// Repo è un repository della lista.
type Repo struct {
	ID   string `json:"id"`
	Nome string `json:"nome"`
	// Repo nella forma "proprietario/nome".
	Repo string `json:"repo"`
	// Asset è il modello (con * e ?) del pacchetto da installare; vuoto = lo
	// sceglie CentralHub dal nome, secondo il sistema.
	Asset string `json:"asset,omitempty"`
	// Privato: il repository richiede una chiave di accesso. Resta vero anche
	// in un file esportato senza chiavi, così chi lo importa sa che gli manca.
	Privato bool `json:"privato,omitempty"`
	// Token è la chiave di accesso (token GitHub in sola lettura). Vuoto se il
	// repository è pubblico, se la chiave non è stata ancora messa o se il
	// file è stato esportato senza chiavi.
	Token string `json:"token,omitempty"`
	Locale
}

// Config è tutto ciò che si esporta e si importa.
type Config struct {
	Versione int    `json:"versione"`
	Repo     []Repo `json:"repo"`
	// Incorporati esiste solo per leggere i file della 0.1.0, dove i
	// repository dell'autore stavano nel programma e qui restavano le loro
	// impostazioni locali. Decodifica li trasforma in repository normali, e
	// non si scrive più.
	Incorporati map[string]Locale `json:"incorporati,omitempty"`
}

// Cartella restituisce la cartella di configurazione e dice se è stata
// spostata con la variabile d'ambiente (modalità isolata, per le prove).
func Cartella() (percorso string, isolata bool, err error) {
	if d := os.Getenv(VariabileCartella); d != "" {
		return d, true, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", false, fmt.Errorf("cartella di configurazione non trovata: %w", err)
	}
	return filepath.Join(base, "CentralHub"), false, nil
}

// PercorsoFile è il file della configurazione.
func PercorsoFile() (string, error) {
	d, _, err := Cartella()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, nomeFile), nil
}

// Carica legge la configurazione. Se il file non esiste restituisce una
// configurazione vuota: è il primo avvio, non un errore.
func Carica() (Config, error) {
	p, err := PercorsoFile()
	if err != nil {
		return nuova(), err
	}
	return leggi(p)
}

func nuova() Config {
	return Config{Versione: versione, Repo: []Repo{}}
}

func leggi(percorso string) (Config, error) {
	b, err := os.ReadFile(percorso)
	if errors.Is(err, os.ErrNotExist) {
		return nuova(), nil
	}
	if err != nil {
		return nuova(), err
	}
	return Decodifica(b)
}

// Decodifica legge una configurazione da JSON e la controlla.
func Decodifica(b []byte) (Config, error) {
	c := nuova()
	if err := json.Unmarshal(b, &c); err != nil {
		return nuova(), fmt.Errorf("il file non è una configurazione di CentralHub valida: %w", err)
	}
	if c.Repo == nil {
		c.Repo = []Repo{}
	}
	c.Versione = versione
	visti := map[string]bool{}
	for i := range c.Repo {
		p := &c.Repo[i]
		p.CartellaArchivio = senzaPercorsoAltrui(p.CartellaArchivio)
		if err := p.Normalizza(); err != nil {
			return nuova(), fmt.Errorf("repository %d: %w", i+1, err)
		}
		if p.ID == "" || visti[p.ID] {
			p.ID = NuovoID()
		}
		visti[p.ID] = true
	}
	migra, err := daIncorporati(c)
	if err != nil {
		return nuova(), err
	}
	c.Repo = append(c.Repo, migra...)
	c.Incorporati = nil
	return c, nil
}

// senzaPercorsoAltrui toglie un percorso completo di un altro sistema (un
// file esportato sul Mac e importato su Windows contiene /Volumes/…): su
// questo computer non vale, e non deve far fallire tutta l'importazione. Un
// percorso relativo invece è un errore e resta tale.
func senzaPercorsoAltrui(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || reUnita.MatchString(p) {
		return ""
	}
	return p
}

var reUnita = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// IDDaRepo è l'identificativo dei repository ricavati dalle impostazioni
// della 0.1.0: sempre lo stesso per lo stesso repository, così gli esiti già
// salvati si ritrovano.
func IDDaRepo(repo string) string {
	h := sha256.Sum256([]byte(strings.ToLower(repo)))
	return hex.EncodeToString(h[:6])
}

// ChiaveEsitoVecchia è la chiave con cui la 0.1.0 salvava gli esiti di un
// repository incorporato.
func ChiaveEsitoVecchia(repo string) string { return "inc:" + strings.ToLower(repo) }

// daIncorporati trasforma le impostazioni dei repository incorporati della
// 0.1.0 in repository normali, privati e senza chiave: l'utente la rimette da
// «Modifica». Di un repository che c'è già non si tocca niente. Il nome non
// stava nel file (la chiave è in minuscolo): si prende dalla cartella
// d'archivio se porta lo stesso nome, altrimenti dal nome del repository.
func daIncorporati(c Config) ([]Repo, error) {
	chiavi := make([]string, 0, len(c.Incorporati))
	for k := range c.Incorporati {
		chiavi = append(chiavi, k)
	}
	sort.Strings(chiavi)
	ci := map[string]bool{}
	for _, r := range c.Repo {
		ci[strings.ToLower(r.Repo)] = true
	}
	var out []Repo
	for _, k := range chiavi {
		repo, err := PulisciRepo(k)
		if err != nil || ci[strings.ToLower(repo)] {
			continue
		}
		l := c.Incorporati[k]
		l.CartellaArchivio = senzaPercorsoAltrui(l.CartellaArchivio)
		if err := l.Normalizza(); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		nome := repo[strings.Index(repo, "/")+1:]
		if l.CartellaArchivio != "" {
			if base := filepath.Base(l.CartellaArchivio); strings.EqualFold(base, nome) {
				nome = base
			}
		}
		// su macOS i programmi dell'autore andavano per tutto il computer
		// senza chiedere: la scelta, prima nel codice, ora sta qui
		if l.Ambito == "" && runtime.GOOS == "darwin" {
			l.Ambito = AmbitoSistema
		}
		out = append(out, Repo{ID: IDDaRepo(repo), Nome: nome, Repo: repo, Privato: true, Locale: l})
	}
	return out, nil
}

// Salva scrive la configurazione: prima una copia .bak di quella che c'è,
// poi file provvisorio e rinomina, così non resta mai un file a metà.
func Salva(c Config) error {
	p, err := PercorsoFile()
	if err != nil {
		return err
	}
	return scrivi(p, c, true)
}

// Esporta scrive la configurazione nel file scelto dall'utente. Le chiavi di
// accesso ci finiscono solo con conChiavi: senza, il file ha tutto il resto
// (anche l'indicazione che il repository è privato).
func Esporta(percorso string, c Config, conChiavi bool) error {
	c.Repo = append([]Repo(nil), c.Repo...)
	for i := range c.Repo {
		if c.Repo[i].Token != "" {
			c.Repo[i].Privato = true // chi importa senza la chiave deve saperlo
			if !conChiavi {
				c.Repo[i].Token = ""
			}
		}
	}
	return scrivi(percorso, c, false)
}

// Chiavi conta i repository che hanno una chiave di accesso.
func (c Config) Chiavi() int {
	n := 0
	for _, r := range c.Repo {
		if r.Token != "" {
			n++
		}
	}
	return n
}

// Importa legge una configurazione dal file scelto dall'utente.
func Importa(percorso string) (Config, error) {
	b, err := os.ReadFile(percorso)
	if err != nil {
		return nuova(), err
	}
	return Decodifica(b)
}

func scrivi(percorso string, c Config, copia bool) error {
	c.Versione = versione
	if c.Repo == nil {
		c.Repo = []Repo{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(percorso), 0o700); err != nil {
		return err
	}
	if copia {
		if vecchio, err := os.ReadFile(percorso); err == nil {
			if err := os.WriteFile(percorso+".bak", vecchio, 0o600); err != nil {
				return fmt.Errorf("copia di sicurezza non riuscita: %w", err)
			}
		}
	}
	tmp := percorso + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, percorso); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// NuovoID genera un identificativo per un repository aggiunto a mano.
func NuovoID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

var reRepo = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)

// PulisciRepo accetta "proprietario/nome" ma anche l'indirizzo copiato dal
// browser (https://github.com/proprietario/nome, con o senza .git) e
// restituisce la forma breve.
func PulisciRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, pref := range []string{"https://", "http://", "git@"} {
		s = strings.TrimPrefix(s, pref)
	}
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimPrefix(s, "github.com:")
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	if parti := strings.Split(s, "/"); len(parti) > 2 {
		s = parti[0] + "/" + parti[1] // un indirizzo con /releases, /tree/… in coda
	}
	if !reRepo.MatchString(s) || strings.HasSuffix(s, "/.") || strings.HasSuffix(s, "/..") {
		return "", errors.New(`repository non valido: va scritto come "proprietario/nome"`)
	}
	return s, nil
}

// Normalizza ripulisce e controlla le impostazioni locali.
func (l *Locale) Normalizza() error {
	l.Tag = strings.TrimSpace(l.Tag)
	l.CartellaArchivio = strings.TrimSpace(l.CartellaArchivio)
	if l.CartellaArchivio != "" {
		if !filepath.IsAbs(l.CartellaArchivio) {
			return fmt.Errorf("la cartella %q non è un percorso completo", l.CartellaArchivio)
		}
		l.CartellaArchivio = filepath.Clean(l.CartellaArchivio)
	}
	switch l.Ambito {
	case "", AmbitoUtente, AmbitoSistema:
	default:
		return fmt.Errorf("ambito di installazione non valido: %q", l.Ambito)
	}
	return nil
}

// Normalizza ripulisce e controlla i campi di un repository.
func (p *Repo) Normalizza() error {
	p.Nome = strings.TrimSpace(p.Nome)
	p.Asset = strings.TrimSpace(p.Asset)
	p.Token = strings.TrimSpace(p.Token)
	if p.Token != "" {
		if len(p.Token) > 512 || strings.IndexFunc(p.Token, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
			// il valore non va nel messaggio: è una chiave
			return errors.New("la chiave di accesso non è valida: dev'essere una sola riga, senza spazi")
		}
		p.Privato = true
	}
	repo, err := PulisciRepo(p.Repo)
	if err != nil {
		return err
	}
	p.Repo = repo
	if p.Nome == "" {
		p.Nome = repo[strings.Index(repo, "/")+1:]
	}
	if p.Asset != "" {
		if _, err := filepath.Match(strings.ToLower(p.Asset), "x"); err != nil {
			return fmt.Errorf("modello del pacchetto non valido: %q", p.Asset)
		}
	}
	return p.Locale.Normalizza()
}

// Esito è l'ultimo lavoro (archiviazione o update) su un repository. Non fa parte
// della configurazione esportata: è lo stato di questo computer.
type Esito struct {
	OK        bool   `json:"ok"`
	Messaggio string `json:"messaggio"`
	Tag       string `json:"tag,omitempty"`
	Quando    string `json:"quando"` // RFC 3339
}

// CaricaEsiti legge gli ultimi esiti (chiave: ID del repository e modalità).
func CaricaEsiti() map[string]Esito {
	m := map[string]Esito{}
	d, _, err := Cartella()
	if err != nil {
		return m
	}
	if b, err := os.ReadFile(filepath.Join(d, nomeStato)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// SalvaEsiti scrive gli ultimi esiti.
func SalvaEsiti(m map[string]Esito) error {
	d, _, err := Cartella()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(d, nomeStato+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(d, nomeStato))
}
