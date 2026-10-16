// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Le immagini disco (.dmg) di macOS: CentralHub le monta in sola lettura,
// copia l'app che contengono con ditto (permessi, collegamenti e firma
// restano com'erano) e le smonta. Da lì in poi è un'app come quella di uno
// zip: stessa cartella, stesso scambio, stessa disinstallazione.

var reMontaggio = regexp.MustCompile(`<key>mount-point</key>\s*<string>([^<]*)</string>`)

// montaDmg monta l'immagine in una cartella temporanea e restituisce i punti
// di montaggio e la funzione che smonta e ripulisce. Niente finestra nel
// Finder (-nobrowse), nessuna verifica lunga (-noverify); se l'immagine
// mostra una licenza la accetta lo stesso: non c'è nessuno a rispondere.
func montaDmg(percorso string) (punti []string, smonta func(), err error) {
	base, err := os.MkdirTemp("", "centralhub-dmg-")
	if err != nil {
		return nil, nil, err
	}
	ctx, annulla := context.WithTimeout(context.Background(), 5*time.Minute)
	defer annulla()
	cmd := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-nobrowse", "-readonly", "-noverify", "-noautoopen",
		"-plist", "-mountrandom", base, percorso)
	cmd.Stdin = strings.NewReader("Y\n")
	out, err := cmd.Output()
	for _, m := range reMontaggio.FindAllSubmatch(out, -1) {
		punti = append(punti, html.UnescapeString(string(m[1])))
	}
	smonta = func() {
		for _, p := range punti {
			for tentativo := 0; tentativo < 6; tentativo++ {
				args := []string{"detach", p}
				if tentativo >= 3 {
					args = append(args, "-force")
				}
				if exec.Command("/usr/bin/hdiutil", args...).Run() == nil {
					break
				}
				time.Sleep(time.Second)
			}
		}
		os.Remove(base)
	}
	if err != nil || len(punti) == 0 {
		smonta()
		if err == nil {
			err = errors.New("nessun volume montato")
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, nil, fmt.Errorf("l'immagine disco non si apre: %w", err)
	}
	return punti, smonta, nil
}

// estraiDmg copia in dest l'app contenuta in un'immagine disco. Se ce n'è
// più d'una sceglie quella che porta il nome del programma.
func estraiDmg(percorso, dest, nome string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("le immagini disco (.dmg) si aprono solo su macOS")
	}
	punti, smonta, err := montaDmg(percorso)
	if err != nil {
		return err
	}
	defer smonta()
	var app, pkg []string
	for _, p := range punti {
		m, _ := filepath.Glob(filepath.Join(p, "*.app"))
		app = append(app, m...)
		k, _ := filepath.Glob(filepath.Join(p, "*.pkg"))
		pkg = append(pkg, k...)
	}
	if len(app) > 1 {
		var giuste []string
		for _, a := range app {
			if somiglia(strings.TrimSuffix(filepath.Base(a), ".app"), nome) || somiglia(nome, strings.TrimSuffix(filepath.Base(a), ".app")) {
				giuste = append(giuste, a)
			}
		}
		if len(giuste) == 1 {
			app = giuste
		}
	}
	switch {
	case len(app) == 1:
	case len(app) > 1:
		return fmt.Errorf("l'immagine disco contiene più app (%d) e non si capisce quale sia %s", len(app), nome)
	case len(pkg) > 0:
		return errors.New("l'immagine disco contiene un installatore (.pkg), non un'app da copiare: CentralHub non lo sa seguire da lì")
	default:
		return errors.New("l'immagine disco non contiene nessuna app")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if out, err := exec.Command("/usr/bin/ditto", app[0], filepath.Join(dest, filepath.Base(app[0]))).CombinedOutput(); err != nil {
		return fmt.Errorf("app non copiata dall'immagine disco: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
