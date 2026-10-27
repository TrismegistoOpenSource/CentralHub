// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"centralhub/internal/config"
	"centralhub/internal/firma"
	"centralhub/internal/github"
	"centralhub/internal/scarica"
)

// La verifica della firma è sempre attiva. Le regole, pensate perché togliere
// la firma non basti a farla ignorare (downgrade):
//
//  1. Se la release ha un SHA256SUMS.txt.sig nel formato di CentralHub, la
//     firma DEVE tornare (con una delle chiavi incorporate, per quel
//     repository e quel tag): altrimenti la release si rifiuta, per qualunque
//     repository. Una firma in un altro formato (minisign, GPG) non è nostra e
//     si ignora.
//  2. Senza firma, la release si rifiuta se la firma è «attesa»: (a) il
//     repository ha l'interruttore acceso, a mano o perché CentralHub ha già
//     visto una sua release firmata (da lì in poi non scende più); (b) è di un
//     proprietario che firma (proprietariFirmati) ed è stata pubblicata dopo
//     firmaObbligatoriaDal. È la regola (b) che protegge chi aggiunge il
//     repository per la prima volta quando le release firmate esistono già.
//  3. Altrimenti (repository di terzi, release vecchie di prima della firma) si
//     procede come prima, con il digest di GitHub o SHA256SUMS.txt, e il
//     registro dice «non firmata».
//
// Chi pubblica carica la firma dopo il run di Actions: una release nostra
// appena pubblicata e non ancora firmata si rifiuta finché non la si firma.

var proprietariFirmati = []string{"trismegistoopensource"}

// testoMancaFirma sta nei messaggi di rifiuto per una firma assente:
// firmaComparsa lo cerca per togliere l'esito che non è più vero.
const testoMancaFirma = "manca SHA256SUMS.txt.sig"

// firmaObbligatoriaDal: le release dei proprietari che firmano, pubblicate da
// questo giorno in poi, devono essere firmate. La data di pubblicazione la
// mette GitHub quando la release esce: non si può retrodatare.
var firmaObbligatoriaDal = time.Date(2026, time.October, 21, 0, 0, 0, 0, time.UTC)

// chiaviFidate sono le chiavi pubbliche con cui si verificano le firme;
// sostituibile nelle prove.
var chiaviFidate = func() []ed25519.PublicKey { return firma.Fidate() }

// proprietarioFirmato dice se il repository è di chi firma i propri programmi.
func proprietarioFirmato(repo string) bool {
	p, _, _ := strings.Cut(strings.ToLower(repo), "/")
	for _, f := range proprietariFirmati {
		if p == f {
			return true
		}
	}
	return false
}

// firmaAttesa dice se per questa release la firma è obbligatoria. quando è
// l'istante di pubblicazione, o zero se non si sa (e allora si è prudenti).
func firmaAttesa(v voce, quando time.Time) bool {
	if v.PretendiFirma {
		return true
	}
	return proprietarioFirmato(v.Repo) && (quando.IsZero() || !quando.Before(firmaObbligatoriaDal))
}

// esitoFirma è ciò che la verifica restituisce.
type esitoFirma struct {
	Release *github.Release   // con i digest delle somme firmate, se firmata
	Somme   map[string]string // nil = non firmata
	// Grezzo e Firma sono il testo di SHA256SUMS.txt e la firma, già
	// verificati (se firmata): servono a completare un archivio.
	Grezzo []byte
	Firma  string
}

func (e esitoFirma) Firmata() bool { return e.Somme != nil }

// verificaRelease applica le regole sopra a una release di GitHub.
func (a *App) verificaRelease(ctx context.Context, v voce, r *github.Release) (esitoFirma, error) {
	cli := a.nuovoCli(v.token)
	sig, err := cli.Firma(ctx, v.Repo, r)
	if err != nil {
		return esitoFirma{}, fmt.Errorf("SHA256SUMS.txt.sig non si è letto: %w", err)
	}
	quando, _ := time.Parse(time.RFC3339, r.Pubblicata)
	if !firma.Riconosce(sig) {
		if firmaAttesa(v, quando) {
			if sig == "" {
				return esitoFirma{}, errors.New("la release non è firmata (" + testoMancaFirma + ") e per questo repository la firma è obbligatoria: chi pubblica la firma con scripts/firma-release.sh. Se l'ha firmata da poco, premi «Controlla» su questo repository")
			}
			return esitoFirma{}, errors.New("il file SHA256SUMS.txt.sig non è nel formato di CentralHub e per questo repository la firma è obbligatoria")
		}
		return esitoFirma{Release: r}, nil
	}
	somme, grezzo, err := cli.SommeEFirma(ctx, v.Repo, r)
	if err != nil {
		return esitoFirma{}, fmt.Errorf("SHA256SUMS.txt non si è letto: %w", err)
	}
	if grezzo == nil {
		return esitoFirma{}, errors.New("la release ha una firma ma non SHA256SUMS.txt: non c'è nulla da verificare")
	}
	if err := firma.Verifica(v.Repo, r.Tag, grezzo, sig, chiaviFidate()); err != nil {
		return esitoFirma{}, err
	}
	rr, err := r.ApplicaFirmate(somme)
	if err != nil {
		return esitoFirma{}, err
	}
	a.ricordaFirma(v)
	return esitoFirma{Release: rr, Somme: somme, Grezzo: grezzo, Firma: sig}, nil
}

// ricordaFirma: da quando CentralHub ha visto una release firmata di questo
// repository, la firma diventa obbligatoria per sempre (l'interruttore si
// accende e si salva): una release senza firma non è più «una di quelle che
// non firmano», è un attacco o un errore.
func (a *App) ricordaFirma(v voce) {
	a.mu.Lock()
	defer a.mu.Unlock()
	nuovo := a.cfg
	nuovo.Repo = append([]config.Repo(nil), a.cfg.Repo...)
	for i := range nuovo.Repo {
		if nuovo.Repo[i].ID == v.ID && !nuovo.Repo[i].PretendiFirma {
			nuovo.Repo[i].PretendiFirma = true
			if config.Salva(nuovo) == nil {
				a.cfg = nuovo
				go a.log(v.Nome + ": ho visto una release firmata, da ora la firma è obbligatoria per questo repository")
			}
		}
	}
}

// firmateDallArchivio fa lo stesso con i due file che l'archiviazione ha
// scaricato accanto ai pacchetti. La cartella si chiama come il tag, ripulito
// (scarica.NomeCartella): la firma si verifica con quel nome, e con il tag
// vero se lo si conosce.
func (a *App) firmateDallArchivio(v voce, rel, tagNoto string) (map[string]string, bool, error) {
	cartella := v.CartellaArchivio
	sigB, haSig := scarica.LeggiDallaRelease(cartella, rel, firma.NomeFile)
	sig := strings.TrimSpace(string(sigB))
	if !haSig || !firma.Riconosce(sig) {
		if firmaAttesa(v, scarica.DataRelease(cartella, rel)) {
			return nil, false, errors.New("nell'archivio la release non è firmata (" + testoMancaFirma + ") e per questo repository la firma è obbligatoria. Se ora su GitHub è firmata, premi «Controlla» e poi «Archivia»: l'archivio si completa con la firma")
		}
		return nil, false, nil
	}
	grezzo, ok := scarica.LeggiDallaRelease(cartella, rel, "SHA256SUMS.txt")
	if !ok {
		return nil, false, errors.New("nell'archivio c'è la firma ma manca SHA256SUMS.txt")
	}
	var ultimo error
	for _, tag := range []string{tagNoto, rel} {
		if tag == "" {
			continue
		}
		if ultimo = firma.Verifica(v.Repo, tag, grezzo, sig, chiaviFidate()); ultimo == nil {
			return github.AnalizzaSomme(string(grezzo)), true, nil
		}
	}
	return nil, false, ultimo
}
