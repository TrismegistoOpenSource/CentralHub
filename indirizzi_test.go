// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"centralhub/internal/config"
)

// hostEstraneo è un server che non deve ricevere mai nessuna richiesta.
func hostEstraneo(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var arrivate atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivate.Add(1)
		w.Write([]byte("pacchetto manomesso"))
	}))
	t.Cleanup(srv.Close)
	return srv, &arrivate
}

// ultime.json sta sul disco dell'utente: se qualcuno cambia gli indirizzi di
// una release ricordata, CentralHub non la riusa e non scarica da lì.
func TestUltimeConIndirizziAlteratiNonSiRiusa(t *testing.T) {
	e := nuovoAmbiente(t)
	e.web = true // repository «gratis»: l'elenco in cache vale 24 ore
	fuori, arrivate := hostEstraneo(t)
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	ctx := context.Background()
	if _, err := e.app.controlla(ctx, e.voce(v.ID)); err != nil {
		t.Fatal(err)
	}
	altera := func() {
		e.app.mu.Lock()
		e.app.ultime[v.ID].Assets[0].URL = fuori.URL + "/repos/a/Prova/releases/assets/1"
		e.app.mu.Unlock()
	}

	// il controllo non riusa la cache alterata: rilegge dall'API (una richiesta) e la corregge
	altera()
	api := e.api
	if _, err := e.app.controlla(ctx, e.voce(v.ID)); err != nil {
		t.Fatal(err)
	}
	if e.api != api+1 {
		t.Errorf("la cache alterata doveva essere rifatta: %d richieste", e.api-api)
	}
	if strings.HasPrefix(e.ricordata(v.ID).Assets[0].URL, fuori.URL) {
		t.Error("la cache è rimasta alterata")
	}

	// l'installazione, subito dopo un controllo, non usa la scorciatoia con una cache alterata
	altera()
	if w := e.aggiorna(v.ID, false); !w.EsitoUpdate.OK || w.Installato.Tag != "v1.0.0" {
		t.Errorf("installazione con la cache alterata: %+v", w.EsitoUpdate)
	}
	if n := arrivate.Load(); n != 0 {
		t.Errorf("l'host estraneo ha ricevuto %d richieste", n)
	}
}

// Se è la risposta stessa a dare un indirizzo di un altro host, il file non si
// scarica, l'altro host non riceve niente, e il rifiuto dice perché.
func TestIndirizzoEstraneoDallApiSiRifiuta(t *testing.T) {
	e := nuovoAmbiente(t)
	fuori, arrivate := hostEstraneo(t)
	e.assetFuori = fuori.URL + "/repos/a/Prova/releases/assets/1"
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	w := e.aggiorna(v.ID, false)
	if w.EsitoUpdate == nil || w.EsitoUpdate.OK || w.Installato.Presente || !strings.Contains(w.EsitoUpdate.Messaggio, "per sicurezza non lo apro") {
		t.Fatalf("un indirizzo estraneo deve far rifiutare: %+v installato %v", w.EsitoUpdate, w.Installato.Presente)
	}
	if n := arrivate.Load(); n != 0 {
		t.Errorf("l'host estraneo ha ricevuto %d richieste", n)
	}

	// archiviare vale lo stesso
	master := t.TempDir()
	e.app.ImpostaLocale(v.ID, config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: master})
	if w = e.archivia(v.ID); w.EsitoArchivio == nil || w.EsitoArchivio.OK {
		t.Errorf("l'archiviazione con un indirizzo estraneo doveva fallire: %+v", w.EsitoArchivio)
	}
	if n := arrivate.Load(); n != 0 {
		t.Errorf("l'host estraneo ha ricevuto %d richieste dall'archiviazione", n)
	}

	// l'API torna giusta: si installa, la release sbagliata non resta incastrata
	e.assetFuori = ""
	if w = e.aggiorna(v.ID, false); !w.EsitoUpdate.OK || w.Installato.Tag != "v1.0.0" {
		t.Errorf("con l'indirizzo giusto doveva installare: %+v", w.EsitoUpdate)
	}
}
