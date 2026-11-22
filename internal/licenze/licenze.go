// SPDX-License-Identifier: MIT

// Package licenze tiene dentro il programma i testi di licenza di tutto ciò che CentralHub incorpora e li
// presenta all'interfaccia (clic sul numero di versione; su macOS anche il menu «Informazioni su CentralHub»).
// Il contenuto di testi/raccolta lo produce scripts/raccogli-licenze.sh a ogni build: le licenze nell'app
// sono quindi sempre aggiornate rispetto a go.mod e agli elenchi ragionati (THIRD-PARTY-*.md).
package licenze

import (
	"path"
	"sort"
	"strings"
)

// testi sono i file della raccolta incorporata: percorso («testi/raccolta/LICENSE», «testi/raccolta/moduli-go/…») → testo. Li imposta il
// pacchetto main, che incorpora i file con go:embed e li passa qui come semplici testi: il generatore dei binding di Wails 2.13, con Go 1.27,
// non riesce a leggere i tipi di io/fs e di embed in un pacchetto interno.
var testi map[string]string

// Usa dice dove stanno i testi incorporati.
func Usa(m map[string]string) { testi = m }

// Voce è un testo di licenza che l'app può mostrare.
type Voce struct {
	K       string // identificatore stabile: il percorso dentro la raccolta
	Nome    string
	Licenza string // riconosciuta dal testo («Apache-2.0», «MIT»…) o «vedi il testo»
	Gruppo  string
	Come    string // una riga che dice di che cosa si tratta
}

// I gruppi, nell'ordine in cui compaiono.
const (
	GruppoProgramma = "Il programma"
	GruppoElenchi   = "Elenchi ragionati dei componenti"
	GruppoGo        = "Librerie Go incorporate"
	GruppoJS        = "Librerie JavaScript incorporate"
	GruppoAltri     = "Altri componenti"
)

// OrdineGruppi è l'ordine di presentazione.
var OrdineGruppi = []string{GruppoProgramma, GruppoElenchi, GruppoGo, GruppoJS, GruppoAltri}

// Raccolta dice se questa compilazione contiene la raccolta (è vuota nelle prove fatte a mano).
func Raccolta() bool { return haRadice(testi, "testi/raccolta") }

func haRadice(m map[string]string, radice string) bool {
	for k := range m {
		if strings.HasPrefix(k, radice+"/") {
			return true
		}
	}
	return false
}

// Indice elenca le voci della raccolta incorporata.
func Indice() []Voce { return indiceDa(testi, "testi/raccolta") }

// Testo restituisce la voce e il suo testo.
func Testo(k string) (Voce, string, bool) { return testoDa(testi, "testi/raccolta", k) }

func indiceDa(fsys map[string]string, radice string) []Voce {
	if !haRadice(fsys, radice) {
		return nil
	}
	versioni := leggiElenco(fsys, path.Join(radice, "moduli-go/ELENCO.txt"))
	versioniJS := leggiElenco(fsys, path.Join(radice, "moduli-js/ELENCO.txt"))
	var out []Voce
	percorsi := make([]string, 0, len(fsys))
	for p := range fsys {
		if strings.HasPrefix(p, radice+"/") {
			percorsi = append(percorsi, p)
		}
	}
	sort.Strings(percorsi)
	for _, p := range percorsi {
		rel := strings.TrimPrefix(p, radice+"/")
		base := path.Base(rel)
		if base == "ELENCO.txt" {
			continue
		}
		v := Voce{K: rel}
		switch {
		case rel == "LICENSE":
			v.Gruppo, v.Nome, v.Come = GruppoProgramma, "CentralHub", "La licenza del programma."
		case strings.HasPrefix(rel, "THIRD-PARTY-"):
			v.Gruppo, v.Nome, v.Come = GruppoElenchi, strings.TrimSuffix(strings.TrimPrefix(base, "THIRD-PARTY-"), path.Ext(base)), "Cosa c'è dentro, con versione, licenza e modifiche."
		case strings.HasPrefix(rel, "licenses/"):
			v.Gruppo, v.Nome, v.Come = GruppoAltri, strings.TrimSuffix(base, path.Ext(base)), "Componente che accompagna il pacchetto (testo scritto a mano)."
		case strings.HasPrefix(rel, "moduli-go/"):
			v.Gruppo, v.Nome = GruppoGo, nomeModulo(rel, base, versioni)
			v.Come = "Libreria Go compilata nel programma."
		case strings.HasPrefix(rel, "moduli-js/"):
			v.Gruppo, v.Nome = GruppoJS, nomeModulo(rel, base, versioniJS)
			v.Come = "Libreria JavaScript compilata nell'interfaccia."
		default:
			continue
		}
		v.Licenza = Riconosci(fsys[p])
		// NOTICE, AUTHORS e CREDITS non sono licenze: sono avvisi o elenchi che la licenza chiede di conservare
		switch up := strings.ToUpper(base); {
		case v.Gruppo == GruppoElenchi:
			v.Licenza = "elenco ragionato"
		case strings.Contains(up, "NOTICE"):
			v.Licenza = "avviso (NOTICE)"
		case strings.Contains(up, "AUTHORS"):
			v.Licenza = "elenco degli autori"
		case strings.Contains(up, "CREDITS"):
			v.Licenza = "crediti"
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := posizioneGruppo(out[i].Gruppo), posizioneGruppo(out[j].Gruppo)
		if gi != gj {
			return gi < gj
		}
		return strings.ToLower(out[i].Nome) < strings.ToLower(out[j].Nome)
	})
	return out
}

func posizioneGruppo(g string) int {
	for i, x := range OrdineGruppi {
		if x == g {
			return i
		}
	}
	return len(OrdineGruppi)
}

func testoDa(fsys map[string]string, radice, k string) (Voce, string, bool) {
	// k arriva dall'interfaccia: si accetta solo ciò che l'indice ha davvero (niente percorsi inventati)
	for _, v := range indiceDa(fsys, radice) {
		if v.K == k {
			testo, ok := fsys[path.Join(radice, k)]
			return v, testo, ok
		}
	}
	return Voce{}, "", false
}

// leggiElenco: «modulo@versione» per riga → mappa dal nome della cartella (barre sostituite da _) alla versione.
func leggiElenco(fsys map[string]string, p string) map[string]string {
	b, ok := fsys[p]
	if !ok {
		return nil
	}
	m := map[string]string{}
	for _, riga := range strings.Split(b, "\n") {
		riga = strings.TrimSpace(riga)
		if i := strings.Index(riga, " ("); i > 0 { // l'elenco npm scrive «nome@versione (licenza)»
			riga = riga[:i]
		}
		if i := strings.LastIndex(riga, "@"); i > 0 {
			m[strings.NewReplacer("/", "_", "@", "_").Replace(riga[:i])] = riga[i+1:]
		}
	}
	return m
}

func nomeModulo(rel, base string, versioni map[string]string) string {
	parti := strings.Split(rel, "/") // moduli-go/<modulo>/[annidate/<sotto>/]FILE
	if len(parti) < 3 {
		return base
	}
	mod := parti[1]
	nome := strings.ReplaceAll(mod, "_", "/")
	if mod == "go" {
		nome = "Go (libreria standard e runtime)"
	} else if v, ok := versioni[mod]; ok {
		nome += " " + v
	}
	if len(parti) > 4 && parti[2] == "annidate" {
		nome += " › " + strings.ReplaceAll(parti[3], "_", "/")
	}
	if !strings.HasPrefix(strings.ToUpper(base), "LICENSE") {
		nome += " (" + strings.TrimSuffix(base, path.Ext(base)) + ")"
	}
	return nome
}

// Riconosci indovina la licenza dal testo. È un aiuto per l'elenco: il testo intero resta la fonte.
func Riconosci(t string) string {
	l := strings.ToLower(t)
	has := func(s ...string) bool {
		for _, x := range s {
			if !strings.Contains(l, x) {
				return false
			}
		}
		return true
	}
	switch {
	case has("apache license", "version 2.0") && has("llvm exception"):
		return "Apache-2.0 con eccezioni LLVM"
	case has("apache license", "version 2.0"):
		return "Apache-2.0"
	case has("sil open font license"):
		return "SIL OFL 1.1"
	case has("mozilla public license"):
		return "MPL-2.0"
	case has("gnu lesser general public license"):
		return "LGPL"
	case has("gnu general public license"):
		return "GPL"
	case has("permission is hereby granted, free of charge") && has("the software is provided"):
		return "MIT"
	case has("redistribution and use in source and binary forms") && has("neither the name"):
		return "BSD-3-Clause"
	case has("redistribution and use in source and binary forms"):
		return "BSD-2-Clause"
	case has("permission to use, copy, modify, and/or distribute"):
		return "ISC"
	case has("public domain") || has("creativecommons.org/publicdomain"):
		return "pubblico dominio"
	case has("unlicense"):
		return "Unlicense"
	}
	return "vedi il testo"
}

// Gruppo è un gruppo della finestra delle licenze: si mostra chiuso, con il numero di voci.
type Gruppo struct {
	Titolo string       `json:"titolo"`
	Voci   []VoceElenco `json:"voci"`
}

// VoceElenco è una riga della finestra: un componente e la sua licenza (il testo si chiede a parte con Testo).
type VoceElenco struct {
	K       string `json:"k"`
	Nome    string `json:"nome"`
	Licenza string `json:"licenza"`
	Come    string `json:"come"`
}

// Catalogo è tutto ciò che la finestra «Licenze» mostra.
type Catalogo struct {
	Versione string   `json:"versione"`
	Raccolta bool     `json:"raccolta"` // false: build fatta a mano senza i testi raccolti
	Gruppi   []Gruppo `json:"gruppi"`
}

// Leggi costruisce il catalogo dalla raccolta incorporata: gruppi nell'ordine dichiarato, voci in ordine di nome.
func Leggi(versione string) Catalogo {
	c := Catalogo{Versione: versione, Raccolta: Raccolta(), Gruppi: []Gruppo{}}
	for _, v := range Indice() {
		if n := len(c.Gruppi); n == 0 || c.Gruppi[n-1].Titolo != v.Gruppo {
			c.Gruppi = append(c.Gruppi, Gruppo{Titolo: v.Gruppo, Voci: []VoceElenco{}})
		}
		g := &c.Gruppi[len(c.Gruppi)-1]
		g.Voci = append(g.Voci, VoceElenco{K: v.K, Nome: v.Nome, Licenza: v.Licenza, Come: v.Come})
	}
	return c
}
