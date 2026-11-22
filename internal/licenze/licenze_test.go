// SPDX-License-Identifier: MIT

package licenze

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func finta() map[string]string {
	apache := "Apache License\nVersion 2.0, January 2004\n..."
	mit := "MIT License\nPermission is hereby granted, free of charge, to any person\nTHE SOFTWARE IS PROVIDED \"AS IS\""
	return map[string]string{
		"r/LICENSE":                                                            mit,
		"r/THIRD-PARTY-LICENSES.md":                                            "# elenco",
		"r/licenses/appimage-runtime.txt":                                      "Redistribution and use in source and binary forms",
		"r/moduli-go/ELENCO.txt":                                               "github.com/tetratelabs/wazero@v1.12.0\ngolang.org/x/text@v0.43.0\n",
		"r/moduli-go/github.com_tetratelabs_wazero/LICENSE":                    apache,
		"r/moduli-go/github.com_tetratelabs_wazero/NOTICE":                     "wazero",
		"r/moduli-go/golang.org_x_text/LICENSE":                                "Redistribution and use in source and binary forms ... Neither the name of Google",
		"r/moduli-go/github.com_wailsapp_wails_v2/annidate/internal_x/LICENSE": mit,
		"r/moduli-go/go/LICENSE":                                               "Redistribution and use in source and binary forms ... Neither the name",
	}
}

func TestIndiceRaggruppaENomina(t *testing.T) {
	v := indiceDa(finta(), "r")
	per := map[string][]string{}
	for _, x := range v {
		per[x.Gruppo] = append(per[x.Gruppo], x.Nome+" | "+x.Licenza)
	}
	if got := per[GruppoProgramma]; len(got) != 1 || got[0] != "CentralHub | MIT" {
		t.Errorf("programma: %v", got)
	}
	if got := strings.Join(per[GruppoGo], "; "); !strings.Contains(got, "github.com/tetratelabs/wazero v1.12.0 | Apache-2.0") ||
		!strings.Contains(got, "github.com/tetratelabs/wazero v1.12.0 (NOTICE)") || !strings.Contains(got, "golang.org/x/text v0.43.0 | BSD-3-Clause") ||
		!strings.Contains(got, "Go (libreria standard e runtime)") || !strings.Contains(got, "› internal/x") {
		t.Errorf("Go: %v", per[GruppoGo])
	}
	if len(per[GruppoElenchi]) != 1 || len(per[GruppoAltri]) != 1 || per[GruppoAltri][0] != "appimage-runtime | BSD-2-Clause" {
		t.Errorf("elenchi %v altri %v", per[GruppoElenchi], per[GruppoAltri])
	}
	ultimo := -1 // l'ordine dei gruppi è quello dichiarato
	for _, x := range v {
		if p := posizioneGruppo(x.Gruppo); p < ultimo {
			t.Fatalf("gruppi fuori ordine: %v", x.Gruppo)
		} else {
			ultimo = p
		}
	}
}

func TestTestoSoloDaiPercorsiDellIndice(t *testing.T) {
	f := finta()
	if _, testo, ok := testoDa(f, "r", "moduli-go/github.com_tetratelabs_wazero/NOTICE"); !ok || testo != "wazero" {
		t.Errorf("voce valida: %q %v", testo, ok)
	}
	for _, k := range []string{"../etc/passwd", "moduli-go/../LICENSE", "/etc/passwd", "moduli-go/ELENCO.txt", "", "nonesiste", "moduli-go/x/../../LICENSE"} {
		if _, _, ok := testoDa(f, "r", k); ok {
			t.Errorf("«%s» non doveva essere accettata", k)
		}
	}
}

func TestSenzaRaccoltaNonCrolla(t *testing.T) {
	if v := indiceDa(map[string]string{}, "r"); len(v) != 0 {
		t.Errorf("voci senza raccolta: %v", v)
	}
}

func TestRiconosci(t *testing.T) {
	for testo, atteso := range map[string]string{
		"Apache License\nVersion 2.0\n LLVM Exception":                                            "Apache-2.0 con eccezioni LLVM",
		"Mozilla Public License Version 2.0":                                                      "MPL-2.0",
		"Permission to use, copy, modify, and/or distribute this software":                        "ISC",
		"Redistribution and use in source and binary forms, with or without. Neither the name of": "BSD-3-Clause",
		"testo qualsiasi": "vedi il testo",
	} {
		if got := Riconosci(testo); got != atteso {
			t.Errorf("%q → %q (atteso %q)", testo, got, atteso)
		}
	}
}

// Con la raccolta incorporata (build.sh e ci-locale.sh la fanno) l'app deve contenere la licenza di OGNI modulo richiesto da go.mod,
// quella del programma, l'elenco ragionato e il testo del runtime AppImage. Nelle prove fatte a mano, senza raccolta, si salta.
func TestRaccoltaIncorporataCompleta(t *testing.T) {
	Usa(daCartella(t, "testi")) // nel pacchetto vero i file li incorpora main; qui si leggono dalla cartella
	if !Raccolta() {
		if os.Getenv("CENTRALHUB_LICENZE_OBBLIGATORIE") != "" {
			t.Fatal("la raccolta delle licenze non è incorporata ma ci-locale/build doveva farla")
		}
		t.Skip("raccolta non generata (la fanno build.sh e ci-locale.sh)")
	}
	nomi := map[string]bool{}
	for _, v := range Indice() {
		nomi[v.Nome] = true
		if _, testo, ok := Testo(v.K); !ok || strings.TrimSpace(testo) == "" {
			t.Errorf("voce «%s» senza testo", v.Nome)
		}
	}
	for _, atteso := range []string{"CentralHub", "appimage-runtime", "Go (libreria standard e runtime)"} {
		if !nomi[atteso] {
			t.Errorf("manca nella raccolta: %s", atteso)
		}
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^\s*([A-Za-z0-9._~/-]+\.[A-Za-z0-9._~/-]+)\s+v[0-9][^\s]*`)
	for _, riga := range strings.Split(string(b), "\n") {
		// solo le dipendenze dirette: le «// indirect» di go.mod comprendono moduli che nessuna build compila (per esempio il server di sviluppo di
		// Wails); quelli che finiscono davvero nel binario li prende raccogli-licenze.sh da «go version -m» ed è lui il cancello
		if strings.Contains(riga, "// indirect") {
			continue
		}
		m := re.FindStringSubmatch(riga)
		if m == nil || strings.HasPrefix(strings.TrimSpace(riga), "module") || strings.HasPrefix(strings.TrimSpace(riga), "go ") {
			continue
		}
		trovato := false
		for n := range nomi {
			trovato = trovato || strings.HasPrefix(n, m[1]+" ")
		}
		if !trovato {
			t.Errorf("il modulo %s (go.mod) non ha la sua licenza nella raccolta", m[1])
		}
	}
	if len(Indice()) < 10 {
		t.Errorf("raccolta troppo piccola: %d voci", len(Indice()))
	}
}

// daCartella legge una cartella (e le sue sottocartelle) in una mappa percorso → testo, come fa main con i file incorporati.
func daCartella(t *testing.T, dir string) map[string]string {
	m := map[string]string{}
	var visita func(rel string)
	visita = func(rel string) {
		voci, err := os.ReadDir(filepath.Join(dir, rel))
		if err != nil {
			return
		}
		for _, v := range voci {
			p := filepath.ToSlash(filepath.Join(rel, v.Name()))
			if v.IsDir() {
				visita(p)
			} else if b, err := os.ReadFile(filepath.Join(dir, p)); err == nil {
				m[dir+"/"+p] = string(b)
			}
		}
	}
	visita("")
	return m
}
