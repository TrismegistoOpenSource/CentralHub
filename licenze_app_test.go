// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"testing"
)

// Nel programma vero (build.sh e ci-locale.sh raccolgono i testi prima di compilare) la finestra delle licenze deve avere i suoi gruppi e i suoi testi
// già dentro l'eseguibile. Nelle prove a mano, senza raccolta, si salta.
func TestLicenzeIncorporateNelProgramma(t *testing.T) {
	usaLicenzeIncorporate()
	a := &App{}
	c := a.Licenze()
	if !c.Raccolta {
		if os.Getenv("CENTRALHUB_LICENZE_OBBLIGATORIE") != "" {
			t.Fatal("i testi delle licenze non sono incorporati ma ci-locale/build doveva raccoglierli")
		}
		t.Skip("raccolta non generata (la fanno build.sh e ci-locale.sh)")
	}
	if c.Versione != Versione || len(c.Gruppi) < 3 {
		t.Fatalf("catalogo: versione %q, %d gruppi", c.Versione, len(c.Gruppi))
	}
	voci := 0
	for _, g := range c.Gruppi {
		for _, v := range g.Voci {
			voci++
			if testo, err := a.TestoLicenza(v.K); err != nil || len(testo) == 0 {
				t.Errorf("voce %q senza testo: %v", v.Nome, err)
			}
		}
	}
	if voci < 10 {
		t.Errorf("poche voci: %d", voci)
	}
	if _, err := a.TestoLicenza("../go.mod"); err == nil {
		t.Error("un percorso fuori dall'elenco non va accettato")
	}
}
