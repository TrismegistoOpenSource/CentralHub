// SPDX-License-Identifier: MIT

package scarica

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"centralhub/internal/github"
)

// La prova contro il GitHub vero. Non gira mai da sola (le prove normali non
// toccano la rete): va chiesta, indicando un repository pubblico.
//
//	CENTRALHUB_PROVA_VERA=proprietario/nome go test ./internal/scarica -run TestGitHubVero -v
//
// Con CENTRALHUB_PROVA_TOKEN si prova anche un repository privato.
func TestGitHubVero(t *testing.T) {
	repo := os.Getenv("CENTRALHUB_PROVA_VERA")
	if repo == "" {
		t.Skip("prova contro il GitHub vero non richiesta (CENTRALHUB_PROVA_VERA)")
	}
	ctx, annulla := context.WithTimeout(context.Background(), 5*time.Minute)
	defer annulla()
	c := github.Nuovo(os.Getenv("CENTRALHUB_PROVA_TOKEN"), "CentralHub-prova")
	r, err := c.Ultima(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ultima release di %s: %s (%q), %d asset", repo, r.Tag, r.Nome, len(r.Assets))
	for _, a := range r.Assets {
		t.Logf("  %s  %d byte  %s", a.Nome, a.Dimensione, a.Digest)
	}
	master := t.TempDir()
	ris, err := Scarica(ctx, c, repo, r, master, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ris.File) < len(r.Assets)+2 {
		t.Errorf("scaricati %d file: attesi gli asset più i due archivi dei sorgenti", len(ris.File))
	}
	for _, n := range ris.File {
		fi, err := os.Stat(filepath.Join(ris.Cartella, n))
		if err != nil || fi.Size() == 0 {
			t.Errorf("%s: %v", n, err)
			continue
		}
		t.Logf("scaricato %s (%d byte)", n, fi.Size())
	}
	if !Presente(master, r.Tag) || TenutiDaParte(master, r.Tag) != 0 {
		t.Error("la release deve essere al suo posto, senza parziali")
	}
}
