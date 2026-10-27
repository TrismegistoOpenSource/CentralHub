// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"centralhub/internal/config"
	"centralhub/internal/github"
)

// repoFirmato aggiunge un repository per cui la firma è attesa (l'interruttore
// «Pretendi la firma» acceso), con un'eventuale cartella d'archivio.
func repoFirmato(e *ambiente, archivio string) string {
	e.t.Helper()
	loc := config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: archivio}
	v := e.aggiungi(loc)
	if _, err := e.app.SalvaRepo(config.Repo{ID: v.ID, Nome: "Prova", Repo: "a/Prova", PretendiFirma: true, Locale: loc}); err != nil {
		e.t.Fatal(err)
	}
	return v.ID
}

// ricordata è la release che CentralHub ricorda per un repository.
func (e *ambiente) ricordata(id string) *github.Release {
	e.app.mu.Lock()
	defer e.app.mu.Unlock()
	return e.app.ultime[id]
}

// Chi pubblica carica la firma dopo il run di Actions. Un controllo fatto in
// mezzo ricorda la release senza firma e per 24 ore la riusa: il tasto
// «Controlla» la rilegge, il controllo automatico no.
func TestControllaDaTastoRileggeLaFirmaComparsa(t *testing.T) {
	e := nuovoAmbiente(t)
	e.web = true // repository «gratis»: il tag si chiede al sito e l'elenco in cache vale 24 ore
	id := repoFirmato(e, "")
	ctx := context.Background()

	// 1. controllo fatto prima che la release venga firmata
	if _, err := e.app.controlla(ctx, e.voce(id)); err != nil {
		t.Fatal(err)
	}
	if _, c := e.ricordata(id).FileFirma(); c {
		t.Fatal("la release di partenza non doveva avere la firma")
	}
	// 2. l'installazione si rifiuta e dice cosa fare
	v := e.aggiorna(id, false)
	if v.EsitoUpdate == nil || v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, testoMancaFirma) || !strings.Contains(v.EsitoUpdate.Messaggio, "Controlla") {
		t.Fatalf("senza firma deve rifiutare e suggerire «Controlla»: %+v", v.EsitoUpdate)
	}

	// 3. chi pubblica firma; il controllo automatico (all'apertura) si fida della cache
	e.firma = "valida"
	api := e.api
	if _, err := e.app.controlla(ctx, e.voce(id)); err != nil {
		t.Fatal(err)
	}
	if e.api != api {
		t.Errorf("il controllo automatico non deve rifare l'elenco: %d richieste in più", e.api-api)
	}
	if _, c := e.ricordata(id).FileFirma(); c {
		t.Error("il controllo automatico non doveva vedere la firma (elenco in cache)")
	}

	// 4. il tasto rilegge: una sola richiesta, la firma compare, l'esito vecchio sparisce
	r, err := e.app.controllaDaTasto(ctx, e.voce(id))
	if err != nil {
		t.Fatal(err)
	}
	if e.api != api+1 {
		t.Errorf("il tasto doveva fare una richiesta, ne ha fatte %d", e.api-api)
	}
	if _, c := r.FileFirma(); !c {
		t.Fatal("dopo il tasto la release deve avere la firma")
	}
	if _, c := e.ricordata(id).FileFirma(); !c {
		t.Error("la cache non è stata aggiornata")
	}
	if v = e.vista(id); v.EsitoUpdate != nil {
		t.Errorf("l'esito «manca la firma» non è più vero e doveva sparire: %+v", v.EsitoUpdate)
	}

	// 5. ora si installa, senza chiudere né riaprire niente
	if v = e.aggiorna(id, false); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" {
		t.Errorf("dopo il tasto l'installazione deve riuscire: %+v", v.EsitoUpdate)
	}
}

// Il tasto rifà solo ciò che serve: un repository la cui release ha già la
// firma, o per cui la firma non è attesa, resta sull'elenco in cache.
func TestControllaDaTastoNonRifaDoveNonServe(t *testing.T) {
	ctx := context.Background()

	// firma non attesa (repository di terzi, release senza firma)
	e := nuovoAmbiente(t)
	e.web = true
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	e.app.controlla(ctx, e.voce(v.ID))
	api := e.api
	if _, err := e.app.controllaDaTasto(ctx, e.voce(v.ID)); err != nil {
		t.Fatal(err)
	}
	if e.api != api {
		t.Errorf("firma non attesa: nessuna richiesta in più, invece %d", e.api-api)
	}

	// firma attesa e già presente nella cache
	e = nuovoAmbiente(t)
	e.web = true
	e.firma = "valida"
	id := repoFirmato(e, "")
	e.app.controlla(ctx, e.voce(id))
	api = e.api
	if _, err := e.app.controllaDaTasto(ctx, e.voce(id)); err != nil {
		t.Fatal(err)
	}
	if e.api != api {
		t.Errorf("release già firmata: nessuna richiesta in più, invece %d", e.api-api)
	}

	// firma attesa, ancora assente: una richiesta, e il rifiuto resta
	e = nuovoAmbiente(t)
	e.web = true
	id = repoFirmato(e, "")
	e.app.controlla(ctx, e.voce(id))
	if w := e.aggiorna(id, false); w.EsitoUpdate.OK {
		t.Fatal("senza firma deve rifiutare")
	}
	api = e.api
	if _, err := e.app.controllaDaTasto(ctx, e.voce(id)); err != nil {
		t.Fatal(err)
	}
	if e.api != api+1 {
		t.Errorf("ancora senza firma: una richiesta, invece %d", e.api-api)
	}
	if w := e.vista(id); w.EsitoUpdate == nil || w.EsitoUpdate.OK {
		t.Errorf("la firma non c'è ancora: l'esito del rifiuto deve restare: %+v", w.EsitoUpdate)
	}
}

// «Controlla» generale applica la stessa regola, e solo ai repository interessati.
func TestControllaTuttiRifaSoloIDaRiverificare(t *testing.T) {
	e := nuovoAmbiente(t)
	e.web = true
	ctx := context.Background()
	firmato := repoFirmato(e, "")
	terzi, err := e.app.SalvaRepo(config.Repo{Nome: "Altro", Repo: "b/Altro", Locale: config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente}})
	if err != nil {
		t.Fatal(err)
	}
	e.app.controlla(ctx, e.voce(firmato))
	e.app.controlla(ctx, e.voce(terzi.ID))

	e.firma = "valida" // da ora la release finta ha la firma, per tutti e due
	api := e.api
	e.app.ControllaTutti()
	e.attendi()
	if e.api != api+1 {
		t.Errorf("ControllaTutti doveva rileggere solo il repository interessato: %d richieste", e.api-api)
	}
	if _, c := e.ricordata(firmato).FileFirma(); !c {
		t.Error("il repository con la firma attesa doveva vedere la firma")
	}
	if _, c := e.ricordata(terzi.ID).FileFirma(); c {
		t.Error("il repository di terzi doveva restare sull'elenco in cache")
	}
}

// Una release archiviata senza firma (prima che la si caricasse) si completa
// con «Archivia»: Scarica non la rifà, e senza firma non si installerebbe.
func TestArchivioSenzaFirmaSiCompleta(t *testing.T) {
	e := nuovoAmbiente(t)
	e.firma = "valida"
	master := filepath.Join(e.base, "master")
	id := repoFirmato(e, master)
	if v := e.archivia(id); !v.EsitoArchivio.OK {
		t.Fatalf("archiviazione firmata: %+v", v.EsitoArchivio)
	}
	rel := filepath.Join(master, "v1.0.0")
	sums, sig := filepath.Join(rel, "SHA256SUMS.txt"), filepath.Join(rel, "SHA256SUMS.txt.sig")
	giusto, err := os.ReadFile(sums)
	if err != nil {
		t.Fatal(err)
	}

	for nome, rovina := range map[string]func(){
		"manca solo la firma": func() { os.Remove(sig) },
		"mancano firma e somme": func() {
			os.Remove(sig)
			os.Remove(sums)
		},
		"somme diverse e firma assente": func() {
			os.Remove(sig)
			os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  "+e.nomeFile+"\n"), 0o644)
		},
	} {
		rovina()
		// così com'è, dall'archivio non si installa
		if v := e.installa(id, Opzioni{Da: "archivio", Tag: "v1.0.0"}); v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "nell'archivio la release non è firmata") {
			t.Fatalf("%s: l'archivio senza firma deve rifiutare: %+v", nome, v.EsitoUpdate)
		}
		// «Archivia» lo completa
		v := e.archivia(id)
		if !v.EsitoArchivio.OK || !strings.Contains(v.EsitoArchivio.Messaggio, "aggiunta la firma") {
			t.Fatalf("%s: l'archivio doveva completarsi: %+v", nome, v.EsitoArchivio)
		}
		if b, err := os.ReadFile(sums); err != nil || string(b) != string(giusto) {
			t.Errorf("%s: le somme firmate non sono state rimesse: %v %q", nome, err, b)
		}
		if _, err := os.Stat(sig); err != nil {
			t.Errorf("%s: manca ancora la firma: %v", nome, err)
		}
		// completo: una seconda volta non scrive niente
		if v = e.archivia(id); strings.Contains(v.EsitoArchivio.Messaggio, "aggiunta") {
			t.Errorf("%s: archivio già completo, non doveva riscrivere: %+v", nome, v.EsitoArchivio)
		}
	}

	// completato, si installa anche senza rete
	e.srv.Close()
	if v := e.installa(id, Opzioni{Da: "archivio", Tag: "v1.0.0"}); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" {
		t.Errorf("installazione dall'archivio completato: %+v", v.EsitoUpdate)
	}
}
