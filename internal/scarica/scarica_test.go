// SPDX-License-Identifier: MIT

package scarica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"centralhub/internal/github"
)

// finto è un GitHub di prova: l'API su un server, i file su un altro (come
// fa GitHub, che rimanda gli asset a un dominio di storage).
type finto struct {
	api, storage *httptest.Server
	contenuti    map[string][]byte // nome asset -> contenuto
	digest       map[string]string
	tronca       string // asset servito a metà
	token        string // se non vuoto, l'API lo pretende
	tokenFuori   atomic.Bool
	stato        int // se non zero, risposta fissa dell'API a /releases/latest
	intestazioni map[string]string
	mu           sync.Mutex
	serviti      map[string]int // quante volte lo storage ha servito ogni file
	apiAsset     atomic.Int32   // richieste di file passate dall'API
}

func nuovoFinto(t *testing.T) *finto {
	t.Helper()
	f := &finto{contenuti: map[string][]byte{}, digest: map[string]string{}, serviti: map[string]int{}}
	f.storage = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			f.tokenFuori.Store(true)
		}
		nome := strings.TrimPrefix(r.URL.Path, "/")
		// gli indirizzi pubblici degli archivi dei sorgenti
		switch {
		case strings.HasSuffix(nome, "/archive/refs/tags/v1.2.0.zip"):
			nome = "sorgente.zip"
		case strings.HasSuffix(nome, "/archive/refs/tags/v1.2.0.tar.gz"):
			nome = "sorgente.tar.gz"
		}
		b, ok := f.contenuti[nome]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.serviti[nome]++
		f.mu.Unlock()
		if nome == f.tronca {
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			w.Write(b[:len(b)/2])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			panic(http.ErrAbortHandler) // chiude la connessione a metà
		}
		w.Write(b)
	}))
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusNotFound) // GitHub nasconde i privati
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			if f.stato != 0 {
				for k, v := range f.intestazioni {
					w.Header().Set(k, v)
				}
				w.WriteHeader(f.stato)
				w.Write([]byte(`{"message":"prova"}`))
				return
			}
			rel := github.Release{Tag: "v1.2.0", Nome: "Versione 1.2.0",
				Zipball: f.api.URL + "/sorgente/zip", Tarball: f.api.URL + "/sorgente/tar"}
			// in ordine di nome: l'ordine di una mappa cambierebbe da una prova all'altra
			nomi := make([]string, 0, len(f.contenuti))
			for nome := range f.contenuti {
				nomi = append(nomi, nome)
			}
			sort.Strings(nomi)
			for _, nome := range nomi {
				b := f.contenuti[nome]
				if strings.HasPrefix(nome, "sorgente") {
					continue
				}
				rel.Assets = append(rel.Assets, github.Asset{Nome: nome, Dimensione: int64(len(b)),
					URL: f.api.URL + "/asset/" + nome, Digest: f.digest[nome], Pubblico: f.storage.URL + "/" + nome})
			}
			json.NewEncoder(w).Encode(rel)
		case strings.HasPrefix(r.URL.Path, "/asset/"):
			f.apiAsset.Add(1)
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "serve Accept: application/octet-stream", http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, f.storage.URL+"/"+strings.TrimPrefix(r.URL.Path, "/asset/"), http.StatusFound)
		case r.URL.Path == "/sorgente/zip":
			f.apiAsset.Add(1)
			http.Redirect(w, r, f.storage.URL+"/sorgente.zip", http.StatusFound)
		case r.URL.Path == "/sorgente/tar":
			f.apiAsset.Add(1)
			http.Redirect(w, r, f.storage.URL+"/sorgente.tar.gz", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { f.api.Close(); f.storage.Close() })
	f.contenuti["sorgente.zip"] = []byte("zip dei sorgenti")
	f.contenuti["sorgente.tar.gz"] = []byte("tar dei sorgenti")
	return f
}

func (f *finto) aggiungi(nome, contenuto string) {
	f.contenuti[nome] = []byte(contenuto)
	s := sha256.Sum256([]byte(contenuto))
	f.digest[nome] = "sha256:" + hex.EncodeToString(s[:])
}

func (f *finto) client(token string) *github.Client {
	c := github.Nuovo(token, "CentralHub-prova")
	c.Base = f.api.URL
	return c
}

func elenco(t *testing.T, dir string) []string {
	t.Helper()
	voci, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, v := range voci {
		n = append(n, v.Name())
	}
	return n
}

func TestScaricaTuttiGliAssetSeparati(t *testing.T) {
	f := nuovoFinto(t)
	f.aggiungi("App-Windows.zip", strings.Repeat("W", 600_000))
	f.aggiungi("App-Linux-x86_64.AppImage", "appimage")
	f.token = "segreto"
	c := f.client("segreto")
	master := filepath.Join(t.TempDir(), "master")

	r, err := c.Ultima(context.Background(), "a/App")
	if err != nil {
		t.Fatal(err)
	}
	var avvisi int
	ris, err := Scarica(context.Background(), c, "a/App", r, master, func(string, int64, int64) { avvisi++ })
	if err != nil {
		t.Fatal(err)
	}
	if ris.GiaPresente || filepath.Base(ris.Cartella) != "v1.2.0" {
		t.Errorf("risultato: %+v", ris)
	}
	got := strings.Join(elenco(t, ris.Cartella), " ")
	atteso := "App-Linux-x86_64.AppImage App-Windows.zip App-v1.2.0-sorgente.tar.gz App-v1.2.0-sorgente.zip"
	if got != atteso {
		t.Errorf("file scaricati:\n %s\natteso:\n %s", got, atteso)
	}
	b, _ := os.ReadFile(filepath.Join(ris.Cartella, "App-Windows.zip"))
	if len(b) != 600_000 {
		t.Errorf("asset di %d byte", len(b))
	}
	if avvisi < 2 {
		t.Errorf("avanzamento segnalato %d volte", avvisi)
	}
	if f.tokenFuori.Load() {
		t.Error("il token è stato mandato al dominio di storage: non deve uscire da GitHub")
	}
	if n := elenco(t, master); len(n) != 1 {
		t.Errorf("nella cartella master deve restare solo la release: %v", n)
	}

	// seconda volta: già scaricata, non si tocca niente
	ris2, err := Scarica(context.Background(), c, "a/App", r, master, nil)
	if err != nil || !ris2.GiaPresente {
		t.Errorf("release già presente: %+v, %v", ris2, err)
	}
}

func TestDownloadInterrottoRiparteDaDoveEra(t *testing.T) {
	f := nuovoFinto(t)
	f.aggiungi("a-buono.zip", "contenuto buono")
	f.aggiungi("b-rotto.zip", strings.Repeat("x", 100_000))
	f.tronca = "b-rotto.zip"
	c := f.client("")
	master := t.TempDir()
	r, err := c.Ultima(context.Background(), "a/App")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scarica(context.Background(), c, "a/App", r, master, nil); err == nil {
		t.Fatal("un asset a metà deve far fallire il download")
	}
	// la release non compare; resta solo la cartella provvisoria nascosta,
	// con i file arrivati interi e senza quello a metà
	if n := strings.Join(elenco(t, master), " "); n != ".v1.2.0.parziale" {
		t.Fatalf("nella cartella master: %q", n)
	}
	if Presente(master, r.Tag) || len(Scaricate(master)) != 0 {
		t.Error("una release a metà non deve risultare scaricata")
	}
	da := elenco(t, filepath.Join(master, ".v1.2.0.parziale"))
	if strings.Join(da, " ") != "a-buono.zip" || TenutiDaParte(master, r.Tag) != 1 {
		t.Errorf("tenuti da parte: %v", da)
	}
	// il tentativo successivo non riscarica ciò che c'è già
	f.tronca = ""
	ris, err := Scarica(context.Background(), c, "a/App", r, master, nil)
	if err != nil {
		t.Fatalf("secondo tentativo: %v", err)
	}
	if f.serviti["a-buono.zip"] != 1 {
		t.Errorf("il file già scaricato è stato chiesto %d volte", f.serviti["a-buono.zip"])
	}
	if len(ris.File) != 4 || len(elenco(t, ris.Cartella)) != 4 || TenutiDaParte(master, r.Tag) != 0 {
		t.Errorf("release completa: %v", ris.File)
	}
	if n := strings.Join(elenco(t, master), " "); n != "v1.2.0" {
		t.Errorf("a download finito resta solo la release: %q", n)
	}
}

func TestFileTenutoDaParteMaRovinato(t *testing.T) {
	f := nuovoFinto(t)
	f.aggiungi("App.zip", "contenuto giusto")
	c := f.client("")
	master := t.TempDir()
	r, _ := c.Ultima(context.Background(), "a/App")
	// nella cartella provvisoria: un file della stessa lunghezza ma diverso,
	// e uno che con questa release non c'entra
	tmp := filepath.Join(master, ".v1.2.0.parziale")
	os.MkdirAll(tmp, 0o755)
	os.WriteFile(filepath.Join(tmp, "App.zip"), []byte("contenuto guasto"), 0o644)
	os.WriteFile(filepath.Join(tmp, "estraneo.bin"), []byte("x"), 0o644)
	// e il tentativo a metà di una release più vecchia
	os.MkdirAll(filepath.Join(master, ".v1.1.0.parziale"), 0o755)
	ris, err := Scarica(context.Background(), c, "a/App", r, master, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ris.Cartella, "App.zip")); string(b) != "contenuto giusto" {
		t.Errorf("un file tenuto da parte ma diverso da quello pubblicato va riscaricato: %q", b)
	}
	if n := strings.Join(elenco(t, ris.Cartella), " "); strings.Contains(n, "estraneo") {
		t.Errorf("file estraneo finito nella release: %s", n)
	}
	if n := strings.Join(elenco(t, master), " "); n != "v1.2.0" {
		t.Errorf("i tentativi di altre release vanno tolti: %q", n)
	}
}

func TestDigestSbagliatoRifiutato(t *testing.T) {
	f := nuovoFinto(t)
	f.aggiungi("App.zip", "contenuto")
	f.digest["App.zip"] = "sha256:" + strings.Repeat("0", 64)
	c := f.client("")
	master := t.TempDir()
	r, _ := c.Ultima(context.Background(), "a/App")
	_, err := Scarica(context.Background(), c, "a/App", r, master, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("atteso errore di SHA-256, avuto %v", err)
	}
	if Presente(master, r.Tag) || TenutiDaParte(master, r.Tag) != 0 {
		t.Errorf("un file che non corrisponde non si tiene: %v", elenco(t, filepath.Join(master, ".v1.2.0.parziale")))
	}
}

func TestAnnullamento(t *testing.T) {
	f := nuovoFinto(t)
	f.aggiungi("App.zip", strings.Repeat("x", 2_000_000))
	c := f.client("")
	master := t.TempDir()
	r, _ := c.Ultima(context.Background(), "a/App")
	ctx, annulla := context.WithCancel(context.Background())
	_, err := Scarica(ctx, c, "a/App", r, master, func(string, int64, int64) { annulla() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("atteso annullamento, avuto %v", err)
	}
	if Presente(master, r.Tag) || TenutiDaParte(master, r.Tag) != 0 {
		t.Errorf("dopo l'annullamento non resta nessun file a metà: %v", elenco(t, filepath.Join(master, ".v1.2.0.parziale")))
	}
}

// Senza token i file si prendono dal sito pubblico: l'API serve una volta
// sola, per sapere qual è l'ultima release. È ciò che evita il limite di 60
// richieste all'ora (una release di Storytold ha 26 file).
func TestRepositoryPubblicoNonConsumaLAPI(t *testing.T) {
	f := nuovoFinto(t)
	for i := 0; i < 26; i++ {
		f.aggiungi(fmt.Sprintf("pacchetto-%02d.bin", i), fmt.Sprintf("contenuto %d", i))
	}
	c := f.client("")
	c.WebDiProva = f.storage.URL
	master := t.TempDir()
	r, err := c.Ultima(context.Background(), "storytold/photocraft")
	if err != nil {
		t.Fatal(err)
	}
	ris, err := Scarica(context.Background(), c, "storytold/photocraft", r, master, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ris.File) != 28 {
		t.Errorf("scaricati %d file, attesi 26 pacchetti e 2 archivi dei sorgenti", len(ris.File))
	}
	if n := f.apiAsset.Load(); n != 0 {
		t.Errorf("%d file sono passati dall'API: per un repository pubblico devono essere zero", n)
	}
	if b, _ := os.ReadFile(filepath.Join(ris.Cartella, "photocraft-v1.2.0-sorgente.zip")); string(b) != "zip dei sorgenti" {
		t.Errorf("archivio dei sorgenti dal sito: %q", b)
	}
	// con un token si resta sull'API: è l'unica strada per i privati
	f.token = "segreto"
	p := f.client("segreto")
	p.WebDiProva = f.storage.URL
	r, _ = p.Ultima(context.Background(), "a/Privato")
	if _, err := Scarica(context.Background(), p, "a/Privato", r, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if n := f.apiAsset.Load(); n != 28 {
		t.Errorf("con il token i file passano dall'API: %d su 28", n)
	}
	if f.tokenFuori.Load() {
		t.Error("il token è arrivato allo storage")
	}
}

func TestNomiDiAssetPericolosi(t *testing.T) {
	for _, n := range []string{"../fuori.zip", "sotto/cartella.zip", `C:\x.zip`, ".nascosto", "", ".."} {
		if _, err := nomeFile(n); err == nil {
			t.Errorf("%q doveva essere rifiutato", n)
		}
	}
	if n, err := nomeFile("App-1.0 (finale).zip"); err != nil || n != "App-1.0 (finale).zip" {
		t.Errorf("nome normale rifiutato: %q %v", n, err)
	}
}

func TestErroriSpiegati(t *testing.T) {
	casi := []struct {
		stato  int
		h      map[string]string
		token  string
		tipo   string
		dentro string
	}{
		{404, nil, "", "nessuna-release", "privato"},
		{404, nil, "t", "nessuna-release", "token può leggere"},
		{401, nil, "t", "token", "scaduto"},
		{403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1790000000"}, "", "limite", "60 all'ora"},
		{429, map[string]string{"Retry-After": "60"}, "t", "limite", "limite di richieste"},
		{403, nil, "t", "accesso", "Contents"},
		{500, nil, "", "server", "500"},
	}
	for _, k := range casi {
		f := nuovoFinto(t)
		f.stato, f.intestazioni = k.stato, k.h
		_, err := f.client(k.token).Ultima(context.Background(), "a/App")
		var e *github.Errore
		if !errors.As(err, &e) || e.Tipo != k.tipo || !strings.Contains(e.Messaggio, k.dentro) {
			t.Errorf("stato %d: %v (atteso tipo %s con %q)", k.stato, err, k.tipo, k.dentro)
		}
	}
	// rete assente
	c := github.Nuovo("", "x")
	c.Base = "http://127.0.0.1:1"
	_, err := c.Ultima(context.Background(), "a/App")
	var e *github.Errore
	if !errors.As(err, &e) || e.Tipo != "rete" {
		t.Errorf("rete assente: %v", err)
	}
}

func TestPrivatoSenzaToken(t *testing.T) {
	f := nuovoFinto(t)
	f.aggiungi("App.zip", "x")
	f.token = "segreto"
	if _, err := f.client("").Ultima(context.Background(), "a/App"); err == nil {
		t.Error("repo privato senza token: deve fallire")
	}
	if _, err := f.client("segreto").Ultima(context.Background(), "a/App"); err != nil {
		t.Errorf("repo privato con token: %v", err)
	}
}

func TestPulisciParzialiToccaSoloQuelli(t *testing.T) {
	master := t.TempDir()
	for _, d := range []string{".v1.0.0.parziale", ".v1.1.0.parziale", "v0.9.0", ".altro", "normale.parziale"} {
		os.MkdirAll(filepath.Join(master, d, "dentro"), 0o755)
	}
	os.WriteFile(filepath.Join(master, ".file.parziale"), []byte("x"), 0o644)
	// si tiene quello della release in corso, si tolgono gli altri
	tolte := PulisciParziali(master, "v1.1.0")
	if len(tolte) != 1 || tolte[0] != ".v1.0.0.parziale" {
		t.Errorf("tolte: %v", tolte)
	}
	got := strings.Join(elenco(t, master), " ")
	if got != ".altro .file.parziale .v1.1.0.parziale normale.parziale v0.9.0" {
		t.Errorf("rimasto: %s", got)
	}
}

func TestVersioniEScaricate(t *testing.T) {
	for _, k := range []struct {
		a, b string
		c    int
	}{{"v1.10.0", "v1.9.3", 1}, {"v2.0.0", "v2.0.1", -1}, {"1.0", "v1.0.0", 0}, {"v2", "v1.9", 1}, {"stabile", "v1", 0}} {
		if got := ConfrontaVersioni(k.a, k.b); got != k.c {
			t.Errorf("ConfrontaVersioni(%s, %s) = %d, atteso %d", k.a, k.b, got, k.c)
		}
	}
	master := t.TempDir()
	for _, d := range []string{"v1.9.3", "v1.10.0", "v0.2.0", ".v2.0.0.parziale"} {
		os.Mkdir(filepath.Join(master, d), 0o755)
	}
	os.WriteFile(filepath.Join(master, "v9.9.9"), []byte("un file, non una release"), 0o644)
	got := strings.Join(Scaricate(master), " ")
	if got != "v1.10.0 v1.9.3 v0.2.0" {
		t.Errorf("Scaricate: %s", got)
	}
	if NomeCartella(" v1.0/beta 2 ") != "v1.0-beta-2" || NomeCartella("../..") != "release" {
		t.Errorf("NomeCartella: %q %q", NomeCartella(" v1.0/beta 2 "), NomeCartella("../.."))
	}
}
