// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"centralhub/internal/config"
	"centralhub/internal/github"
	"centralhub/internal/installa"
	"centralhub/internal/integrita"
)

// nomeUltime è il file, accanto a config.json, in cui si ricorda l'ultima
// release vista di ogni repository: all'apertura i LED si disegnano subito
// con quello che si sapeva, prima di qualunque richiesta.
const nomeUltime = "ultime.json"

// maxNote: le note di una release si tengono al massimo di così.
const maxNote = 16 << 10

// ricordo è ciò che si salva per un repository.
type ricordo struct {
	Release *github.Release `json:"release"`
	// Visto: l'ultimo controllo riuscito. Preso: l'ultima volta che l'elenco
	// dei pacchetti è stato chiesto all'API.
	Visto time.Time `json:"visto"`
	Preso time.Time `json:"preso"`
}

// caricaUltime legge ultime.json. Va chiamata con il lucchetto. Un file
// assente o illeggibile significa solo che non si ricorda niente.
func (a *App) caricaUltime() {
	var m map[string]ricordo
	if b := config.LeggiFileDiStato(nomeUltime); b != nil {
		_ = json.Unmarshal(b, &m)
	}
	for id, r := range m {
		if r.Release == nil || r.Release.Tag == "" {
			continue
		}
		a.ultime[id], a.visteIl[id], a.prese[id] = r.Release, r.Visto, r.Preso
	}
}

// salvaUltime scrive ultime.json, per i soli repository ancora in lista. Va
// chiamata con il lucchetto; un errore di scrittura non ferma il controllo.
func (a *App) salvaUltime() {
	m := map[string]ricordo{}
	for _, v := range a.voci() {
		if r := a.ultime[v.ID]; r != nil {
			m[v.ID] = ricordo{Release: r, Visto: a.visteIl[v.ID], Preso: a.prese[v.ID]}
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = config.ScriviFileDiStato(nomeUltime, b)
}

// indirizzoRepo è la pagina del progetto su GitHub (non una release). Il nome
// viene dalla configurazione, ma si ricontrolla: nell'indirizzo entra solo
// ciò che è un nome di repository valido.
func indirizzoRepo(repo string) (string, error) {
	r, err := config.PulisciRepo(repo)
	if err != nil {
		return "", err
	}
	return "https://github.com/" + r, nil
}

// VaiAlRepo apre nel browser la pagina del progetto su GitHub.
func (a *App) VaiAlRepo(id string) error {
	a.mu.Lock()
	v, ok := a.voce(id)
	a.mu.Unlock()
	if !ok {
		return errors.New("repository non trovato")
	}
	url, err := indirizzoRepo(v.Repo)
	if err != nil {
		return err
	}
	if a.ctx != nil {
		wr.BrowserOpenURL(a.ctx, url)
	}
	return nil
}

// Novita è il testo della release che si conosce di un repository.
type Novita struct {
	Tag        string `json:"tag"`
	Nome       string `json:"nome"`
	Pubblicata string `json:"pubblicata"`
	Testo      string `json:"testo"`
}

// Novita restituisce le note dell'ultima release vista di un repository.
// Sono testo di GitHub: l'interfaccia le mostra come testo semplice.
func (a *App) Novita(id string) (Novita, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.voce(id); !ok {
		return Novita{}, errors.New("repository non trovato")
	}
	r := a.ultime[id]
	if r == nil {
		return Novita{}, errors.New("la release non è ancora stata controllata")
	}
	return Novita{Tag: r.Tag, Nome: r.Nome, Pubblicata: r.Pubblicata, Testo: r.Note}, nil
}

// ---------------------------------------------------------------- integrità

// annotaIntegrita annota gli SHA-256 dei file appena installati (da un
// pacchetto già verificato), per ricontrollarli all'avvio. Non vale per ciò
// che installa il sistema (deb, pkg, installatori) né per CentralHub che
// aggiorna sé stesso.
func (a *App) annotaIntegrita(v voce, reg, tipo string, e installa.Esito) {
	nome := installa.NomeProgramma(v.Nome)
	if installa.DaSistema(tipo) || e.SeStesso || e.Percorso == "" {
		integrita.Elimina(reg, nome)
		a.mu.Lock()
		delete(a.integri, v.ID)
		a.mu.Unlock()
		return
	}
	m, err := integrita.Calcola(e.Percorso)
	if err != nil {
		integrita.Elimina(reg, nome)
		a.log(v.Nome + ": le somme dei file installati non si annotano (" + err.Error() + ")")
		return
	}
	if err := integrita.Salva(reg, nome, m); err != nil {
		a.log(v.Nome + ": le somme dei file installati non si salvano (" + err.Error() + ")")
		return
	}
	a.mu.Lock()
	a.integri[v.ID] = integrita.Esito{Totale: len(m.File)}
	a.mu.Unlock()
}

// VerificaIntegrita ricontrolla gli SHA-256 dei programmi installati da
// CentralHub contro quelli annotati all'installazione. Va in un thread a
// parte: l'apertura della finestra non aspetta. Costa poco (un programma da
// 70 MB si controlla in circa 60 millisecondi).
func (a *App) VerificaIntegrita() {
	a.mu.Lock()
	voci := a.voci()
	cartelle := a.cartelle
	a.mu.Unlock()
	reg, err := a.cartellaRegistro()
	if err != nil {
		return
	}
	go func() {
		for _, v := range voci {
			s := installa.LeggiStato(reg, v.Nome, runtime.GOOS, cartelle)
			if !s.Presente || !s.Nostro || s.Percorso == "" {
				continue
			}
			m, ok := integrita.Carica(reg, installa.NomeProgramma(v.Nome))
			if !ok {
				continue
			}
			e := integrita.Confronta(s.Percorso, m)
			a.mu.Lock()
			a.integri[v.ID] = e
			a.mu.Unlock()
			if !e.Integro() {
				a.log(fmt.Sprintf("%s: %d file diversi da come CentralHub li ha installati, %d mancanti (primo: %s)", v.Nome, len(e.Diversi), len(e.Mancano), append(append([]string{}, e.Diversi...), e.Mancano...)[0]))
			}
			a.avvisa(v.ID)
		}
	}()
}
