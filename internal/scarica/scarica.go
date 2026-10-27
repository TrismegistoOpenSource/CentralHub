// SPDX-License-Identifier: MIT

// Package scarica porta una release nella cartella master di un repository:
// una sottocartella con il nome del tag, dentro ogni asset come file
// separato, più gli archivi dei sorgenti. Si scarica in una cartella
// provvisoria nascosta (".<tag>.parziale") che prende il nome della release
// solo a download completo e verificato: una release a metà non compare mai.
// Se il download si interrompe i file già arrivati interi restano nella
// cartella provvisoria, e il tentativo successivo riparte da lì invece di
// riscaricare tutto (una release può pesare qualche GB).
package scarica

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"centralhub/internal/firma"
	"centralhub/internal/github"
)

var reNonSicuro = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

// NomeCartella trasforma un tag nel nome della sua sottocartella: solo
// lettere, numeri, punto, trattino, più e sottolineato.
func NomeCartella(tag string) string {
	n := strings.Trim(reNonSicuro.ReplaceAllString(strings.TrimSpace(tag), "-"), "-.")
	if n == "" {
		return "release"
	}
	return n
}

// nomeFile rende sicuro il nome di un asset: mai un percorso, mai un file
// nascosto.
func nomeFile(nome string) (string, error) {
	n := strings.TrimSpace(nome)
	if n == "" || n != filepath.Base(n) || strings.ContainsAny(n, `/\:`) || strings.HasPrefix(n, ".") {
		return "", fmt.Errorf("nome di asset non accettabile: %q", nome)
	}
	return n, nil
}

func parziale(cartella, tag string) string {
	return filepath.Join(cartella, "."+NomeCartella(tag)+".parziale")
}

// Presente dice se la release con quel tag è già nella cartella master.
func Presente(cartella, tag string) bool {
	fi, err := os.Stat(filepath.Join(cartella, NomeCartella(tag)))
	return err == nil && fi.IsDir()
}

// Avanzamento riceve i byte scaricati finora e il totale (0 se ignoto).
type Avanzamento func(file string, fatti, totale int64)

// Risultato di un download.
type Risultato struct {
	Cartella    string // la sottocartella della release
	GiaPresente bool
	File        []string
	// Verificati sono i pacchetti controllati contro il loro SHA-256
	// (digest di GitHub o SHA256SUMS.txt); NonVerificati i pacchetti per cui
	// la release non pubblica nessuna somma. Gli archivi dei sorgenti non
	// hanno somma e non compaiono in nessuno dei due.
	Verificati    []string
	NonVerificati []string
}

// nomeSommeArchivio è il file nascosto in cui l'archiviazione annota le somme
// con cui ha verificato i pacchetti: serve a controllarli di nuovo quando si
// installa dall'archivio, anche se la release non pubblica un SHA256SUMS.txt.
const nomeSommeArchivio = ".centralhub-sha256.txt"

// sorgente è un file da scaricare.
type sorgente struct {
	nome       string
	indirizzo  string
	asset      bool
	dimensione int64
	digest     string
	// web: l'indirizzo è sul sito pubblico, non sull'API.
	web bool
}

// perAsset sceglie da dove scaricare un pacchetto: dal sito pubblico quando
// si può (repository pubblico, nessun token: non consuma il limite di
// richieste dell'API), altrimenti dall'API.
func perAsset(c *github.Client, nome string, a github.Asset) sorgente {
	if c.Web() != "" && a.Pubblico != "" {
		return sorgente{nome, a.Pubblico, true, a.Dimensione, a.Digest, true}
	}
	return sorgente{nome, a.URL, true, a.Dimensione, a.Digest, false}
}

// Scarica porta la release in cartella/<tag>. Se c'è già non fa niente.
func Scarica(ctx context.Context, c *github.Client, repo string, r *github.Release, cartella string, av Avanzamento) (Risultato, error) {
	dest := filepath.Join(cartella, NomeCartella(r.Tag))
	if Presente(cartella, r.Tag) {
		return Risultato{Cartella: dest, GiaPresente: true}, nil
	}
	var elenco []sorgente
	visti := map[string]bool{}
	for _, a := range r.Assets {
		n, err := nomeFile(a.Nome)
		if err != nil {
			return Risultato{}, err
		}
		if visti[strings.ToLower(n)] {
			return Risultato{}, fmt.Errorf("la release ha due asset con lo stesso nome: %s", n)
		}
		visti[strings.ToLower(n)] = true
		elenco = append(elenco, perAsset(c, n, a))
	}
	// Gli archivi dei sorgenti, che GitHub non conta fra gli asset.
	base := repo[strings.Index(repo, "/")+1:] + "-" + NomeCartella(r.Tag) + "-sorgente"
	zip, tar, web := r.Zipball, r.Tarball, false
	if w := c.Web(); w != "" {
		zip, tar = github.SorgentiWeb(w, repo, r.Tag)
		web = true
	}
	for _, s := range []sorgente{{base + ".zip", zip, false, 0, "", web}, {base + ".tar.gz", tar, false, 0, "", web}} {
		if s.indirizzo != "" && !visti[strings.ToLower(s.nome)] {
			elenco = append(elenco, s)
		}
	}
	if len(elenco) == 0 {
		return Risultato{}, errors.New("la release non contiene nessun file da scaricare")
	}

	if err := os.MkdirAll(cartella, 0o755); err != nil {
		return Risultato{}, fmt.Errorf("la cartella %s non si può creare: %w", cartella, err)
	}
	// i tentativi rimasti a metà di ALTRE release non servono più
	PulisciParziali(cartella, r.Tag)
	tmp := parziale(cartella, r.Tag)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Risultato{}, fmt.Errorf("non si può scrivere in %s: %w", cartella, err)
	}
	// nella cartella provvisoria si tengono solo i file di questa release
	voluti := map[string]bool{nomeSommeArchivio: true}
	for _, s := range elenco {
		voluti[s.nome] = true
		voluti[filepath.Base(parte(s.nome))] = true
	}
	if voci, err := os.ReadDir(tmp); err == nil {
		for _, v := range voci {
			if !voluti[v.Name()] {
				os.RemoveAll(filepath.Join(tmp, v.Name()))
			}
		}
	}

	var nomi []string
	for _, s := range elenco {
		percorso := filepath.Join(tmp, s.nome)
		if giaIntero(percorso, s) {
			if av != nil {
				av(s.nome, s.dimensione, s.dimensione)
			}
			nomi = append(nomi, s.nome)
			continue
		}
		os.Remove(percorso)
		if err := unFile(ctx, c, repo, s, percorso, av); err != nil {
			os.Remove(percorso) // mai un file a metà fra quelli tenuti da parte
			if ctx.Err() != nil {
				return Risultato{}, ctx.Err()
			}
			return Risultato{}, fmt.Errorf("%s: %w", s.nome, err)
		}
		nomi = append(nomi, s.nome)
	}
	var verificati, nonVerificati []string
	var righe strings.Builder
	for _, s := range elenco {
		if !s.asset {
			continue
		}
		if somma, si := strings.CutPrefix(strings.ToLower(s.digest), "sha256:"); si {
			verificati = append(verificati, s.nome)
			righe.WriteString(somma + "  " + s.nome + "\n")
		} else {
			nonVerificati = append(nonVerificati, s.nome)
		}
	}
	if righe.Len() > 0 {
		// non è un motivo per fallire: il file serve solo a controllare di nuovo
		_ = os.WriteFile(filepath.Join(tmp, nomeSommeArchivio), []byte(righe.String()), 0o644)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return Risultato{}, err
	}
	return Risultato{Cartella: dest, File: nomi, Verificati: verificati, NonVerificati: nonVerificati}, nil
}

// giaIntero dice se un file rimasto da un tentativo precedente è quello
// giusto, intero: stessa dimensione e, quando GitHub lo dà, stesso SHA-256.
// Senza una dimensione dichiarata (gli archivi dei sorgenti) non si può
// sapere: si riscarica.
func giaIntero(percorso string, s sorgente) bool {
	fi, err := os.Stat(percorso)
	if err != nil || !fi.Mode().IsRegular() || s.dimensione <= 0 || fi.Size() != s.dimensione {
		return false
	}
	atteso, si := strings.CutPrefix(strings.ToLower(s.digest), "sha256:")
	if !si {
		return true
	}
	got, err := sommaFile(percorso)
	return err == nil && got == atteso
}

// TenutiDaParte dice quanti file interi di una release sono rimasti nella
// cartella provvisoria dopo un download non finito (senza quello rimasto a
// metà, ".part", da cui si riprende comunque).
func TenutiDaParte(cartella, tag string) int {
	voci, err := os.ReadDir(parziale(cartella, tag))
	if err != nil {
		return 0
	}
	n := 0
	for _, v := range voci {
		if !strings.HasSuffix(v.Name(), ".part") && !strings.HasPrefix(v.Name(), ".") {
			n++
		}
	}
	return n
}

// ErrSenzaSomma: di un pacchetto da installare non c'è nessuna somma di
// controllo pubblicata. Un file che nessuna somma garantisce non si installa.
var ErrSenzaSomma = errors.New("la release non pubblica nessuna somma di controllo per questo file (né il digest di GitHub né un SHA256SUMS.txt): per sicurezza non lo installo")

// nonCorrisponde: il file scaricato ha un SHA-256 diverso da quello
// pubblicato.
type nonCorrisponde struct{ ottenuto, atteso string }

func (e *nonCorrisponde) Error() string {
	return fmt.Sprintf("il file scaricato non corrisponde a quello pubblicato (SHA-256 %s, atteso %s)", e.ottenuto, e.atteso)
}

// parte è il file in cui si tiene un download non finito.
func parte(percorso string) string { return percorso + ".part" }

// Attese fra un tentativo e l'altro quando la connessione cade: raddoppiano
// fino al massimo; dopo tanti tentativi di fila senza un byte in più il
// download si ferma, tenendo ciò che ha (si riprende da lì).
var (
	attesaIniziale = 2 * time.Second
	attesaMassima  = 30 * time.Second
)

const maxSenzaProgressi = 8

// unFile scarica un file in percorso. Un download interrotto o caduto
// riprende dal punto in cui era (richiesta Range) e non si butta mai ciò che
// è già arrivato; se alla fine la somma non torna lo si riscarica una volta
// da zero.
func unFile(ctx context.Context, c *github.Client, repo string, s sorgente, percorso string, av Avanzamento) error {
	var err error
	for giro := 0; giro < 2; giro++ {
		if err = riprendi(ctx, c, repo, s, percorso, av); err == nil {
			return nil
		}
		var nc *nonCorrisponde
		if !errors.As(err, &nc) {
			break
		}
		os.Remove(parte(percorso)) // un file che non corrisponde non si tiene mai
	}
	return err
}

// riprendeAMeta dice se di questo file si sa la lunghezza, e quindi si può
// riprendere: gli archivi dei sorgenti non hanno né dimensione né Range
// affidabile.
func riprendeAMeta(s sorgente) bool { return s.dimensione > 0 }

func apri(ctx context.Context, c *github.Client, repo string, s sorgente, da int64) (*github.Pezzo, error) {
	if !s.asset && !s.web {
		corpo, n, err := c.Apri(ctx, s.indirizzo, repo, false)
		if err != nil {
			return nil, err
		}
		return &github.Pezzo{Corpo: corpo, Totale: n}, nil
	}
	return c.ApriDa(ctx, s.indirizzo, repo, s.web, da)
}

// transitorio dice se vale la pena riprovare dopo questo errore: la
// connessione che cade sì; un 404, un token rifiutato o il limite di
// richieste no.
func transitorio(err error) bool {
	var ge *github.Errore
	if errors.As(err, &ge) {
		return ge.Tipo == "rete"
	}
	return true
}

func riprendi(ctx context.Context, c *github.Client, repo string, s sorgente, percorso string, av Avanzamento) error {
	p := parte(percorso)
	attesa := attesaIniziale
	senzaProgressi := 0
	for {
		var da int64
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			da = fi.Size()
		}
		if !riprendeAMeta(s) || da > s.dimensione {
			os.Remove(p)
			da = 0
		}
		if da < s.dimensione || s.dimensione <= 0 {
			pezzo, err := apri(ctx, c, repo, s, da)
			if err == nil {
				err = copiaPezzo(ctx, pezzo, p, da, s, av)
			}
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err() // il parziale resta: si riprende da lì
				}
				var ge *github.Errore
				if errors.As(err, &ge) && ge.Tipo == "intervallo" {
					os.Remove(p)
					continue
				}
				if !transitorio(err) {
					return err
				}
				fi, _ := os.Stat(p)
				if fi != nil && fi.Size() > da {
					senzaProgressi, attesa = 0, attesaIniziale
				} else if senzaProgressi++; senzaProgressi >= maxSenzaProgressi {
					return fmt.Errorf("download interrotto, ciò che è arrivato è tenuto da parte per riprendere: %w", err)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(attesa):
				}
				if attesa *= 2; attesa > attesaMassima {
					attesa = attesaMassima
				}
				continue
			}
		}
		break
	}
	// finito: dimensione, somma, e solo allora il nome vero
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if s.dimensione > 0 && fi.Size() != s.dimensione {
		os.Remove(p)
		return fmt.Errorf("file incompleto: %d byte su %d", fi.Size(), s.dimensione)
	}
	if atteso, si := strings.CutPrefix(strings.ToLower(s.digest), "sha256:"); si {
		got, err := sommaFile(p)
		if err != nil {
			return err
		}
		if got != atteso {
			return &nonCorrisponde{got, atteso}
		}
	}
	return os.Rename(p, percorso)
}

// copiaPezzo accoda il corpo di pezzo al file parziale, che contiene già da
// byte; se il server ha mandato il file intero ricomincia da zero.
func copiaPezzo(ctx context.Context, pezzo *github.Pezzo, p string, da int64, s sorgente, av Avanzamento) error {
	defer pezzo.Corpo.Close()
	flag := os.O_WRONLY | os.O_CREATE
	if pezzo.Inizio == 0 {
		flag |= os.O_TRUNC
		da = 0
	} else {
		flag |= os.O_APPEND
	}
	f, err := os.OpenFile(p, flag, 0o644)
	if err != nil {
		return err
	}
	totale := s.dimensione
	if totale <= 0 && pezzo.Totale > 0 {
		totale = pezzo.Totale
	}
	_, err = io.Copy(f, &lettore{r: pezzo.Corpo, ctx: ctx, nome: s.nome, fatti: da, ultimo: da, totale: totale, av: av})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download interrotto: %w", err)
	}
	return nil
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

// lettore conta i byte, avvisa dell'avanzamento e si ferma se il lavoro
// viene annullato.
type lettore struct {
	r      io.Reader
	ctx    context.Context
	nome   string
	fatti  int64
	totale int64
	ultimo int64
	av     Avanzamento
}

func (l *lettore) Read(p []byte) (int, error) {
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := l.r.Read(p)
	l.fatti += int64(n)
	// un avviso ogni 256 kB e alla fine, non a ogni blocco
	if l.av != nil && (l.fatti-l.ultimo >= 256<<10 || err == io.EOF) {
		l.ultimo = l.fatti
		l.av(l.nome, l.fatti, l.totale)
	}
	return n, err
}

// Asset scarica un solo file della release in una cartella, verificandolo.
// Serve alla modalità Update, che prende solo il pacchetto del proprio
// sistema. Un file rimasto da un tentativo precedente viene sostituito.
func Asset(ctx context.Context, c *github.Client, repo string, a github.Asset, cartella string, av Avanzamento) (string, error) {
	n, err := nomeFile(a.Nome)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cartella, 0o700); err != nil {
		return "", err
	}
	if _, si := strings.CutPrefix(strings.ToLower(a.Digest), "sha256:"); !si {
		return "", fmt.Errorf("%s: %w", n, ErrSenzaSomma)
	}
	percorso := filepath.Join(cartella, n)
	if err := os.Remove(percorso); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := unFile(ctx, c, repo, perAsset(c, n, a), percorso, av); err != nil {
		os.Remove(percorso)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%s: %w", n, err)
	}
	return percorso, nil
}

// PulisciParziali toglie dalla cartella master i download rimasti a metà
// (cartelle ".<tag>.parziale"), tranne quello del tag indicato, che serve a
// ripartire. Tocca solo quelle cartelle: niente altro.
func PulisciParziali(cartella, tieni string) []string {
	tenuta := ""
	if tieni != "" {
		tenuta = filepath.Base(parziale(cartella, tieni))
	}
	voci, err := os.ReadDir(cartella)
	if err != nil {
		return nil
	}
	var tolte []string
	for _, v := range voci {
		n := v.Name()
		if v.IsDir() && n != tenuta && strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".parziale") && len(n) > len("..parziale") {
			if os.RemoveAll(filepath.Join(cartella, n)) == nil {
				tolte = append(tolte, n)
			}
		}
	}
	return tolte
}

// Scaricate elenca le release presenti nella cartella master, dalla più
// recente: ordine di versione quando i nomi sono numeri di versione,
// altrimenti data della cartella.
func Scaricate(cartella string) []string {
	voci, err := os.ReadDir(cartella)
	if err != nil {
		return nil
	}
	type voce struct {
		nome string
		mod  int64
	}
	var v []voce
	for _, e := range voci {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var mod int64
		if fi, err := e.Info(); err == nil {
			mod = fi.ModTime().UnixNano()
		}
		v = append(v, voce{e.Name(), mod})
	}
	sort.SliceStable(v, func(i, j int) bool {
		if c := ConfrontaVersioni(v[i].nome, v[j].nome); c != 0 {
			return c > 0
		}
		return v[i].mod > v[j].mod
	})
	nomi := make([]string, len(v))
	for i := range v {
		nomi[i] = v[i].nome
	}
	return nomi
}

var reNumeri = regexp.MustCompile(`\d+`)

// ConfrontaVersioni confronta due tag come numeri di versione (v1.10.0 >
// v1.9.3). Restituisce 1, -1, oppure 0 se sono uguali o non confrontabili.
func ConfrontaVersioni(a, b string) int {
	na, nb := reNumeri.FindAllString(a, -1), reNumeri.FindAllString(b, -1)
	if len(na) == 0 || len(nb) == 0 {
		return 0
	}
	for i := 0; i < len(na) || i < len(nb); i++ {
		var x, y uint64
		if i < len(na) {
			x, _ = strconv.ParseUint(na[i], 10, 64)
		}
		if i < len(nb) {
			y, _ = strconv.ParseUint(nb[i], 10, 64)
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// ---------------------------------------------------------------- firma di una release già archiviata

// NomeSomme è il nome con cui si salvano le somme firmate di una release.
const NomeSomme = "SHA256SUMS.txt"

// CompletaFirma aggiunge a una release già archiviata i due file che la
// firmano e che allora non c'erano: chi pubblica carica SHA256SUMS.txt.sig
// dopo il run di Actions, quindi una release archiviata nel frattempo l'ha
// senza, Scarica non rifà una release già presente, e senza la firma
// l'installazione dall'archivio si rifiuta. somme e sig sono quelli che la
// chiamante ha già verificato con le chiavi di CentralHub; si scrivono solo se
// mancano o sono diversi (le somme firmate valgono più di quelle archiviate:
// se un pacchetto non torna, l'installazione lo dice). Restituisce i nomi dei
// file scritti, nessuno se l'archivio era già completo.
func CompletaFirma(cartella, tag string, somme []byte, sig string) ([]string, error) {
	rel := NomeCartella(tag)
	if !Presente(cartella, rel) {
		return nil, fmt.Errorf("la release %s non è nell'archivio", tag)
	}
	var scritti []string
	metti := func(nome string, contenuto []byte, uguali func(a, b []byte) bool) error {
		dest := filepath.Join(cartella, rel, nome)
		for _, f := range FileDellaRelease(cartella, rel) {
			if strings.EqualFold(f.Nome, nome) {
				// c'è già, magari con le maiuscole diverse: si riscrive quello
				dest = filepath.Join(cartella, rel, f.Nome)
				if b, err := os.ReadFile(dest); err == nil && uguali(b, contenuto) {
					return nil
				}
			}
		}
		if err := scriviAtomico(dest, contenuto); err != nil {
			return fmt.Errorf("%s non si è scritto nell'archivio: %w", filepath.Base(dest), err)
		}
		scritti = append(scritti, filepath.Base(dest))
		return nil
	}
	// prima le somme, poi la firma: non resta mai una firma senza le somme che firma
	if err := metti(NomeSomme, somme, bytes.Equal); err != nil {
		return scritti, err
	}
	err := metti(firma.NomeFile, []byte(strings.TrimSpace(sig)+"\n"), func(a, b []byte) bool {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	})
	return scritti, err
}

// scriviAtomico scrive un file piccolo nella sua cartella senza mai lasciarlo
// a metà: prima un file nascosto accanto, poi la rinomina.
func scriviAtomico(dest string, contenuto []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".completa-*")
	if err != nil {
		return err
	}
	nome := tmp.Name()
	_, err = tmp.Write(contenuto)
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Chmod(nome, 0o644)
	}
	if err == nil {
		err = os.Rename(nome, dest)
	}
	if err != nil {
		os.Remove(nome)
	}
	return err
}

// ---------------------------------------------------------------- dall'archivio

// FileArchiviato è un file di una release che sta nella cartella d'archivio.
type FileArchiviato struct {
	Nome       string
	Dimensione int64
}

// FileDellaRelease elenca i file di una release già archiviata (la
// sottocartella con il nome del tag), senza i nascosti.
func FileDellaRelease(cartella, cartellaRelease string) []FileArchiviato {
	voci, err := os.ReadDir(filepath.Join(cartella, cartellaRelease))
	if err != nil {
		return nil
	}
	var out []FileArchiviato
	for _, v := range voci {
		if !v.Type().IsRegular() || strings.HasPrefix(v.Name(), ".") {
			continue
		}
		var dim int64
		if fi, err := v.Info(); err == nil {
			dim = fi.Size()
		}
		out = append(out, FileArchiviato{v.Name(), dim})
	}
	return out
}

// SommeArchiviate legge il SHA256SUMS.txt che l'archiviazione ha scaricato
// insieme ai pacchetti, se c'è: nome -> somma.
func SommeArchiviate(cartella, cartellaRelease string) map[string]string {
	somme := map[string]string{}
	// prima quelle annotate dall'archiviazione, poi (che prevalgono) quelle
	// che la release stessa pubblicava
	if b, err := os.ReadFile(filepath.Join(cartella, cartellaRelease, nomeSommeArchivio)); err == nil && len(b) <= 1<<20 {
		for n, s := range github.AnalizzaSomme(string(b)) {
			somme[n] = s
		}
	}
	for _, f := range FileDellaRelease(cartella, cartellaRelease) {
		n := strings.ToLower(f.Nome)
		if n == "sha256sums.txt" || n == "sha256sums" {
			b, err := os.ReadFile(filepath.Join(cartella, cartellaRelease, f.Nome))
			if err == nil && len(b) <= 1<<20 {
				for n, s := range github.AnalizzaSomme(string(b)) {
					somme[n] = s
				}
			}
		}
	}
	return somme
}

// DallArchivio porta un pacchetto dell'archivio nella cartella di lavoro,
// perché l'installazione possa consumarlo senza toccare l'archivio. Se si
// conosce la somma attesa la verifica mentre copia; verificato dice se è
// successo. Il nome è quello del file, senza percorsi.
func DallArchivio(ctx context.Context, origine, cartella, sha256atteso string, av Avanzamento) (percorso string, verificato bool, err error) {
	n, err := nomeFile(filepath.Base(origine))
	if err != nil {
		return "", false, err
	}
	in, err := os.Open(origine)
	if err != nil {
		return "", false, fmt.Errorf("il file dell'archivio non si apre: %w", err)
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s non è un file dell'archivio", origine)
	}
	if err := os.MkdirAll(cartella, 0o700); err != nil {
		return "", false, err
	}
	percorso = filepath.Join(cartella, n)
	out, err := os.OpenFile(percorso, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return "", false, err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), &lettore{r: in, ctx: ctx, nome: n, totale: fi.Size(), av: av})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(percorso)
		return "", false, fmt.Errorf("copia dall'archivio interrotta: %w", err)
	}
	if atteso := strings.ToLower(strings.TrimPrefix(strings.ToLower(sha256atteso), "sha256:")); atteso != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != atteso {
			os.Remove(percorso)
			return "", false, &nonCorrisponde{got, atteso}
		}
		verificato = true
	}
	return percorso, verificato, nil
}

// DataRelease è il momento in cui la cartella della release è stata
// archiviata (la sua data di modifica); zero se non si sa.
func DataRelease(cartella, cartellaRelease string) time.Time {
	if fi, err := os.Stat(filepath.Join(cartella, cartellaRelease)); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// LeggiDallaRelease legge un piccolo file (somme, firma) dalla release
// archiviata, senza badare alle maiuscole nel nome.
func LeggiDallaRelease(cartella, cartellaRelease, nome string) ([]byte, bool) {
	for _, f := range FileDellaRelease(cartella, cartellaRelease) {
		if strings.EqualFold(f.Nome, nome) {
			b, err := os.ReadFile(filepath.Join(cartella, cartellaRelease, f.Nome))
			if err != nil || len(b) > 1<<20 {
				return nil, false
			}
			return b, true
		}
	}
	return nil, false
}
