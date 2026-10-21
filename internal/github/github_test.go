// SPDX-License-Identifier: MIT

package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	r := &Release{Tag: "v1", Assets: []Asset{{Nome: "a.zip"}, {Nome: "b.zip", Digest: "sha256:" + strings.Repeat("d", 64)}, {Nome: "sha256sums.TXT", URL: srv.URL}}}
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
