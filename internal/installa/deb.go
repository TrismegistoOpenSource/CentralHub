// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// I pacchetti .deb (Debian e derivate): li installa apt, che risolve le
// dipendenze e che i permessi di amministratore li fa chiedere a pkexec.
// Dove finiscono i file lo decide il pacchetto; CentralHub annota il nome del
// pacchetto e chiede a dpkg se c'è ancora.

var (
	// debInfo legge nome e versione di un file .deb.
	debInfo = func(file string) (pacchetto, versione string, err error) {
		dpkgDeb, err := exec.LookPath("dpkg-deb")
		if err != nil {
			return "", "", errors.New("per installare un .deb serve dpkg-deb, che su questo sistema non c'è")
		}
		ctx, annulla := context.WithTimeout(context.Background(), 30*time.Second)
		defer annulla()
		out, err := exec.CommandContext(ctx, dpkgDeb, "-f", file, "Package", "Version").Output()
		if err != nil {
			return "", "", fmt.Errorf("%s non è un pacchetto .deb leggibile: %w", filepath.Base(file), err)
		}
		for _, riga := range strings.Split(string(out), "\n") {
			k, v, _ := strings.Cut(riga, ":")
			switch strings.TrimSpace(k) {
			case "Package":
				pacchetto = strings.TrimSpace(v)
			case "Version":
				versione = strings.TrimSpace(v)
			}
		}
		if !reNomeDeb.MatchString(pacchetto) {
			return "", "", fmt.Errorf("%s: nome del pacchetto non valido (%q)", filepath.Base(file), pacchetto)
		}
		return pacchetto, versione, nil
	}
	// debInstallato dice se dpkg considera installato un pacchetto.
	debInstallato = func(pacchetto string) bool {
		dpkgQuery, err := exec.LookPath("dpkg-query")
		if err != nil || !reNomeDeb.MatchString(pacchetto) {
			return false
		}
		ctx, annulla := context.WithTimeout(context.Background(), 10*time.Second)
		defer annulla()
		out, err := exec.CommandContext(ctx, dpkgQuery, "-W", "-f=${db:Status-Abbrev}", pacchetto).Output()
		return err == nil && strings.HasPrefix(string(out), "ii")
	}
)

// reNomeDeb: nome di pacchetto Debian (minuscole, cifre, + - .).
var reNomeDeb = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

// ScriptDebInstalla è lo script che, con i permessi di amministratore,
// installa un .deb con apt: anche una versione più vecchia (l'utente può
// averla scelta) e senza domande.
func ScriptDebInstalla(file string) string {
	return "#!/bin/sh\nset -e\nexport DEBIAN_FRONTEND=noninteractive\n" +
		"apt-get install -y --allow-downgrades -- " + apice(file) + "\n"
}

// ScriptDebRimuovi toglie un pacchetto con apt.
func ScriptDebRimuovi(pacchetto string) string {
	return "#!/bin/sh\nset -e\nexport DEBIAN_FRONTEND=noninteractive\n" +
		"apt-get remove -y -- " + apice(pacchetto) + "\n"
}

func installaDeb(r Richiesta) (Esito, error) {
	if r.GOOS != "linux" {
		return Esito{}, fmt.Errorf("i pacchetti .deb non si installano su %s", r.GOOS)
	}
	asset := filepath.Base(r.File)
	pacchetto, _, err := debInfo(r.File)
	if err != nil {
		return Esito{}, err
	}
	if err := r.privilegiato("linux", ScriptDebInstalla(r.File)); err != nil {
		return Esito{}, err
	}
	if !debInstallato(pacchetto) {
		return Esito{}, fmt.Errorf("apt è uscito senza errori, ma il pacchetto %s non risulta installato", pacchetto)
	}
	esito := Esito{Asset: asset}
	reg := Registro{ID: r.ID, Nome: r.Nome, Repo: r.Repo, Tag: r.Tag, Asset: asset, Tipo: TipoDeb, Fissata: r.Fissata,
		Chiave: pacchetto, NomeMostrato: pacchetto, Quando: time.Now().Format(time.RFC3339)}
	scriviRegistro(r, reg, &esito)
	return esito, nil
}
