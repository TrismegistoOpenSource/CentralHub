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
}

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
	voluti := map[string]bool{}
	for _, s := range elenco {
		voluti[s.nome] = true
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
	if err := os.Rename(tmp, dest); err != nil {
		return Risultato{}, err
	}
	return Risultato{Cartella: dest, File: nomi}, nil
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
	f, err := os.Open(percorso)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == atteso
}

// TenutiDaParte dice quanti file di una release sono rimasti nella cartella
// provvisoria dopo un download non finito.
func TenutiDaParte(cartella, tag string) int {
	voci, err := os.ReadDir(parziale(cartella, tag))
	if err != nil {
		return 0
	}
	return len(voci)
}

func unFile(ctx context.Context, c *github.Client, repo string, s sorgente, percorso string, av Avanzamento) error {
	var corpo io.ReadCloser
	var lunghezza int64
	var err error
	if s.web {
		corpo, lunghezza, err = c.ApriWeb(ctx, s.indirizzo)
	} else {
		corpo, lunghezza, err = c.Apri(ctx, s.indirizzo, repo, s.asset)
	}
	if err != nil {
		return err
	}
	defer corpo.Close()
	totale := s.dimensione
	if totale <= 0 && lunghezza > 0 {
		totale = lunghezza
	}
	f, err := os.OpenFile(percorso, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), &lettore{r: corpo, ctx: ctx, nome: s.nome, totale: totale, av: av})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download interrotto: %w", err)
	}
	if s.dimensione > 0 && n != s.dimensione {
		return fmt.Errorf("file incompleto: %d byte su %d", n, s.dimensione)
	}
	if atteso, si := strings.CutPrefix(strings.ToLower(s.digest), "sha256:"); si {
		if got := hex.EncodeToString(h.Sum(nil)); got != atteso {
			return fmt.Errorf("il file scaricato non corrisponde a quello pubblicato (SHA-256 %s, atteso %s)", got, atteso)
		}
	}
	return nil
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
