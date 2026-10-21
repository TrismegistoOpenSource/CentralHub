// SPDX-License-Identifier: MIT

// Package integrita annota gli SHA-256 dei file di un programma al momento
// dell'installazione (da un pacchetto già verificato) e li ricontrolla dopo:
// dice se il programma è ancora com'era, o se qualcosa è cambiato, sparito
// o è stato danneggiato (disco, antivirus, manomissione).
//
// Si confrontano solo i file annotati: quelli nuovi (le impostazioni che un
// programma scrive accanto a sé) non contano. Un file annotato che è
// cambiato conta anche se a cambiarlo è stato il programma stesso: per questo
// il messaggio all'utente dice «diversi da come li ho installati», non
// «manomessi».
package integrita

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limiti: un programma più grande di così non si annota (il controllo
// all'avvio deve restare nell'ordine del secondo).
const (
	MaxFile = 20000
	MaxByte = 3 << 30
)

// ErrTroppoGrande: il programma supera i limiti e non si annota.
var ErrTroppoGrande = errors.New("il programma ha troppi file o è troppo grande per annotarne le somme")

// Manifesto sono le somme dei file di un programma installato.
type Manifesto struct {
	Quando string `json:"quando"`
	// File: percorso relativo (con "/") -> SHA-256 in esadecimale; per un
	// collegamento simbolico "link:<destinazione>".
	File map[string]string `json:"file"`
	Byte int64             `json:"byte"`
}

// Calcola annota le somme di un file o di tutto ciò che sta in una cartella.
func Calcola(percorso string) (Manifesto, error) {
	m := Manifesto{Quando: time.Now().Format(time.RFC3339), File: map[string]string{}}
	fi, err := os.Lstat(percorso)
	if err != nil {
		return m, err
	}
	if !fi.IsDir() {
		s, err := sommaFile(percorso)
		if err != nil {
			return m, err
		}
		m.File[filepath.Base(percorso)], m.Byte = s, fi.Size()
		return m, nil
	}
	err = filepath.WalkDir(percorso, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(percorso, p)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			dest, err := os.Readlink(p)
			if err != nil {
				return err
			}
			m.File[rel] = "link:" + dest
		case d.Type().IsRegular():
			s, err := sommaFile(p)
			if err != nil {
				return err
			}
			m.File[rel] = s
			m.Byte += info.Size()
		default:
			return nil // dispositivi, socket: non sono del programma
		}
		if len(m.File) > MaxFile || m.Byte > MaxByte {
			return ErrTroppoGrande
		}
		return nil
	})
	return m, err
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

// Esito dice come sta un programma rispetto alle somme annotate.
type Esito struct {
	Diversi []string `json:"diversi"` // annotati e cambiati
	Mancano []string `json:"mancano"` // annotati e spariti
	Totale  int      `json:"totale"`
}

// Integro: nessun file annotato è cambiato o sparito.
func (e Esito) Integro() bool { return len(e.Diversi) == 0 && len(e.Mancano) == 0 }

// Confronta ricalcola le somme dei file annotati e le confronta.
func Confronta(percorso string, m Manifesto) Esito {
	e := Esito{Totale: len(m.File)}
	base := percorso
	if fi, err := os.Lstat(percorso); err == nil && !fi.IsDir() {
		base = filepath.Dir(percorso)
	}
	nomi := make([]string, 0, len(m.File))
	for n := range m.File {
		nomi = append(nomi, n)
	}
	sort.Strings(nomi)
	for _, rel := range nomi {
		p := filepath.Join(base, filepath.FromSlash(rel))
		atteso := m.File[rel]
		if dest, ok := strings.CutPrefix(atteso, "link:"); ok {
			if got, err := os.Readlink(p); err != nil {
				e.Mancano = append(e.Mancano, rel)
			} else if got != dest {
				e.Diversi = append(e.Diversi, rel)
			}
			continue
		}
		got, err := sommaFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			e.Mancano = append(e.Mancano, rel)
		case err != nil || got != atteso:
			e.Diversi = append(e.Diversi, rel)
		}
	}
	return e
}

func fileDi(cartellaRegistro, nomeProgramma string) string {
	return filepath.Join(cartellaRegistro, nomeProgramma+".integrita.json")
}

// Salva scrive il manifesto accanto al registro delle installazioni.
func Salva(cartellaRegistro, nomeProgramma string, m Manifesto) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cartellaRegistro, 0o700); err != nil {
		return err
	}
	f := fileDi(cartellaRegistro, nomeProgramma)
	if err := os.WriteFile(f+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(f+".tmp", f)
}

// Carica legge il manifesto di un programma; false se non ce n'è uno.
func Carica(cartellaRegistro, nomeProgramma string) (Manifesto, bool) {
	var m Manifesto
	b, err := os.ReadFile(fileDi(cartellaRegistro, nomeProgramma))
	if err != nil || json.Unmarshal(b, &m) != nil || len(m.File) == 0 {
		return Manifesto{}, false
	}
	return m, true
}

// Elimina toglie il manifesto (disinstallazione).
func Elimina(cartellaRegistro, nomeProgramma string) {
	_ = os.Remove(fileDi(cartellaRegistro, nomeProgramma))
}
