// SPDX-License-Identifier: MIT

package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTagDaRedirect(t *testing.T) {
	for _, k := range []struct {
		stato int
		dove  string
		tag   string
		tipo  string // "" = nessun errore
	}{
		{302, "https://github.com/storytold/photocraft/releases/tag/v0.11.1", "v0.11.1", ""},
		{301, "/storytold/photocraft/releases/tag/v1.0.0", "v1.0.0", ""},
		{302, "https://github.com/o/r/releases/tag/release%2F2.0", "release/2.0", ""}, // un tag con la barra
		{302, "https://github.com/o/r/releases", "", "nessuna-release"},
		{302, "", "", "nessuna-release"},
		{404, "", "", "nessuna-release"},
		{200, "", "", "nessuna-release"},
		{302, "https://github.com/o/r/releases/tag/", "", "server"},
	} {
		tag, err := tagDaRedirect(k.stato, k.dove)
		var ge *Errore
		switch {
		case k.tipo == "" && (err != nil || tag != k.tag):
			t.Errorf("%d %q: %q %v, atteso %q", k.stato, k.dove, tag, err, k.tag)
		case k.tipo != "" && (!errors.As(err, &ge) || ge.Tipo != k.tipo):
			t.Errorf("%d %q: %v, atteso errore %s", k.stato, k.dove, err, k.tipo)
		}
	}
}

func TestTagPubblicoNonSegueIlRedirect(t *testing.T) {
	var richieste []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		richieste = append(richieste, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/a/App/releases/latest":
			http.Redirect(w, r, "/a/App/releases/tag/v3.1.4", http.StatusFound)
		case "/a/Vuoto/releases/latest":
			http.Redirect(w, r, "/a/Vuoto/releases", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := Nuovo("", "prova")
	c.WebDiProva = srv.URL
	if tag, err := c.TagPubblico(context.Background(), "a/App"); err != nil || tag != "v3.1.4" {
		t.Errorf("tag: %q %v", tag, err)
	}
	for _, r := range richieste {
		if strings.Contains(r, "/tag/") {
			t.Errorf("il redirect è stato seguito: %v", richieste)
		}
	}
	if _, err := c.TagPubblico(context.Background(), "a/Vuoto"); err == nil {
		t.Error("un repository senza release deve dare errore")
	}
	if _, err := c.TagPubblico(context.Background(), "a/Inesistente"); err == nil {
		t.Error("un repository che non c'è deve dare errore")
	}
	// con un token si usa l'API: la sonda non vale
	if _, err := Nuovo("segreto", "prova").TagPubblico(context.Background(), "a/App"); err == nil {
		t.Error("con un token la sonda del sito non si usa")
	}
}

func TestAnalizzaSomme(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("B", 64)
	testo := a + "  App-Windows.zip\n" +
		b + " *App Linux.AppImage\n" + // maiuscole e asterisco binario
		a + "  ./App-Intel.zip\n" +
		"non è una somma  x\n" +
		strings.Repeat("g", 64) + "  non-esadecimale\n" +
		a + "  ../fuori.zip\n" +
		a + "  sotto/cartella.zip\n"
	m := AnalizzaSomme(testo)
	if len(m) != 3 || m["App-Windows.zip"] != a || m["App Linux.AppImage"] != strings.ToLower(b) || m["App-Intel.zip"] != a {
		t.Errorf("somme: %v", m)
	}
}

func TestSommeEConSomme(t *testing.T) {
	somma := strings.Repeat("c", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(somma + "  a.zip\n"))
	}))
	defer srv.Close()
	c := Nuovo("", "prova")
	c.Base = srv.URL
	r := &Release{Tag: "v1", Assets: []Asset{{Nome: "a.zip"}, {Nome: "b.zip", Digest: "sha256:" + strings.Repeat("d", 64)}, {Nome: "sha256sums.TXT", URL: srv.URL + "/repos/a/App/releases/assets/7"}}}
	// senza il file delle somme: nil, nessun errore
	if m, err := c.Somme(context.Background(), "a/App", &Release{Assets: []Asset{{Nome: "a.zip"}}}); m != nil || err != nil {
		t.Errorf("senza file: %v %v", m, err)
	}
	m, err := c.Somme(context.Background(), "a/App", r)
	if err != nil || m["a.zip"] != somma {
		t.Fatalf("somme: %v %v", m, err)
	}
	r2 := r.ConSomme(m)
	if r2.Assets[0].Digest != "sha256:"+somma || r2.Assets[1].Digest != "sha256:"+strings.Repeat("d", 64) {
		t.Errorf("ConSomme: %+v", r2.Assets)
	}
	if r.Assets[0].Digest != "" {
		t.Error("ConSomme non deve toccare la release originale")
	}
}

func TestLeggiContentRange(t *testing.T) {
	for _, k := range []struct {
		v           string
		inizio, tot int64
		ok          bool
	}{{"bytes 100-199/1000", 100, 1000, true}, {"bytes 5-9/*", 5, -1, true}, {"bytes */1000", 0, 0, false}, {"", 0, 0, false}, {"bytes x-9/10", 0, 0, false}} {
		i, tot, ok := leggiContentRange(k.v)
		if ok != k.ok || (ok && (i != k.inizio || tot != k.tot)) {
			t.Errorf("%q: %d %d %v", k.v, i, tot, ok)
		}
	}
}

// Gli indirizzi dei file di una release si aprono solo se stanno fra i file di
// quel repository, sull'API o sul sito pubblico.
func TestIndirizzoLecito(t *testing.T) {
	const api, web = "https://api.github.com", "https://github.com"
	repo := "Owner/Repo"
	for indirizzo, atteso := range map[string]bool{
		api + "/repos/Owner/Repo/releases/assets/123":                                   true,
		api + "/repos/owner/repo/releases/assets/123":                                   true, // le maiuscole le sceglie GitHub
		api + "/repos/Owner/Repo/zipball/v1.0.0":                                        true,
		api + "/repos/Owner/Repo/tarball/v1.0.0":                                        true,
		api + "/repos/Owner/Repo/zipball/rilascio%2F1.0":                                true,  // tag con la barra
		"https://api.github.com/repos/Owner/Repo2/releases/assets/123":                  false, // un repository che comincia allo stesso modo
		api + "/repos/Owner/Altro/releases/assets/123":                                  false,
		api + "/repos/Owner/Repo/contents/README.md":                                    false, // non è un file di una release
		api + "/repos/Owner/Repo/releases/assets/":                                      false, // niente dopo il prefisso
		api + "/repos/Owner/Repo/releases/assets/../../../Altro/Repo/releases/assets/1": false,
		api + "/repos/Owner/Repo/releases/assets/%2e%2e/%2e%2e/Altro":                   false,
		api + "/repos/Owner/Repo/releases/assets//1":                                    false,
		api + "/repos/Owner/Repo/releases/assets/1?x=y":                                 false,
		api + "/repos/Owner/Repo/releases/assets/1#f":                                   false,
		"http://api.github.com/repos/Owner/Repo/releases/assets/1":                      false, // senza TLS
		"https://evil.example/repos/Owner/Repo/releases/assets/1":                       false,
		"https://api.github.com.evil.example/repos/Owner/Repo/releases/assets/1":        false,
		"https://api.github.com@evil.example/repos/Owner/Repo/releases/assets/1":        false,
		"https://api.github.com:8443/repos/Owner/Repo/releases/assets/1":                false,
		"https://user@api.github.com/repos/Owner/Repo/releases/assets/1":                false,
		api + "/repos/Owner/Repo/releases/assets/1\r\nX: y":                             false,
		api + "/repos/Owner/Repo\\releases/assets/1":                                    false,
		"":                                    false,
		"/repos/Owner/Repo/releases/assets/1": false, // relativo
		"file:///etc/passwd":                  false,
	} {
		if got := indirizzoLecito(api, indirizzo, prefissiAPI(repo)); got != atteso {
			t.Errorf("API %q: %v, atteso %v", indirizzo, got, atteso)
		}
	}
	for indirizzo, atteso := range map[string]bool{
		web + "/Owner/Repo/releases/download/v1.0.0/App.zip":                            true,
		web + "/owner/repo/releases/download/v1.0.0/App%20Uno.zip":                      true,
		web + "/Owner/Repo/archive/refs/tags/v1.0.0.zip":                                true,
		web + "/Owner/Repo/archive/refs/tags/v1.0.0.tar.gz":                             true,
		web + "/Owner/Altro/releases/download/v1.0.0/App.zip":                           false,
		web + "/Owner/Repo/raw/main/App.zip":                                            false,
		web + "/Owner/Repo/releases/download/../../../Altro/x":                          false,
		"https://objects.githubusercontent.com/Owner/Repo/releases/download/v1/App.zip": false,
		"https://evil.example/Owner/Repo/releases/download/v1.0.0/App.zip":              false,
	} {
		if got := indirizzoLecito(web, indirizzo, prefissiWeb(repo)); got != atteso {
			t.Errorf("sito %q: %v, atteso %v", indirizzo, got, atteso)
		}
	}
	// la base di prova ha un percorso suo (un sito finto sotto /web)
	if !indirizzoLecito("http://127.0.0.1:1/web", "http://127.0.0.1:1/web/Owner/Repo/releases/download/v1/A.zip", prefissiWeb(repo)) {
		t.Error("la base di prova con un percorso deve valere")
	}
	if indirizzoLecito("http://127.0.0.1:1/web", "http://127.0.0.1:1/Owner/Repo/releases/download/v1/A.zip", prefissiWeb(repo)) {
		t.Error("fuori dal percorso della base non vale")
	}
}

// Un indirizzo fuori posto non si apre e non riceve il token: nessuna
// richiesta parte verso l'altro host.
func TestIndirizzoFuoriPostoNonSiContatta(t *testing.T) {
	var arrivate atomic.Int32
	fuori := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivate.Add(1)
		w.Write([]byte("pacchetto"))
	}))
	defer fuori.Close()
	dentro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pacchetto"))
	}))
	defer dentro.Close()

	c := Nuovo("segreto", "prova")
	c.Base = dentro.URL
	ctx := context.Background()
	const repo = "a/App"
	for nome, apri := range map[string]func() error{
		"Apri": func() error {
			_, _, err := c.Apri(ctx, fuori.URL+"/repos/a/App/releases/assets/1", repo, true)
			return err
		},
		"ApriDa": func() error {
			_, err := c.ApriDa(ctx, fuori.URL+"/repos/a/App/releases/assets/1", repo, false, 0)
			return err
		},
		"ApriDa sito": func() error {
			c2 := Nuovo("", "prova")
			c2.WebDiProva = dentro.URL
			_, err := c2.ApriDa(ctx, fuori.URL+"/a/App/releases/download/v1/A.zip", repo, true, 0)
			return err
		},
		"ApriWeb": func() error {
			c2 := Nuovo("", "prova")
			c2.WebDiProva = dentro.URL
			_, _, err := c2.ApriWeb(ctx, fuori.URL+"/a/App/releases/download/v1/A.zip", repo)
			return err
		},
		"ApriWeb con il token (sito non usato)": func() error {
			_, _, err := c.ApriWeb(ctx, dentro.URL+"/a/App/releases/download/v1/A.zip", repo)
			return err
		},
	} {
		err := apri()
		var ge *Errore
		if !errors.As(err, &ge) || ge.Tipo != "indirizzo" {
			t.Errorf("%s: atteso un errore di indirizzo, avuto %v", nome, err)
		}
	}
	if n := arrivate.Load(); n != 0 {
		t.Errorf("l'altro host ha ricevuto %d richieste", n)
	}
	// uno giusto passa
	corpo, _, err := c.Apri(ctx, dentro.URL+"/repos/a/App/releases/assets/1", repo, true)
	if err != nil {
		t.Fatalf("indirizzo giusto: %v", err)
	}
	corpo.Close()
}

func TestIndirizziLeciti(t *testing.T) {
	c := Nuovo("", "prova")
	giusta := func() *Release {
		return &Release{Tag: "v1", Zipball: BasePredefinita + "/repos/o/r/zipball/v1", Tarball: BasePredefinita + "/repos/o/r/tarball/v1",
			Assets: []Asset{{Nome: "a.zip", URL: BasePredefinita + "/repos/O/R/releases/assets/9", Pubblico: WebPredefinito + "/O/R/releases/download/v1/a.zip"}}}
	}
	if !c.IndirizziLeciti("o/r", giusta()) {
		t.Error("una release giusta deve valere")
	}
	for nome, guasta := range map[string]func(*Release){
		"asset su un altro host":     func(r *Release) { r.Assets[0].URL = "https://evil.example/repos/o/r/releases/assets/9" },
		"asset di un altro repo":     func(r *Release) { r.Assets[0].URL = BasePredefinita + "/repos/o/x/releases/assets/9" },
		"indirizzo pubblico altrove": func(r *Release) { r.Assets[0].Pubblico = "https://evil.example/o/r/releases/download/v1/a.zip" },
		"zip dei sorgenti altrove":   func(r *Release) { r.Zipball = "https://evil.example/zip" },
		"tar dei sorgenti altrove":   func(r *Release) { r.Tarball = "https://evil.example/tar" },
	} {
		r := giusta()
		guasta(r)
		if c.IndirizziLeciti("o/r", r) {
			t.Errorf("%s: doveva essere rifiutata", nome)
		}
	}
	if c.IndirizziLeciti("o/r", nil) {
		t.Error("nessuna release non vale")
	}
	// con un token il sito pubblico non si usa: il suo indirizzo non conta
	ct := Nuovo("segreto", "prova")
	r := giusta()
	r.Assets[0].Pubblico = "https://qualunque.example/x"
	if !ct.IndirizziLeciti("o/r", r) {
		t.Error("con il token l'indirizzo pubblico non si usa e non deve far rifiutare la release")
	}
}
