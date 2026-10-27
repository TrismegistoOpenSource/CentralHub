// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"sync"
	"time"

	"centralhub/internal/autoverifica"
	"centralhub/internal/config"
)

// repoRelease è dove sta la release firmata di CentralHub: l'autoverifica
// all'avvio ne scarica la firma da lì (internal/autoverifica).
const repoRelease = "TrismegistoOpenSource/CentralHub"

// autocontrollo tiene l'esito del controllo che CentralHub fa su sé stesso.
type autocontrollo struct {
	una   sync.Once
	stato autoverifica.Stato
}

// AutoverificaProgramma controlla che i file di CentralHub siano integri e,
// se la release è pubblica, che sia firmata da chi la pubblica
// (internal/autoverifica). L'interfaccia la chiama dopo il primo disegno,
// quindi non ritarda l'avvio; gira una volta sola e le chiamate successive
// restituiscono lo stesso esito. L'ultimo esito è anche scritto nella cartella
// di configurazione (autoverifica-stato.json).
//
// Non c'entra con VerificaIntegrita, che ricontrolla i programmi che
// CentralHub ha installato.
func (a *App) AutoverificaProgramma() autoverifica.Stato {
	a.autocontrollo.una.Do(func() {
		ctx, annulla := context.WithTimeout(context.Background(), 40*time.Second)
		defer annulla()
		dir, _, _ := config.Cartella()
		scrivi := func(s autoverifica.Stato) {
			if dir != "" {
				_ = autoverifica.ScriviStato(dir, s)
			}
		}
		a.autocontrollo.stato = autoverifica.Verifica(ctx, autoverifica.Configurazione{
			Versione:     Versione,
			Repo:         repoRelease,
			CartellaDati: dir,
			Avanzamento:  scrivi, // l'esito locale è su disco subito, prima del controllo in rete
		})
		scrivi(a.autocontrollo.stato)
	})
	return a.autocontrollo.stato
}
