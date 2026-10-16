// SPDX-License-Identifier: MIT

// Package github parla con l'API di GitHub quel tanto che serve: trovare
// l'ultima release di un repository, elencare le altre e scaricarne i file.
// Solo lettura.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BasePredefinita è l'indirizzo dell'API pubblica.
const BasePredefinita = "https://api.github.com"

// WebPredefinito è il sito pubblico, da cui si scaricano i file dei
// repository pubblici.
const WebPredefinito = "https://github.com"

// Client interroga GitHub. Il token è facoltativo: senza, si leggono solo i
// repository pubblici.
type Client struct {
	Base      string
	Token     string
	UserAgent string
	HTTP      *http.Client
	// WebDiProva sostituisce il sito pubblico nelle prove.
	WebDiProva string
}

// Web restituisce l'indirizzo del sito pubblico da cui scaricare i file,
// oppure "" se vanno presi dall'API.
//
// Senza token l'API concede 60 richieste all'ora per indirizzo IP, e ogni
// file scaricato dall'API ne consuma una: una release con ventisei pacchetti
// esaurisce il limite a metà. I file dei repository pubblici hanno però un
// indirizzo pubblico (github.com/…/releases/download/…) che non passa
// dall'API e non consuma niente. Con un token si resta sull'API: è l'unica
// strada per i repository privati, e lì il limite è di 5000 all'ora.
func (c *Client) Web() string {
	if c.Token != "" {
		return ""
	}
	if c.WebDiProva != "" {
		return c.WebDiProva
	}
	if c.Base == BasePredefinita {
		return WebPredefinito
	}
	return ""
}

// Nuovo prepara un client. Nessun limite di tempo sull'intera richiesta (un
// asset grande può durare minuti): i limiti stanno sulla connessione e
// sull'attesa della risposta, il resto lo governa il context.
func Nuovo(token, userAgent string) *Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		// 30 s e non 15: il limite comprende la ricerca del nome, che su un
		// Windows con un server DNS IPv6 irraggiungibile dura 15 s esatti
		// (visto il 14/10/2026: ogni controllo falliva, il browser no).
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	return &Client{Base: BasePredefinita, Token: strings.TrimSpace(token), UserAgent: userAgent,
		HTTP: &http.Client{Transport: tr, CheckRedirect: soloAllOrigine}}
}

// soloAllOrigine toglie il token quando un redirect porta su un altro host:
// GitHub rimanda gli asset e gli archivi dei sorgenti a domini di storage, e
// il token non deve mai arrivarci. net/http lo fa già fra domini diversi; qui
// è esplicito e vale anche per una porta diversa dello stesso host.
func soloAllOrigine(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("troppi redirect")
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// Asset è un file allegato a una release.
type Asset struct {
	Nome       string `json:"name"`
	Dimensione int64  `json:"size"`
	URL        string `json:"url"` // indirizzo dell'API: l'unico che funziona nei repo privati
	// Pubblico è l'indirizzo sul sito: per i repository pubblici, non
	// consuma il limite di richieste dell'API.
	Pubblico string `json:"browser_download_url"`
	// Digest è "sha256:<esadecimale>" quando GitHub lo fornisce.
	Digest string `json:"digest"`
}

// Release è una release pubblicata.
type Release struct {
	Tag        string  `json:"tag_name"`
	Nome       string  `json:"name"`
	Pubblicata string  `json:"published_at"`
	Assets     []Asset `json:"assets"`
	Tarball    string  `json:"tarball_url"`
	Zipball    string  `json:"zipball_url"`
	// Bozza e Prerelease: una bozza non è pubblicata; una prerelease sì, ma
	// non è mai "l'ultima".
	Bozza      bool `json:"draft"`
	Prerelease bool `json:"prerelease"`
}

// Errore è un errore già spiegato per l'utente; Tipo permette
// all'interfaccia di distinguere i casi.
type Errore struct {
	Tipo      string // rete | token | accesso | limite | nessuna-release | server
	Messaggio string
}

func (e *Errore) Error() string { return e.Messaggio }

func (c *Client) richiesta(ctx context.Context, indirizzo, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indirizzo, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		// Sui redirect verso altri host lo toglie soloAllOrigine.
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Errore{"rete", "GitHub non è raggiungibile: rete assente, bloccata o troppo lenta (" + causa(err) + ")"}
	}
	return resp, nil
}

func causa(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// spiega trasforma una risposta di errore di GitHub in un messaggio chiaro.
func (c *Client) spiega(resp *http.Response, repo string) error {
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var api struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(corpo, &api)
	h := resp.Header
	limite := h.Get("X-RateLimit-Remaining") == "0" || resp.StatusCode == http.StatusTooManyRequests || h.Get("Retry-After") != ""
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return &Errore{"token", "Il token di GitHub non è valido o è scaduto: va rigenerato e reinserito nelle impostazioni"}
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && limite:
		quando := ""
		if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			quando = " Riprova dopo le " + time.Unix(s, 0).Format("15:04") + "."
		}
		senza := ""
		if c.Token == "" {
			senza = " Per i repository pubblici GitHub ne concede 60 all'ora a tutto il computer."
		}
		return &Errore{"limite", "Raggiunto il limite di richieste a GitHub." + quando + senza}
	case resp.StatusCode == http.StatusForbidden:
		return &Errore{"accesso", "GitHub rifiuta l'accesso a " + repo + ": il token non ha il permesso Contents (sola lettura) su questo repository"}
	case resp.StatusCode == http.StatusNotFound:
		if c.Token == "" {
			return &Errore{"nessuna-release", repo + ": nessuna release pubblicata, oppure il repository non esiste o è privato (per i privati serve il token)"}
		}
		return &Errore{"nessuna-release", repo + ": nessuna release pubblicata, oppure il repository non esiste o non è fra quelli che il token può leggere"}
	}
	msg := fmt.Sprintf("GitHub ha risposto con l'errore %d", resp.StatusCode)
	if api.Message != "" {
		msg += ": " + api.Message
	}
	return &Errore{"server", msg}
}

// Ultima restituisce l'ultima release pubblicata (bozze e prerelease
// escluse: è ciò che GitHub chiama "latest").
func (c *Client) Ultima(ctx context.Context, repo string) (*Release, error) {
	resp, err := c.richiesta(ctx, c.Base+"/repos/"+repo+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.spiega(resp, repo)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return nil, &Errore{"server", "risposta di GitHub non leggibile: " + err.Error()}
	}
	if r.Tag == "" {
		return nil, &Errore{"server", "GitHub ha restituito una release senza tag"}
	}
	return &r, nil
}

// Elenco restituisce le release pubblicate di un repository, dalla più
// recente: al massimo trenta, prerelease comprese, bozze escluse. Serve a
// far scegliere all'utente quale versione installare.
func (c *Client) Elenco(ctx context.Context, repo string) ([]Release, error) {
	resp, err := c.richiesta(ctx, c.Base+"/repos/"+repo+"/releases?per_page=30", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.spiega(resp, repo)
	}
	var tutte []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&tutte); err != nil {
		return nil, &Errore{"server", "risposta di GitHub non leggibile: " + err.Error()}
	}
	var pubblicate []Release
	for _, r := range tutte {
		if !r.Bozza && r.Tag != "" {
			pubblicate = append(pubblicate, r)
		}
	}
	return pubblicate, nil
}

// Apri apre in lettura un indirizzo dell'API (un asset, o l'archivio dei
// sorgenti). Chi chiama chiude il corpo. La dimensione è -1 se ignota.
func (c *Client) Apri(ctx context.Context, indirizzo, repo string, asset bool) (io.ReadCloser, int64, error) {
	accept := "application/vnd.github+json"
	if asset {
		accept = "application/octet-stream"
	}
	resp, err := c.richiesta(ctx, indirizzo, accept)
	if err != nil {
		return nil, -1, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, -1, c.spiega(resp, repo)
	}
	return resp.Body, resp.ContentLength, nil
}

// ApriWeb apre in lettura un file sul sito pubblico (un pacchetto di una
// release, o l'archivio dei sorgenti di un tag). Niente token e niente
// intestazioni dell'API. Chi chiama chiude il corpo.
func (c *Client) ApriWeb(ctx context.Context, indirizzo string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indirizzo, nil)
	if err != nil {
		return nil, -1, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, -1, ctx.Err()
		}
		return nil, -1, &Errore{"rete", "GitHub non è raggiungibile: rete assente, bloccata o troppo lenta (" + causa(err) + ")"}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, -1, &Errore{"server", "il file non c'è più su GitHub (la release è stata modificata?)"}
		case http.StatusTooManyRequests:
			return nil, -1, &Errore{"limite", "GitHub chiede di rallentare i download: riprova fra qualche minuto"}
		}
		return nil, -1, &Errore{"server", fmt.Sprintf("GitHub ha risposto con l'errore %d", resp.StatusCode)}
	}
	return resp.Body, resp.ContentLength, nil
}

// SorgentiWeb restituisce gli indirizzi pubblici degli archivi dei sorgenti
// di un tag (zip e tar.gz).
func SorgentiWeb(web, repo, tag string) (zip, tar string) {
	parti := strings.Split(tag, "/") // un tag può contenere barre
	for i := range parti {
		parti[i] = url.PathEscape(parti[i])
	}
	base := web + "/" + repo + "/archive/refs/tags/" + strings.Join(parti, "/")
	return base + ".zip", base + ".tar.gz"
}
