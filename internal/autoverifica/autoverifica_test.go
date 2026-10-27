// SPDX-License-Identifier: MIT

package autoverifica

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"centralhub/internal/firma"
)

const (
	provaRepo     = "Trismegisto/App"
	provaVersione = "1.2.3"
	provaSistema  = "linux-amd64"
)

// pacchetto crea una cartella di programma finta e ne restituisce la radice.
func pacchetto(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	scrivi(t, r, "App", "eseguibile finto")
	scrivi(t, r, "dati/5.0/importato/classi.jsonl", "una classe\ndue classi\n")
	scrivi(t, r, "LICENZE/LICENSE", "MIT")
	return r
}

func scrivi(t *testing.T, radice, rel, contenuto string) {
	t.Helper()
	p := filepath.Join(radice, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(contenuto), 0o644); err != nil {
		t.Fatal(err)
	}
}

// elenco scrive «<sha256>  <percorso>» per tutti i file di radice tranne gli esclusi.
func elenco(t *testing.T, radice string, escludi ...string) string {
	t.Helper()
	var righe []string
	err := filepath.Walk(radice, func(p string, st os.FileInfo, err error) error {
		if err != nil || st.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(radice, p)
		rel = filepath.ToSlash(rel)
		if rel == NomeElenco {
			return nil
		}
		for _, e := range escludi {
			if rel == e {
				return nil
			}
		}
		s, err := sommaFile(p)
		if err != nil {
			return err
		}
		righe = append(righe, s+"  "+rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(righe)
	return strings.Join(righe, "\n") + "\n"
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// release finta: serve SHA256SUMS.txt, la firma e l'elenco del sistema.
type release struct {
	sums, sig, elenco string
	richieste         atomic.Int32
	srv               *httptest.Server
}

func nuovaRelease(t *testing.T, elencoCompleto string, priv ed25519.PrivateKey) *release {
	t.Helper()
	r := &release{elenco: elencoCompleto}
	r.sums = sha(elencoCompleto) + "  " + NomeElencoRelease(provaSistema) + "\n" +
		strings.Repeat("0", 64) + "  App-Linux.AppImage\n"
	var err error
	r.sig, err = firma.Firma(priv, provaRepo, "v"+provaVersione, []byte(r.sums))
	if err != nil {
		t.Fatal(err)
	}
	base := "/" + provaRepo + "/releases/download/v" + provaVersione + "/"
	mux := http.NewServeMux()
	servi := func(nome, corpo string) {
		mux.HandleFunc(base+nome, func(w http.ResponseWriter, _ *http.Request) {
			r.richieste.Add(1)
			w.Write([]byte(corpo))
		})
	}
	servi("SHA256SUMS.txt", r.sums)
	servi("SHA256SUMS.txt.sig", r.sig)
	servi(NomeElencoRelease(provaSistema), r.elenco)
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func config(radice string, rel *release, pub ed25519.PublicKey) Configurazione {
	c := Configurazione{Versione: provaVersione, Sistema: provaSistema, Radice: radice, Chiavi: []ed25519.PublicKey{pub}}
	if rel != nil {
		c.Repo = provaRepo
		c.URLBase = rel.srv.URL
	}
	return c
}

func TestSenzaElencoNonVerificabile(t *testing.T) {
	s := Verifica(context.Background(), Configurazione{Versione: provaVersione, Radice: t.TempDir()})
	if s.Esito != EsitoNonVerificabile {
		t.Fatalf("esito %q, voglio %q", s.Esito, EsitoNonVerificabile)
	}
}

func TestElencoIllegibileNonVerificabile(t *testing.T) {
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, "questo non è un elenco\n")
	if s := Verifica(context.Background(), Configurazione{Versione: provaVersione, Radice: r}); s.Esito != EsitoNonVerificabile {
		t.Fatalf("esito %q", s.Esito)
	}
}

func TestLocaleIntegroSenzaRepository(t *testing.T) {
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	s := Verifica(context.Background(), Configurazione{Versione: provaVersione, Radice: r})
	if s.Esito != EsitoIntegro || !strings.Contains(s.Titolo, "repository privato") || s.Controllati != 3 {
		t.Fatalf("%+v", s)
	}
}

// Nel bundle macOS l'elenco sta in Contents/Resources, ma i percorsi sono relativi al .app.
func TestElencoNelBundleMacOS(t *testing.T) {
	r := t.TempDir()
	scrivi(t, r, "Contents/MacOS/App", "eseguibile")
	scrivi(t, r, "Contents/Resources/dati/5.0/importato/classi.jsonl", "una classe\n")
	scrivi(t, r, "Contents/Resources/"+NomeElenco, elenco(t, r, "Contents/MacOS/App"))
	s := Verifica(context.Background(), Configurazione{Versione: provaVersione, Radice: r})
	if s.Esito != EsitoIntegro || s.Controllati != 1 {
		t.Fatalf("%+v", s)
	}
	scrivi(t, r, "Contents/Resources/dati/5.0/importato/classi.jsonl", "una classe MODIFICATA\n")
	if s := Verifica(context.Background(), Configurazione{Versione: provaVersione, Radice: r}); s.Esito != EsitoDiversi {
		t.Fatalf("file alterato nel bundle: %+v", s)
	}
}

func TestFileAlteratoEMancante(t *testing.T) {
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	scrivi(t, r, "dati/5.0/importato/classi.jsonl", "una classe\nTRE classi\n")
	if err := os.Remove(filepath.Join(r, "LICENZE", "LICENSE")); err != nil {
		t.Fatal(err)
	}
	s := Verifica(context.Background(), Configurazione{Versione: provaVersione, Radice: r})
	if s.Esito != EsitoDiversi {
		t.Fatalf("esito %q", s.Esito)
	}
	if len(s.Diversi) != 1 || s.Diversi[0] != "dati/5.0/importato/classi.jsonl" {
		t.Errorf("diversi %v", s.Diversi)
	}
	if len(s.Mancano) != 1 || s.Mancano[0] != "LICENZE/LICENSE" {
		t.Errorf("mancano %v", s.Mancano)
	}
}

func TestCatenaFirmataECache(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	dati := t.TempDir()
	c := config(r, rel, pub)
	c.CartellaDati = dati

	s := Verifica(context.Background(), c)
	if s.Esito != EsitoIntegroFirmato {
		t.Fatalf("%+v", s)
	}
	if rel.richieste.Load() != 3 {
		t.Errorf("richieste %d, volute 3", rel.richieste.Load())
	}
	// seconda volta: la versione è già verificata, la rete non serve
	rel.srv.Close()
	rel.richieste.Store(0)
	if s := Verifica(context.Background(), c); s.Esito != EsitoIntegroFirmato {
		t.Fatalf("con la cache: %+v", s)
	}
	if rel.richieste.Load() != 0 {
		t.Errorf("con la cache la rete è stata toccata")
	}
	// la cache manomessa si ignora (qui la rete non c'è: ripiega sul locale)
	b, _ := os.ReadFile(filepath.Join(dati, nomeCache))
	manomessa := strings.Replace(string(b), "LICENZE/LICENSE", "LICENZE/LICENSX", 1)
	if manomessa == string(b) {
		t.Fatal("la prova non ha alterato la cache")
	}
	if err := os.WriteFile(filepath.Join(dati, nomeCache), []byte(manomessa), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := Verifica(context.Background(), c); s.Esito != EsitoIntegro || !strings.Contains(s.Titolo, "nessuna rete") {
		t.Fatalf("cache manomessa: %+v", s)
	}
}

func TestCacheDiUnaltraVersioneNonVale(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	c := config(r, rel, pub)
	c.CartellaDati = t.TempDir()
	if s := Verifica(context.Background(), c); s.Esito != EsitoIntegroFirmato {
		t.Fatalf("%+v", s)
	}
	rel.srv.Close()
	c.Versione = "9.9.9"
	if s := Verifica(context.Background(), c); s.Esito != EsitoIntegro {
		t.Fatalf("la cache di un'altra versione è stata usata: %+v", s)
	}
}

// Su macOS l'elenco nel pacchetto non può contenere l'eseguibile (la firma del
// sistema lo cambia dopo): lo scopre l'elenco della release.
func TestEseguibileCambiatoLoScopreLaRelease(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	completo := elenco(t, r)
	scrivi(t, r, NomeElenco, elenco(t, r, "App"))
	rel := nuovaRelease(t, completo, priv)
	if s := Verifica(context.Background(), config(r, rel, pub)); s.Esito != EsitoIntegroFirmato {
		t.Fatalf("prima: %+v", s)
	}
	scrivi(t, r, "App", "eseguibile SOSTITUITO")
	s := Verifica(context.Background(), config(r, rel, pub))
	if s.Esito != EsitoDiversi || len(s.Diversi) != 1 || s.Diversi[0] != "App" {
		t.Fatalf("dopo: %+v", s)
	}
}

func TestFirmaDiUnaltraChiave(t *testing.T) {
	pub, _, _ := firma.Nuova()
	_, estranea, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), estranea)
	s := Verifica(context.Background(), config(r, rel, pub))
	if s.Esito != EsitoFirmaNonValida {
		t.Fatalf("%+v", s)
	}
}

func TestFirmaDiUnaltraRelease(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	// l'attaccante ricarica la firma di un'altra versione, buona ma non di questa
	rel.srv.Config.Handler = func() http.Handler {
		mux := http.NewServeMux()
		base := "/" + provaRepo + "/releases/download/v" + provaVersione + "/"
		mux.HandleFunc(base+"SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(rel.sums)) })
		vecchia, _ := firma.Firma(priv, provaRepo, "v0.0.1", []byte(rel.sums))
		mux.HandleFunc(base+"SHA256SUMS.txt.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(vecchia)) })
		mux.HandleFunc(base+NomeElencoRelease(provaSistema), func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(rel.elenco)) })
		return mux
	}()
	if s := Verifica(context.Background(), config(r, rel, pub)); s.Esito != EsitoFirmaNonValida {
		t.Fatalf("%+v", s)
	}
}

func TestElencoNonCopertoDallaFirma(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	// l'elenco servito non è quello di cui le somme firmate parlano
	rel.elenco = strings.Replace(rel.elenco, "  App", "  Altro", 1)
	rel.srv.Config.Handler = func() http.Handler {
		mux := http.NewServeMux()
		base := "/" + provaRepo + "/releases/download/v" + provaVersione + "/"
		mux.HandleFunc(base+"SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(rel.sums)) })
		mux.HandleFunc(base+"SHA256SUMS.txt.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(rel.sig)) })
		mux.HandleFunc(base+NomeElencoRelease(provaSistema), func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(rel.elenco)) })
		return mux
	}()
	s := Verifica(context.Background(), config(r, rel, pub))
	if s.Esito != EsitoFirmaNonValida || !strings.Contains(s.Dettaglio, "coperto dalla firma") {
		t.Fatalf("%+v", s)
	}
}

func TestNessunaRete(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	rel.srv.Close()
	s := Verifica(context.Background(), config(r, rel, pub))
	if s.Esito != EsitoIntegro || !strings.Contains(s.Titolo, "nessuna rete") {
		t.Fatalf("%+v", s)
	}
}

// L'esito del confronto locale arriva prima di toccare la rete: con una rete
// lenta l'utente (e il file di stato) non aspettano il controllo della firma.
func TestAvanzamentoPrecedeLaRete(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	c := config(r, rel, pub)
	var visti []Stato
	var richiesteAlMomento int32 = -1
	c.Avanzamento = func(s Stato) {
		visti = append(visti, s)
		richiesteAlMomento = rel.richieste.Load()
	}
	fin := Verifica(context.Background(), c)
	if fin.Esito != EsitoIntegroFirmato {
		t.Fatalf("finale: %+v", fin)
	}
	if len(visti) != 1 || visti[0].Esito != EsitoIntegro || !strings.Contains(visti[0].Titolo, "in corso") {
		t.Fatalf("avanzamento: %+v", visti)
	}
	if richiesteAlMomento != 0 {
		t.Errorf("la rete era già stata toccata (%d richieste) quando è arrivato l'avanzamento", richiesteAlMomento)
	}
	// niente avanzamento se il confronto locale fallisce, o se non c'è rete da usare
	visti = nil
	scrivi(t, r, "LICENZE/LICENSE", "ALTRO")
	if s := Verifica(context.Background(), c); s.Esito != EsitoDiversi || len(visti) != 0 {
		t.Errorf("file diverso: %+v, avanzamenti %d", s, len(visti))
	}
}

func TestContestoAnnullato(t *testing.T) {
	pub, priv, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	rel := nuovaRelease(t, elenco(t, r), priv)
	ctx, annulla := context.WithCancel(context.Background())
	annulla()
	if s := Verifica(ctx, config(r, rel, pub)); s.Esito != EsitoIntegro {
		t.Fatalf("%+v", s)
	}
}

func TestReleaseNonFirmata(t *testing.T) {
	pub, _, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := config(r, nil, pub)
	c.Repo, c.URLBase = provaRepo, srv.URL
	s := Verifica(context.Background(), c)
	if s.Esito != EsitoIntegro || !strings.Contains(s.Titolo, "release non firmata") {
		t.Fatalf("%+v", s)
	}
}

func TestFileDiFirmaInAltroFormato(t *testing.T) {
	pub, _, _ := firma.Nuova()
	r := pacchetto(t)
	scrivi(t, r, NomeElenco, elenco(t, r))
	base := "/" + provaRepo + "/releases/download/v" + provaVersione + "/"
	mux := http.NewServeMux()
	mux.HandleFunc(base+"SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("x")) })
	mux.HandleFunc(base+"SHA256SUMS.txt.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("untrusted comment: minisign")) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := config(r, nil, pub)
	c.Repo, c.URLBase = provaRepo, srv.URL
	if s := Verifica(context.Background(), c); s.Esito != EsitoIntegro || !strings.Contains(s.Titolo, "release non firmata") {
		t.Fatalf("%+v", s)
	}
}

func TestLeggiElencoRifiutaPercorsiPericolosi(t *testing.T) {
	z := strings.Repeat("a", 64)
	for _, p := range []string{"../fuori", "/assoluto", "a/../b", "a//b", "./a", `a\b`, "C:/x", "a/", ""} {
		if _, err := leggiElenco([]byte(z + "  " + p + "\n")); err == nil {
			t.Errorf("percorso %q accettato", p)
		}
	}
	if _, err := leggiElenco([]byte(z + "  a\n" + z + "  a\n")); err == nil {
		t.Error("percorso doppio accettato")
	}
	if _, err := leggiElenco([]byte(strings.Repeat("g", 64) + "  a\n")); err == nil {
		t.Error("somma non esadecimale accettata")
	}
	v, err := leggiElenco([]byte("# commento\n\n" + z + "  Contents/MacOS/App\r\n"))
	if err != nil || len(v) != 1 || v[0].Percorso != "Contents/MacOS/App" {
		t.Errorf("elenco valido: %v %v", v, err)
	}
}

func TestSommaDi(t *testing.T) {
	z1, z2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	somme := []byte(z1 + "  a.zip\n" + z2 + " *INTEGRITA-linux-amd64.txt\n")
	if s, err := sommaDi(somme, "INTEGRITA-linux-amd64.txt"); err != nil || s != z2 {
		t.Errorf("%q %v", s, err)
	}
	if _, err := sommaDi(somme, "INTEGRITA-windows-amd64.txt"); err == nil {
		t.Error("file non elencato trovato")
	}
}

func TestRadiceDi(t *testing.T) {
	// macOS: il bundle .app
	app := filepath.Join(string(filepath.Separator), "Applicazioni", "The.app")
	if got := radiceDi(filepath.Join(app, "Contents", "MacOS", "The"), "darwin", ""); got != app {
		t.Errorf("macOS: %q", got)
	}
	// Windows: la cartella dell'eseguibile
	w := filepath.Join(string(filepath.Separator), "Programmi", "The")
	if got := radiceDi(filepath.Join(w, "The.exe"), "windows", ""); got != w {
		t.Errorf("Windows: %q", got)
	}
	// Linux: la AppDir dell'AppImage, da APPDIR o risalendo da usr/bin
	dir := t.TempDir()
	scrivi(t, dir, NomeElenco, "x")
	scrivi(t, dir, "usr/bin/The", "x")
	exe := filepath.Join(dir, "usr", "bin", "The")
	if got := radiceDi(exe, "linux", ""); got != dir {
		t.Errorf("Linux da usr/bin: %q", got)
	}
	if got := radiceDi("/altro/posto/The", "linux", dir); got != dir {
		t.Errorf("Linux da APPDIR: %q", got)
	}
	// Linux senza elenco: la cartella dell'eseguibile
	nuda := t.TempDir()
	if got := radiceDi(filepath.Join(nuda, "The"), "linux", ""); got != nuda {
		t.Errorf("Linux senza elenco: %q", got)
	}
}

func TestSistemaCorrente(t *testing.T) {
	s := SistemaCorrente()
	if strings.Contains(s, "darwin") || !strings.Contains(s, "-") {
		t.Errorf("sistema %q", s)
	}
}

func TestScriviStato(t *testing.T) {
	d := t.TempDir()
	if err := ScriviStato(filepath.Join(d, "cartella-nuova"), Stato{Esito: EsitoIntegro, Titolo: "x"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(d, "cartella-nuova", NomeFileStato))
	if err != nil || !strings.Contains(string(b), `"esito": "integro"`) {
		t.Fatalf("%s %v", b, err)
	}
}
