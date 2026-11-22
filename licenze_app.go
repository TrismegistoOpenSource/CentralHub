// SPDX-License-Identifier: MIT

package main

import (
	"errors"

	"centralhub/internal/licenze"
)

// Licenze elenca le licenze di tutto ciò che sta dentro il programma, a gruppi e in ordine; i testi sono incorporati nell'eseguibile.
func (a *App) Licenze() licenze.Catalogo {
	return licenze.Leggi(Versione)
}

// TestoLicenza dà il testo intero di una voce dell'elenco, così com'è.
func (a *App) TestoLicenza(k string) (string, error) {
	_, testo, ok := licenze.Testo(k)
	if !ok {
		return "", errors.New("licenza non trovata")
	}
	return testo, nil
}
