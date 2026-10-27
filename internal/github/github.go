// SPDX-License-Identifier: MIT

// Package github parla con l'API di GitHub quel tanto che serve: trovare
// l'ultima release di un repository, elencare le altre e scaricarne i file.
// Solo lettura.
package github

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
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
	// Note è il testo della release (Markdown), com'è su GitHub.
	Note string `json:"body"`
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
	return c.richiestaDa(ctx, indirizzo, accept, 0)
}

// richiestaDa è richiesta con, se da > 0, la richiesta del file a partire da
// quel byte (Range). L'intestazione segue i redirect verso lo storage.
func (c *Client) richiestaDa(ctx context.Context, indirizzo, accept string, da int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indirizzo, nil)
	if err != nil {
		return nil, err
	}
	if da > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(da, 10)+"-")
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
	if err := c.controllaIndirizzo(indirizzo, repo, false); err != nil {
		return nil, -1, err
	}
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
// release, o l'archivio dei sorgenti di un tag) del repository repo. Niente
// token e niente intestazioni dell'API. Chi chiama chiude il corpo.
func (c *Client) ApriWeb(ctx context.Context, indirizzo, repo string) (io.ReadCloser, int64, error) {
	if err := c.controllaIndirizzo(indirizzo, repo, true); err != nil {
		return nil, -1, err
	}
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

// ---------------------------------------------------------------- indirizzi di download

// Gli indirizzi dei file di una release arrivano dall'API di GitHub o da
// ultime.json, che sta sul disco dell'utente. Si aprono solo se stanno dove
// GitHub tiene i file di QUEL repository: così un ultime.json alterato a mano,
// o una risposta manomessa, non può far scaricare da un altro host né
// mandargli il token (Apri e ApriDa lo mettono nella richiesta).

// prefissiAPI sono i percorsi, sotto l'API, dei file di un repository.
func prefissiAPI(repo string) []string {
	return []string{"/repos/" + repo + "/releases/assets/", "/repos/" + repo + "/zipball/", "/repos/" + repo + "/tarball/"}
}

// prefissiWeb sono i percorsi, sotto il sito pubblico, dei file di un repository.
func prefissiWeb(repo string) []string {
	return []string{"/" + repo + "/releases/download/", "/" + repo + "/archive/refs/tags/"}
}

// indirizzoLecito dice se indirizzo sta sotto base (stesso schema e stesso
// host) in uno dei percorsi dati, con qualcosa dopo. Il nome del repository si
// confronta senza badare alle maiuscole: GitHub le restituisce com'è scritto
// il repository, la configurazione può averle diverse.
func indirizzoLecito(base, indirizzo string, prefissi []string) bool {
	if base == "" || strings.ContainsAny(indirizzo, "\\\r\n") {
		return false
	}
	b, err := url.Parse(base)
	if err != nil {
		return false
	}
	u, err := url.Parse(indirizzo)
	if err != nil || u.Scheme != b.Scheme || !strings.EqualFold(u.Host, b.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	// niente «..», «.» o barre doppie: il prefisso giusto non deve poter uscire dalla cartella
	if u.Path == "" || path.Clean(u.Path) != u.Path {
		return false
	}
	p := strings.ToLower(u.Path)
	for _, pref := range prefissi {
		pref = strings.ToLower(strings.TrimRight(b.Path, "/") + pref)
		if strings.HasPrefix(p, pref) && len(p) > len(pref) {
			return true
		}
	}
	return false
}

// controllaIndirizzo rifiuta un indirizzo che non sta fra i file del
// repository: sull'API se web è falso, sul sito pubblico se è vero.
func (c *Client) controllaIndirizzo(indirizzo, repo string, web bool) error {
	base, prefissi := c.Base, prefissiAPI(repo)
	if web {
		base, prefissi = c.Web(), prefissiWeb(repo)
	}
	if repo == "" || !indirizzoLecito(base, indirizzo, prefissi) {
		return &Errore{"indirizzo", "un indirizzo della release non è fra i file di " + repo + " su GitHub (" + descriviIndirizzo(indirizzo) + "): per sicurezza non lo apro"}
	}
	return nil
}

// descriviIndirizzo accorcia un indirizzo per un messaggio: l'host e l'inizio del percorso.
func descriviIndirizzo(indirizzo string) string {
	u, err := url.Parse(indirizzo)
	if err != nil || u.Host == "" {
		return "illeggibile"
	}
	p := u.Path
	if len(p) > 60 {
		p = p[:60] + "…"
	}
	return u.Host + p
}

// IndirizziLeciti dice se tutti gli indirizzi di download di una release (i
// file, gli archivi dei sorgenti) stanno fra i file del repository. Serve a
// non riusare dalla cache una release i cui indirizzi sono stati cambiati.
func (c *Client) IndirizziLeciti(repo string, r *Release) bool {
	if r == nil {
		return false
	}
	web := c.Web()
	for _, a := range r.Assets {
		if !indirizzoLecito(c.Base, a.URL, prefissiAPI(repo)) {
			return false
		}
		// con un token il sito pubblico non si usa mai (Web() è vuoto): l'indirizzo non conta
		if web != "" && a.Pubblico != "" && !indirizzoLecito(web, a.Pubblico, prefissiWeb(repo)) {
			return false
		}
	}
	for _, sorgente := range []string{r.Zipball, r.Tarball} {
		if sorgente != "" && !indirizzoLecito(c.Base, sorgente, prefissiAPI(repo)) {
			return false
		}
	}
	return true
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

// TagPubblico dice qual è l'ultima release di un repository chiedendolo al
// sito e non all'API: GET github.com/<repo>/releases/latest risponde con un
// redirect a .../releases/tag/<tag>, e quella richiesta non consuma il
// limite di 60 richieste all'ora dell'API. Vale solo per i repository
// pubblici senza token (Web() non vuoto); con un errore qualunque chi
// chiama ripiega sull'API, che dà anche il messaggio giusto.
//
// "Ultima" è la stessa di GitHub: bozze e prerelease escluse.
func (c *Client) TagPubblico(ctx context.Context, repo string) (string, error) {
	web := c.Web()
	if web == "" {
		return "", errors.New("senza il sito pubblico non si può chiedere il tag")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, web+"/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	senzaRedirect := *c.HTTP
	senzaRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := senzaRedirect.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &Errore{"rete", "GitHub non è raggiungibile: rete assente, bloccata o troppo lenta (" + causa(err) + ")"}
	}
	defer resp.Body.Close()
	return tagDaRedirect(resp.StatusCode, resp.Header.Get("Location"))
}

// tagDaRedirect legge il tag dall'indirizzo a cui GitHub rimanda
// /releases/latest. Senza release rimanda a /releases (o risponde 404).
func tagDaRedirect(stato int, dove string) (string, error) {
	if stato < 300 || stato > 399 || dove == "" {
		return "", &Errore{"nessuna-release", "il sito non indica nessuna release"}
	}
	u, err := url.Parse(dove)
	if err != nil {
		return "", err
	}
	const segno = "/releases/tag/"
	i := strings.LastIndex(u.EscapedPath(), segno)
	if i < 0 {
		return "", &Errore{"nessuna-release", "il sito non indica nessuna release"}
	}
	tag, err := url.PathUnescape(u.EscapedPath()[i+len(segno):])
	if err != nil || tag == "" || strings.ContainsAny(tag, "\r\n") {
		return "", &Errore{"server", "il sito ha indicato un tag illeggibile"}
	}
	return tag, nil
}

// ePerSomme dice se un asset è il file con le somme SHA-256 della release.
func ePerSomme(nome string) bool {
	n := strings.ToLower(nome)
	return n == "sha256sums.txt" || n == "sha256sums"
}

// FileSomme restituisce l'asset con le somme di controllo della release
// (SHA256SUMS.txt), se c'è.
func (r *Release) FileSomme() (Asset, bool) {
	for _, a := range r.Assets {
		if ePerSomme(a.Nome) {
			return a, true
		}
	}
	return Asset{}, false
}

// maxSomme: il file delle somme è di poche righe.
const maxSomme = 1 << 20

// AnalizzaSomme legge l'uscita di sha256sum (`<sha256>  <nome>` o
// `<sha256> *<nome>`, un file per riga): nome -> somma in minuscolo. Le righe
// che non sembrano una somma si ignorano.
func AnalizzaSomme(testo string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(testo))
	for sc.Scan() {
		riga := strings.TrimSpace(sc.Text())
		if len(riga) < 66 {
			continue
		}
		somma := strings.ToLower(riga[:64])
		if b, err := hex.DecodeString(somma); err != nil || len(b) != 32 {
			continue
		}
		nome := strings.TrimSpace(riga[64:])
		nome = strings.TrimPrefix(nome, "*")
		nome = strings.TrimPrefix(nome, "./")
		if nome == "" || strings.ContainsAny(nome, "/\\") {
			continue
		}
		m[nome] = somma
	}
	return m
}

// FileFirma restituisce l'asset con la firma di SHA256SUMS.txt
// (SHA256SUMS.txt.sig), se c'è.
func (r *Release) FileFirma() (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Nome, "SHA256SUMS.txt.sig") {
			return a, true
		}
	}
	return Asset{}, false
}

// piccolo scarica un asset di poche righe (somme, firma).
func (c *Client) piccolo(ctx context.Context, repo string, a Asset) ([]byte, error) {
	var corpo io.ReadCloser
	var err error
	if c.Web() != "" && a.Pubblico != "" {
		corpo, _, err = c.ApriWeb(ctx, a.Pubblico, repo)
	} else {
		corpo, _, err = c.Apri(ctx, a.URL, repo, true)
	}
	if err != nil {
		return nil, err
	}
	defer corpo.Close()
	b, err := io.ReadAll(io.LimitReader(corpo, maxSomme+1))
	if err != nil {
		return nil, &Errore{"rete", "il file " + a.Nome + " non si è letto per intero: " + err.Error()}
	}
	if len(b) > maxSomme {
		return nil, &Errore{"server", "il file " + a.Nome + " è troppo grande"}
	}
	return b, nil
}

// Somme legge il file SHA256SUMS.txt di una release. Restituisce nil, nil se
// la release non lo pubblica; un errore se lo pubblica e non si riesce a
// leggerlo (una rete che balbetta non deve far saltare il controllo).
func (c *Client) Somme(ctx context.Context, repo string, r *Release) (map[string]string, error) {
	somme, _, err := c.SommeEFirma(ctx, repo, r)
	return somme, err
}

// SommeEFirma legge anche il testo grezzo di SHA256SUMS.txt e la firma
// (SHA256SUMS.txt.sig, "" se la release non la pubblica): la firma si
// verifica sul testo grezzo, byte per byte.
func (c *Client) SommeEFirma(ctx context.Context, repo string, r *Release) (somme map[string]string, grezzo []byte, err error) {
	a, ok := r.FileSomme()
	if !ok {
		return nil, nil, nil
	}
	if grezzo, err = c.piccolo(ctx, repo, a); err != nil {
		return nil, nil, err
	}
	return AnalizzaSomme(string(grezzo)), grezzo, nil
}

// Firma legge la firma di SHA256SUMS.txt: "" se la release non ne ha una.
func (c *Client) Firma(ctx context.Context, repo string, r *Release) (string, error) {
	a, ok := r.FileFirma()
	if !ok {
		return "", nil
	}
	b, err := c.piccolo(ctx, repo, a)
	return strings.TrimSpace(string(b)), err
}

// ConSomme restituisce una copia della release in cui gli asset senza digest
// ricevono la somma dal file delle somme. Il digest di GitHub, se c'è, resta.
func (r *Release) ConSomme(somme map[string]string) *Release {
	copia := *r
	copia.Assets = append([]Asset(nil), r.Assets...)
	for i, a := range copia.Assets {
		if a.Digest == "" {
			if s, ok := somme[a.Nome]; ok {
				copia.Assets[i].Digest = "sha256:" + s
			}
		}
	}
	return &copia
}

// Pezzo è un file aperto dal byte Inizio: Inizio è quello chiesto, oppure 0 se
// il server ha mandato il file intero (non sa riprendere a metà).
type Pezzo struct {
	Corpo  io.ReadCloser
	Inizio int64
	// Totale è la lunghezza dell'intero file, -1 se ignota.
	Totale int64
}

// ApriDa apre un asset (sull'API se web è falso, altrimenti un indirizzo del
// sito pubblico) a partire dal byte da. Serve a riprendere un download
// interrotto invece di ricominciarlo.
func (c *Client) ApriDa(ctx context.Context, indirizzo, repo string, web bool, da int64) (*Pezzo, error) {
	if err := c.controllaIndirizzo(indirizzo, repo, web); err != nil {
		return nil, err
	}
	var resp *http.Response
	var err error
	if web {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, indirizzo, nil)
		if e != nil {
			return nil, e
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if da > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(da, 10)+"-")
		}
		if resp, err = c.HTTP.Do(req); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &Errore{"rete", "GitHub non è raggiungibile: rete assente, bloccata o troppo lenta (" + causa(err) + ")"}
		}
	} else if resp, err = c.richiestaDa(ctx, indirizzo, "application/octet-stream", da); err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return &Pezzo{Corpo: resp.Body, Inizio: 0, Totale: resp.ContentLength}, nil
	case http.StatusPartialContent:
		inizio, totale, ok := leggiContentRange(resp.Header.Get("Content-Range"))
		if !ok || inizio != da {
			resp.Body.Close()
			return nil, &Errore{"server", "il server ha risposto a metà file con un intervallo diverso da quello chiesto"}
		}
		return &Pezzo{Corpo: resp.Body, Inizio: inizio, Totale: totale}, nil
	case http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		return nil, &Errore{"intervallo", "il file parziale è più grande di quello pubblicato"}
	}
	defer resp.Body.Close()
	if web {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, &Errore{"server", "il file non c'è più su GitHub (la release è stata modificata?)"}
		case http.StatusTooManyRequests:
			return nil, &Errore{"limite", "GitHub chiede di rallentare i download: riprova fra qualche minuto"}
		}
		return nil, &Errore{"server", fmt.Sprintf("GitHub ha risposto con l'errore %d", resp.StatusCode)}
	}
	return nil, c.spiega(resp, repo)
}

// leggiContentRange legge "bytes 100-199/1000" → (100, 1000); il totale è -1
// se il server scrive "*".
func leggiContentRange(v string) (inizio, totale int64, ok bool) {
	v = strings.TrimSpace(v)
	v, si := strings.CutPrefix(v, "bytes ")
	if !si {
		return 0, 0, false
	}
	span, tot, si := strings.Cut(v, "/")
	if !si {
		return 0, 0, false
	}
	da, _, si := strings.Cut(span, "-")
	if !si {
		return 0, 0, false
	}
	inizio, err := strconv.ParseInt(strings.TrimSpace(da), 10, 64)
	if err != nil || inizio < 0 {
		return 0, 0, false
	}
	totale = -1
	if tot = strings.TrimSpace(tot); tot != "*" {
		if totale, err = strconv.ParseInt(tot, 10, 64); err != nil {
			return 0, 0, false
		}
	}
	return inizio, totale, true
}

// ApplicaFirmate restituisce una copia della release in cui ogni asset
// elencato nelle somme (già verificate con la firma) ha quella somma come
// digest. Se GitHub dava un digest diverso per lo stesso file la release è
// incoerente: errore.
func (r *Release) ApplicaFirmate(somme map[string]string) (*Release, error) {
	copia := *r
	copia.Assets = append([]Asset(nil), r.Assets...)
	for i, a := range copia.Assets {
		s, ok := somme[a.Nome]
		if !ok {
			continue
		}
		if a.Digest != "" && !strings.EqualFold(a.Digest, "sha256:"+s) {
			return nil, fmt.Errorf("per %s il digest di GitHub non coincide con la somma firmata: la release è stata alterata", a.Nome)
		}
		copia.Assets[i].Digest = "sha256:" + s
	}
	return &copia, nil
}
