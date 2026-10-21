// SPDX-License-Identifier: MIT

package main

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"centralhub/internal/config"
	"centralhub/internal/firma"
	"centralhub/internal/github"
	"centralhub/internal/installa"
)

// ambiente prepara un'app di prova: configurazione in una cartella
// temporanea (mai quella vera), un GitHub finto, cartelle di installazione di
// prova, e le cartelle in cui un'installazione vera scriverebbe (menu delle
// applicazioni, menu Start) spostate anche loro.
type ambiente struct {
	t        *testing.T
	app      *App
	srv      *httptest.Server
	tag      string
	dmg      bool              // su macOS: la release ha un .dmg invece dello zip
	dmgFatti map[string][]byte // hdiutil non è deterministico: lo stesso .dmg per tutta la prova
	altre    []string          // release più vecchie, dalla più recente
	extra    []string          // altri pacchetti nelle release, oltre a quello di questo sistema
	token    map[string]string // repository -> token preteso (privato)
	base     string
	nomeFile string
	visti    map[string]string // repository -> ultimo token ricevuto
	// cestinati: ciò che una disinstallazione ha mandato nel cestino (qui
	// finto: le prove non riempiono quello vero)
	cestinati []string
	// somme: come la release pubblica la somma di controllo del pacchetto:
	// "" o "digest" = il digest di GitHub; "file" = solo SHA256SUMS.txt;
	// "sbagliata" = SHA256SUMS.txt con una somma che non torna; "nessuna" = niente
	somme string
	// firma: "" = non firmata; "valida" = SHA256SUMS.txt.sig con la chiave
	// fidata; "altra" = firmata con un'altra chiave; "alterata" = firma giusta
	// ma le somme sono state cambiate dopo (firma che non torna)
	firma string
	// pubblicata: data di pubblicazione delle release finte (RFC 3339); vuota = non indicata
	pubblicata string
	web        bool // il sito pubblico finto risponde: i repository sono "gratis"
	// quante richieste ha avuto l'API e quante il sito (la sonda del tag)
	api, sonda    int
	fatti         map[string][]byte
	chiaveDiProva ed25519.PrivateKey
}

// testoSomme è il SHA256SUMS.txt che la release finta pubblica.
func (e *ambiente) testoSomme(tag string) string {
	somma := sha(e.pacchetto(tag))
	if e.somme == "sbagliata" {
		somma = strings.Repeat("0", 64)
	}
	return fmt.Sprintf("%s  %s\n", somma, e.nomeFile)
}

func nuovoAmbiente(t *testing.T) *ambiente {
	t.Helper()
	e := &ambiente{t: t, tag: "v1.0.0", base: t.TempDir(), token: map[string]string{}, visti: map[string]string{}}
	t.Setenv(config.VariabileCartella, filepath.Join(e.base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(e.base, "xdg"))
	t.Setenv("APPDATA", filepath.Join(e.base, "appdata"))
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parti := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parti) < 3 {
			http.NotFound(w, r)
			return
		}
		repo := parti[1] + "/" + parti[2]
		if parti[0] == "web" {
			// il sito pubblico: /web/<o>/<r>/releases/latest rimanda al tag
			e.sonda++
			if len(parti) == 5 && parti[4] == "latest" {
				w.Header().Set("Location", "/web/"+repo+"/releases/tag/"+e.tag)
				w.WriteHeader(http.StatusFound)
				return
			}
			http.NotFound(w, r)
			return
		}
		if parti[0] == "repos" {
			e.api++
		}
		avuto := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		e.visti[repo] = avuto
		if preteso := e.token[repo]; preteso != "" && avuto != preteso {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch parti[0] {
		case "repos":
			release := func(tag string) github.Release {
				b := e.pacchetto(tag)
				r := github.Release{Tag: tag, Nome: "Release " + tag, Pubblicata: e.pubblicata, Note: "Novità della " + tag, Assets: []github.Asset{
					{Nome: e.nomeFile, Dimensione: int64(len(b)), URL: e.srv.URL + "/asset/" + repo + "/" + tag},
					{Nome: "note.txt", Dimensione: 4, URL: e.srv.URL + "/note/" + repo},
				}}
				switch e.somme {
				case "", "digest":
					r.Assets[0].Digest = "sha256:" + sha(b)
				case "file", "sbagliata":
					r.Assets = append(r.Assets, github.Asset{Nome: "SHA256SUMS.txt", Dimensione: int64(len(e.testoSomme(tag))), URL: e.srv.URL + "/sums/" + repo + "/" + tag})
				}
				if e.firma != "" {
					r.Assets = append(r.Assets, github.Asset{Nome: "SHA256SUMS.txt", Dimensione: int64(len(e.testoSomme(tag))), URL: e.srv.URL + "/sums/" + repo + "/" + tag},
						github.Asset{Nome: "SHA256SUMS.txt.sig", Dimensione: 89, URL: e.srv.URL + "/sig/" + repo + "/" + tag})
				}
				for _, n := range e.extra {
					r.Assets = append(r.Assets, github.Asset{Nome: n, Dimensione: 4, URL: e.srv.URL + "/note/" + repo})
				}
				return r
			}
			if parti[len(parti)-1] == "latest" {
				json.NewEncoder(w).Encode(release(e.tag))
				return
			}
			// l'elenco: l'ultima, le più vecchie e una bozza, che non deve comparire
			tutte := []github.Release{release(e.tag)}
			for _, t := range e.altre {
				tutte = append(tutte, release(t))
			}
			bozza := release("v9.9.9")
			bozza.Bozza = true
			json.NewEncoder(w).Encode(append(tutte, bozza))
		case "asset":
			w.Write(e.pacchetto(parti[3]))
		case "note":
			w.Write([]byte("note"))
		case "sums":
			w.Write([]byte(e.testoSomme(parti[3])))
		case "sig":
			testo := e.testoSomme(parti[3])
			chiave := e.chiaveDiProva
			if e.firma == "altra" {
				_, chiave, _ = firma.Nuova()
			}
			sig, _ := firma.Firma(chiave, repo, parti[3], []byte(testo))
			switch e.firma {
			case "alterata":
				// la firma è delle somme vere; quelle servite sono diverse
				sig, _ = firma.Firma(chiave, repo, parti[3], []byte(strings.Replace(testo, e.nomeFile, "Altro.zip", 1)))
			case "riusata":
				// firma vera, ma fatta per un'altra release
				sig, _ = firma.Firma(chiave, repo, "v0.0.1", []byte(testo))
			case "estranea":
				sig = "untrusted comment: firma di minisign\nRWQ..."
			}
			w.Write([]byte(sig + "\n"))
		}
	}))
	t.Cleanup(e.srv.Close)
	e.app = NuovaApp()
	e.app.nuovoCli = e.cliente
	pub, priv, _ := firma.Nuova()
	e.chiaveDiProva = priv
	chiaviFidate = func() []ed25519.PublicKey { return []ed25519.PublicKey{pub} }
	t.Cleanup(func() { chiaviFidate = func() []ed25519.PublicKey { return firma.Fidate() } })
	e.app.cestina = func(_, percorso string) error {
		e.cestinati = append(e.cestinati, percorso)
		return os.RemoveAll(percorso)
	}
	e.app.carica()
	e.app.cartelle = installa.Cartelle{Utente: filepath.Join(e.base, "utente"), Sistema: filepath.Join(e.base, "sistema")}
	return e
}

// pacchetto costruisce l'asset installabile su questo sistema, con dentro il
// tag (per riconoscere la versione installata).
func (e *ambiente) pacchetto(tag string) []byte {
	e.t.Helper()
	if b, ok := e.fatti[tag]; ok {
		return b
	}
	b := e.costruisci(tag)
	if e.fatti == nil {
		e.fatti = map[string][]byte{}
	}
	e.fatti[tag] = b // lo stesso per tutta la prova: la somma deve tornare
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// cliente è il client verso il GitHub finto; con e.web risponde anche il sito.
func (e *ambiente) cliente(token string) *github.Client {
	c := github.Nuovo(token, "CentralHub-prova")
	c.Base = e.srv.URL
	if e.web {
		c.WebDiProva = e.srv.URL + "/web"
	}
	return c
}

func (e *ambiente) costruisci(tag string) []byte {
	e.t.Helper()
	dir := e.t.TempDir()
	switch runtime.GOOS {
	case "linux":
		e.nomeFile = "Prova-Linux-x86_64.AppImage"
		if runtime.GOARCH == "arm64" {
			e.nomeFile = "Prova-Linux-aarch64.AppImage"
		}
		return []byte("#!/bin/sh\n# " + tag + "\nexit 1\n")
	case "windows":
		e.nomeFile = "Prova-Windows.zip"
		f := filepath.Join(dir, "p.zip")
		out, _ := os.Create(f)
		z := zip.NewWriter(out)
		w, _ := z.Create("Prova-Windows/Prova.exe")
		w.Write([]byte(tag))
		z.Close()
		out.Close()
		b, _ := os.ReadFile(f)
		return b
	default:
		app := "Prova-AppleSilicon"
		if runtime.GOARCH == "amd64" {
			app = "Prova-Intel"
		}
		e.nomeFile = app + ".zip"
		exe := filepath.Join(dir, app+".app", "Contents", "MacOS", "Prova")
		os.MkdirAll(filepath.Dir(exe), 0o755)
		os.WriteFile(exe, []byte(tag), 0o755)
		if e.dmg {
			e.nomeFile = "Prova-" + strings.ToLower(runtime.GOARCH) + ".dmg"
			if b, ok := e.dmgFatti[tag]; ok {
				return b
			}
			f := filepath.Join(dir, "p.dmg")
			if out, err := exec.Command("/usr/bin/hdiutil", "create", "-quiet", "-srcfolder", filepath.Join(dir, app+".app"), "-volname", "Prova", "-format", "UDZO", f).CombinedOutput(); err != nil {
				e.t.Fatalf("hdiutil: %v %s", err, out)
			}
			b, _ := os.ReadFile(f)
			if e.dmgFatti == nil {
				e.dmgFatti = map[string][]byte{}
			}
			e.dmgFatti[tag] = b
			return b
		}
		f := filepath.Join(dir, "p.zip")
		if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", filepath.Join(dir, app+".app"), f).CombinedOutput(); err != nil {
			e.t.Fatalf("ditto: %v %s", err, out)
		}
		b, _ := os.ReadFile(f)
		return b
	}
}

func (e *ambiente) aggiungi(l config.Locale) Vista {
	e.t.Helper()
	v, err := e.app.SalvaRepo(config.Repo{Nome: "Prova", Repo: "https://github.com/a/Prova", Locale: l})
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *ambiente) vista(id string) Vista {
	e.t.Helper()
	for _, v := range e.app.Stato().Repo {
		if v.ID == id {
			return v
		}
	}
	e.t.Fatalf("repository %s non trovato", id)
	return Vista{}
}

func (e *ambiente) voce(id string) voce {
	e.app.mu.Lock()
	defer e.app.mu.Unlock()
	v, _ := e.app.voce(id)
	return v
}

func (e *ambiente) archivia(id string) Vista {
	e.app.archivia(context.Background(), e.voce(id))
	return e.vista(id)
}

func (e *ambiente) aggiorna(id string, sostituisci bool) Vista {
	return e.installa(id, Opzioni{Sostituisci: sostituisci})
}

func (e *ambiente) installa(id string, o Opzioni) Vista {
	e.app.aggiorna(context.Background(), e.voce(id), o)
	return e.vista(id)
}

func TestArchiviazione(t *testing.T) {
	e := nuovoAmbiente(t)
	master := filepath.Join(e.base, "master")
	v := e.aggiungi(config.Locale{Tag: "Prove", CartellaArchivio: master})
	if v.Repo != "a/Prova" || v.UltimaRelease != "" || v.ArchivioAggiornato != "" || v.Tag != "Prove" {
		t.Errorf("repository nuovo: %+v", v)
	}
	v = e.archivia(v.ID)
	if v.EsitoArchivio == nil || !v.EsitoArchivio.OK || v.Archiviata != "v1.0.0" || v.ArchivioAggiornato != "si" {
		t.Fatalf("dopo l'archiviazione: %+v esito %+v", v, v.EsitoArchivio)
	}
	for _, f := range []string{e.nomeFile, "note.txt"} {
		if _, err := os.Stat(filepath.Join(master, "v1.0.0", f)); err != nil {
			t.Errorf("manca %s nella cartella della release", f)
		}
	}
	// archiviare non installa niente
	if v.Installato.Presente || v.EsitoUpdate != nil {
		t.Errorf("l'archiviazione non deve installare: %+v", v.Installato)
	}
	if _, err := os.Stat(filepath.Join(e.base, "utente")); err == nil {
		t.Error("è stata creata la cartella di installazione")
	}
	// esce una release nuova: il LED deve dirlo dopo il controllo
	e.tag = "v1.1.0"
	if _, err := e.app.controlla(context.Background(), e.voce(v.ID)); err != nil {
		t.Fatal(err)
	}
	if v = e.vista(v.ID); v.UltimaRelease != "v1.1.0" || v.ArchivioAggiornato != "no" || v.Archiviata != "v1.0.0" {
		t.Errorf("release nuova non segnalata: %+v", v)
	}
	if v = e.archivia(v.ID); v.ArchivioAggiornato != "si" || v.Archiviata != "v1.1.0" {
		t.Errorf("dopo la seconda archiviazione: %+v", v)
	}
	if v = e.archivia(v.ID); !strings.Contains(v.EsitoArchivio.Messaggio, "già archiviata") {
		t.Errorf("una release già archiviata si salta: %+v", v.EsitoArchivio)
	}
	// senza cartella non si archivia
	if _, err := e.app.ImpostaLocale(v.ID, config.Locale{}); err != nil {
		t.Fatal(err)
	}
	if v = e.archivia(v.ID); v.EsitoArchivio.OK {
		t.Error("senza cartella d'archivio deve dirlo")
	}
}

func TestUpdateInstallaEAggiorna(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{Aggiorna: true})
	// senza aver scelto dove, non si installa
	if v = e.aggiorna(v.ID, false); v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "dove installare") {
		t.Fatalf("senza ambito: %+v", v.EsitoUpdate)
	}
	if v.Installabile != "si" || v.UpdateAggiornato != "no" {
		t.Errorf("selezionato e non installato: da aggiornare. %+v", v)
	}
	if _, err := e.app.ImpostaLocale(v.ID, config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente}); err != nil {
		t.Fatal(err)
	}
	v = e.aggiorna(v.ID, false)
	if !v.EsitoUpdate.OK || !v.Installato.Nostro || v.Installato.Tag != "v1.0.0" || v.UpdateAggiornato != "si" || v.Installato.Ambito != config.AmbitoUtente {
		t.Fatalf("prima installazione: %+v esito %+v", v, v.EsitoUpdate)
	}
	if !strings.HasPrefix(v.Installato.Percorso, filepath.Join(e.base, "utente")) {
		t.Errorf("installato fuori dalla cartella dell'utente: %s", v.Installato.Percorso)
	}
	// la modalità Update non archivia e non lascia il pacchetto in giro
	if v.Archiviata != "" || v.EsitoArchivio != nil {
		t.Error("l'update non deve archiviare")
	}
	if voci, _ := os.ReadDir(filepath.Join(e.base, "config", "scaricati")); len(voci) != 0 {
		t.Errorf("pacchetto rimasto dopo l'installazione: %v", voci)
	}
	if v = e.aggiorna(v.ID, false); !v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "Già aggiornato") || v.Installato.Precedente {
		t.Errorf("seconda esecuzione sulla stessa release: %+v", v.EsitoUpdate)
	}
	e.tag = "v1.1.0"
	e.app.controlla(context.Background(), e.voce(v.ID))
	if v = e.vista(v.ID); v.UpdateAggiornato != "no" {
		t.Errorf("con una release nuova il LED deve essere rosso: %+v", v)
	}
	v = e.aggiorna(v.ID, false)
	if !v.EsitoUpdate.OK || v.Installato.Tag != "v1.1.0" || v.Installato.Precedente || v.UpdateAggiornato != "si" {
		t.Errorf("aggiornamento: %+v esito %+v", v, v.EsitoUpdate)
	}
}

func TestAggiornaTuttiSuCosaLavora(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{})
	if daAggiornare(e.vista(v.ID)) {
		t.Error("non installato e non seguito: «Aggiorna tutti» lo ignora")
	}
	// seguito, ma senza aver scelto dove: si salta (lo chiede l'interfaccia)
	e.app.ImpostaLocale(v.ID, config.Locale{Aggiorna: true})
	if daAggiornare(e.vista(v.ID)) {
		t.Error("seguito senza ambito: va chiesto dove installare, non tentato")
	}
	e.app.ImpostaLocale(v.ID, config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	if !daAggiornare(e.vista(v.ID)) {
		t.Error("seguito e con l'ambito scelto: va installato")
	}
	// una volta installato resta fra quelli da aggiornare anche se non più seguito
	e.aggiorna(v.ID, false)
	e.app.ImpostaLocale(v.ID, config.Locale{})
	if got := e.vista(v.ID); !got.Installato.Presente || !daAggiornare(got) {
		t.Errorf("installato: sempre fra quelli da aggiornare. %+v", got.Installato)
	}
}

func TestProgrammaGiaInstallatoAMano(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{})
	// un programma con lo stesso nome nella cartella di tutto il computer
	sistema := filepath.Join(e.base, "sistema")
	switch runtime.GOOS {
	case "linux":
		os.MkdirAll(filepath.Join(sistema, "Prova"), 0o755)
		os.WriteFile(filepath.Join(sistema, "Prova", "Prova.AppImage"), []byte("a mano"), 0o755)
	case "windows":
		os.MkdirAll(filepath.Join(sistema, "Prova"), 0o755)
	default:
		e.pacchetto("x")
		os.MkdirAll(filepath.Join(sistema, strings.TrimSuffix(e.nomeFile, ".zip")+".app"), 0o755)
	}
	// rilevato anche se non selezionato, e senza conoscerne la versione
	e.app.controlla(context.Background(), e.voce(v.ID))
	v = e.vista(v.ID)
	if !v.Installato.Presente || v.Installato.Nostro || v.Installato.Ambito != config.AmbitoSistema || v.UpdateAggiornato != "no" {
		t.Fatalf("programma già installato non rilevato: %+v", v.Installato)
	}
	v = e.aggiorna(v.ID, false)
	if v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "va confermata") {
		t.Fatalf("senza conferma non si sostituisce: %+v", v.EsitoUpdate)
	}
	if runtime.GOOS == "darwin" { // su Linux e Windows servirebbero i permessi di amministratore veri
		v = e.aggiorna(v.ID, true)
		if !v.EsitoUpdate.OK || !v.Installato.Nostro || v.Installato.Precedente || v.Installato.Ambito != config.AmbitoSistema {
			t.Errorf("con la conferma resta dov'era, aggiornato: %+v esito %+v", v.Installato, v.EsitoUpdate)
		}
	}
}

func (e *ambiente) privato(nome, repo, token string) Vista {
	e.t.Helper()
	v, err := e.app.SalvaRepo(config.Repo{Nome: nome, Repo: repo, Token: token, Privato: true, Locale: config.Locale{Tag: "Miei"}})
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// nessunaChiaveFuoriDaConfig cerca una chiave in tutti i file sotto la cartella
// base e in ciò che torna all'interfaccia, tranne dove può stare: config.json.
func (e *ambiente) nessunaChiaveFuoriDaConfig(chiave string) {
	e.t.Helper()
	filepath.WalkDir(e.base, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasPrefix(filepath.Base(p), "config.json") {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), chiave) {
				e.t.Errorf("una chiave è finita nel file %s", p)
			}
		}
		return nil
	})
	if b, _ := json.Marshal(e.app.Stato()); strings.Contains(string(b), chiave) {
		e.t.Error("una chiave torna all'interfaccia")
	}
}

func TestOgniRepoLaSuaChiave(t *testing.T) {
	e := nuovoAmbiente(t)
	e.token["mio/Uno"], e.token["mio/Due"] = "github_pat_chiave_di_uno", "github_pat_chiave_di_due"
	uno := e.privato("Uno", "mio/Uno", "github_pat_chiave_di_uno")
	due := e.privato("Due", "mio/Due", "github_pat_chiave_di_due")
	pubblico := e.aggiungi(config.Locale{})

	p := e.app.Stato()
	if len(p.Repo) != 3 || !p.Repo[0].Privato || !p.Repo[0].ConChiave || p.Repo[0].MancaChiave || p.Repo[2].Privato || p.Repo[2].ConChiave || p.Chiavi != 2 {
		t.Fatalf("lista: %+v (chiavi %d)", p.Repo, p.Chiavi)
	}
	for _, v := range p.Repo {
		if _, err := e.app.controlla(context.Background(), e.voce(v.ID)); err != nil {
			t.Errorf("%s: %v", v.Nome, err)
		}
	}
	if e.visti["mio/Uno"] != "github_pat_chiave_di_uno" || e.visti["mio/Due"] != "github_pat_chiave_di_due" {
		t.Errorf("ogni repository deve usare la propria chiave: %v", e.visti)
	}
	if e.visti["a/Prova"] != "" {
		t.Errorf("a un repository pubblico non va mandata nessuna chiave: %q", e.visti["a/Prova"])
	}

	// la chiave sta solo in config.json: non nello stato, non nel registro degli installati, non all'interfaccia
	if _, err := e.app.ImpostaLocale(uno.ID, config.Locale{Tag: "Altro", CartellaArchivio: filepath.Join(e.base, "arch")}); err != nil {
		t.Fatal(err)
	}
	e.archivia(uno.ID)
	e.aggiorna(due.ID, false)
	e.nessunaChiaveFuoriDaConfig("github_pat_")

	// modificare senza scrivere una chiave nuova tiene quella salvata
	if _, err := e.app.SalvaRepo(config.Repo{ID: uno.ID, Nome: "Uno", Repo: "mio/Uno", Privato: true, Locale: config.Locale{Tag: "Terzo"}}); err != nil {
		t.Fatal(err)
	}
	if e.app.cfg.Repo[0].Token != "github_pat_chiave_di_uno" || e.vista(uno.ID).Tag != "Terzo" {
		t.Errorf("la chiave salvata dev'essere rimasta: %+v", e.app.cfg.Repo[0])
	}
	if _, err := e.app.controlla(context.Background(), e.voce(uno.ID)); err != nil {
		t.Errorf("con la chiave tenuta il repository risponde ancora: %v", err)
	}
	// una chiave nuova la sostituisce
	if _, err := e.app.SalvaRepo(config.Repo{ID: uno.ID, Nome: "Uno", Repo: "mio/Uno", Token: "github_pat_nuova"}); err != nil {
		t.Fatal(err)
	}
	if e.app.cfg.Repo[0].Token != "github_pat_nuova" {
		t.Error("la chiave nuova non è stata salvata")
	}
	// togliere la spunta «privato» toglie la chiave; cambiare repository non porta con sé la chiave di prima
	if _, err := e.app.SalvaRepo(config.Repo{ID: uno.ID, Nome: "Uno", Repo: "mio/Uno"}); err != nil {
		t.Fatal(err)
	}
	if v := e.vista(uno.ID); v.Privato || v.ConChiave || e.app.cfg.Repo[0].Token != "" {
		t.Errorf("senza la spunta non resta nessuna chiave: %+v", v)
	}
	if _, err := e.app.SalvaRepo(config.Repo{ID: due.ID, Nome: "Due", Repo: "altro/Repo", Privato: true}); err != nil {
		t.Fatal(err)
	}
	if v := e.vista(due.ID); !v.MancaChiave || v.ConChiave {
		t.Errorf("un altro repository non può usare la chiave del precedente: %+v", v)
	}
	// tutti si tolgono, la chiave con loro
	if err := e.app.RimuoviRepo(due.ID); err != nil || len(e.app.Stato().Repo) != 2 {
		t.Errorf("rimozione: %v", err)
	}
	_ = pubblico
}

func TestPrivatoSenzaChiave(t *testing.T) {
	e := nuovoAmbiente(t)
	e.token["mio/Solo"] = "github_pat_arrivata" // GitHub lo mostra solo con la chiave
	v, err := e.app.SalvaRepo(config.Repo{Nome: "Solo", Repo: "mio/Solo", Privato: true, Locale: config.Locale{CartellaArchivio: filepath.Join(e.base, "m")}})
	if err != nil {
		t.Fatal(err)
	}
	if !v.MancaChiave {
		t.Fatalf("manca la chiave e la vista non lo dice: %+v", v)
	}
	v = e.archivia(v.ID)
	if v.EsitoArchivio == nil || v.EsitoArchivio.OK || !strings.Contains(v.Errore, "chiave") || !v.MancaChiave {
		t.Errorf("l'archiviazione deve dire che manca la chiave: %+v %q", v.EsitoArchivio, v.Errore)
	}
	if _, err := e.app.Scelte(v.ID); err == nil || !strings.Contains(err.Error(), "chiave") {
		t.Errorf("anche la scelta della versione: %v", err)
	}
	if k, ok := e.visti["mio/Solo"]; !ok || k != "" {
		t.Errorf("senza chiave si prova anonimi, e basta: %v", e.visti)
	}
	// con la chiave, tutto funziona
	if _, err := e.app.SalvaRepo(config.Repo{ID: v.ID, Nome: "Solo", Repo: "mio/Solo", Token: "github_pat_arrivata", Locale: config.Locale{CartellaArchivio: filepath.Join(e.base, "m")}}); err != nil {
		t.Fatal(err)
	}
	if v = e.archivia(v.ID); !v.EsitoArchivio.OK || v.MancaChiave || v.Errore != "" || !v.Privato {
		t.Errorf("con la chiave: %+v %q", v.EsitoArchivio, v.Errore)
	}
}

// Un repository che era privato e diventa pubblico smette da solo di
// chiedere la chiave.
func TestDiventaPubblico(t *testing.T) {
	e := nuovoAmbiente(t)
	v, err := e.app.SalvaRepo(config.Repo{Nome: "Aperto", Repo: "mio/Aperto", Privato: true, Locale: config.Locale{CartellaArchivio: filepath.Join(e.base, "m")}})
	if err != nil || !v.MancaChiave {
		t.Fatalf("%+v %v", v, err)
	}
	// GitHub lo mostra anche senza chiave
	if _, err := e.app.controlla(context.Background(), e.voce(v.ID)); err != nil {
		t.Fatal(err)
	}
	if v = e.vista(v.ID); v.Privato || v.MancaChiave || v.Errore != "" {
		t.Errorf("dopo il controllo non è più privato: %+v", v)
	}
	if v = e.archivia(v.ID); !v.EsitoArchivio.OK {
		t.Errorf("e si archivia: %+v", v.EsitoArchivio)
	}
	// resta così dopo un riavvio
	altra := NuovaApp()
	altra.carica()
	if p := altra.Stato(); len(p.Repo) != 1 || p.Repo[0].Privato {
		t.Errorf("dopo il riavvio: %+v", p.Repo)
	}
}

func TestImportaAggiungeSenzaTogliere(t *testing.T) {
	e := nuovoAmbiente(t)
	e.privato("Uno", "mio/Uno", "github_pat_locale_uno")
	locale := e.aggiungi(config.Locale{Tag: "Mio", CartellaArchivio: filepath.Join(e.base, "a")})

	// il file porta: lo stesso repository privato senza chiave, uno nuovo
	// privato senza chiave, uno nuovo con chiave, uno con nome già preso
	u, err := e.app.importa(config.Config{Repo: []config.Repo{
		{ID: "1", Nome: "Uno", Repo: "MIO/uno", Privato: true, Locale: config.Locale{Tag: "Dal file", Aggiorna: true}},
		{ID: "2", Nome: "Due", Repo: "mio/Due", Privato: true},
		{ID: "3", Nome: "Tre", Repo: "mio/Tre", Privato: true, Token: "github_pat_dal_file"},
		{ID: "4", Nome: "Prova", Repo: "altro/Prova"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if u.Aggiunti != 2 || u.Aggiornati != 1 || u.Saltati != 1 || u.SenzaChiave != 1 {
		t.Errorf("esito: %+v", u)
	}
	p := e.app.Stato()
	if len(p.Repo) != 4 {
		t.Fatalf("lista dopo l'importazione: %+v", p.Repo)
	}
	byNome := map[string]Vista{}
	for _, v := range p.Repo {
		byNome[v.Nome] = v
	}
	if v := byNome["Uno"]; !v.ConChiave || v.Tag != "Dal file" || !v.Aggiorna {
		t.Errorf("la chiave locale resta e le impostazioni del file si aggiungono: %+v", v)
	}
	if v := byNome["Due"]; !v.MancaChiave {
		t.Errorf("importato senza chiave: dev'esserci, con l'avviso: %+v", v)
	}
	if v := byNome["Tre"]; !v.ConChiave || v.MancaChiave {
		t.Errorf("importato con la chiave: %+v", v)
	}
	if v := byNome["Prova"]; v.ID != locale.ID || v.Tag != "Mio" || v.Repo != "a/Prova" {
		t.Errorf("un repository non nominato dal file non si tocca: %+v", v)
	}
	// la lista e le chiavi sopravvivono a un riavvio
	altra := NuovaApp()
	altra.carica()
	if q := altra.Stato(); len(q.Repo) != 4 || q.Chiavi != 2 {
		t.Errorf("dopo il riavvio: %d repository, %d chiavi", len(q.Repo), q.Chiavi)
	}
}

// Chi aggiorna dalla 0.1.0 trova i suoi repository, con le impostazioni e gli
// esiti, e deve solo rimettere le chiavi.
func TestAggiornamentoDallaZeroUno(t *testing.T) {
	e := nuovoAmbiente(t)
	cartella := filepath.Join(e.base, "Archivio", "Uno")
	vecchia := `{"versione":1,"repo":[],"incorporati":{"mio/uno":{"tag":"Miei","cartellaArchivio":` + jsonStr(cartella) + `}}}`
	f, _ := config.PercorsoFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(vecchia), 0o600); err != nil {
		t.Fatal(err)
	}
	id := config.IDDaRepo("mio/Uno")
	config.SalvaEsiti(map[string]config.Esito{config.ChiaveEsitoVecchia("mio/Uno") + "|" + modoUpdate: {OK: true, Messaggio: "Installato", Tag: "v0.9", Quando: "2026-10-14T10:00:00Z"}})

	altra := NuovaApp()
	altra.carica()
	p := altra.Stato()
	if len(p.Repo) != 1 {
		t.Fatalf("lista: %+v", p.Repo)
	}
	v := p.Repo[0]
	if v.ID != id || v.Nome != "Uno" || v.Tag != "Miei" || v.CartellaArchivio != cartella || !v.MancaChiave {
		t.Errorf("repository ritrovato: %+v", v)
	}
	if v.EsitoUpdate == nil || !v.EsitoUpdate.OK || v.EsitoUpdate.Tag != "v0.9" {
		t.Errorf("l'ultimo esito si ritrova: %+v", v.EsitoUpdate)
	}
	// il file di prima non è stato toccato finché non cambia qualcosa
	if b, _ := os.ReadFile(f); string(b) != vecchia {
		t.Error("l'avvio non deve riscrivere la configurazione")
	}
}

func TestRepoPrivatoSenzaToken(t *testing.T) {
	e := nuovoAmbiente(t)
	e.token["a/Prova"] = "github_pat_che_non_abbiamo"
	v := e.aggiungi(config.Locale{CartellaArchivio: filepath.Join(e.base, "m")})
	v = e.archivia(v.ID)
	if v.EsitoArchivio.OK || !strings.Contains(v.Errore, "privato") {
		t.Fatalf("privato senza token: esito %+v errore %q", v.EsitoArchivio, v.Errore)
	}
}

func TestListaETag(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{Tag: "Storytold", CartellaArchivio: filepath.Join(e.base, "master")})
	if _, err := e.app.SalvaRepo(config.Repo{Nome: "prova", Repo: "b/Altro"}); err == nil {
		t.Error("due repository con lo stesso nome finirebbero sullo stesso programma installato")
	}
	if _, err := e.app.SalvaRepo(config.Repo{Repo: "non valido"}); err == nil {
		t.Error("repository non valido accettato")
	}
	if _, err := e.app.SalvaRepo(config.Repo{Nome: "Altro", Repo: "b/Altro", Locale: config.Locale{Tag: "grafica"}}); err != nil {
		t.Fatal(err)
	}
	if tag := strings.Join(e.app.Stato().Tag, ","); tag != "grafica,Storytold" {
		t.Errorf("tag in ordine alfabetico: %s", tag)
	}
	// modifica: stesso ID, nessun doppione, le impostazioni locali restano
	r := config.Repo{ID: v.ID, Nome: "Prova", Repo: "a/Prova", Asset: "*.zip", Locale: config.Locale{Tag: "Storytold", CartellaArchivio: v.CartellaArchivio}}
	if _, err := e.app.SalvaRepo(r); err != nil {
		t.Fatal(err)
	}
	if got := e.vista(v.ID); got.Asset != "*.zip" || len(e.app.Stato().Repo) != 2 {
		t.Errorf("modifica: %+v", got)
	}
	// togliere il repository non cancella niente dal disco
	e.archivia(v.ID)
	release := filepath.Join(e.base, "master", "v1.0.0", "note.txt")
	if err := e.app.RimuoviRepo(v.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.app.Stato().Repo) != 1 {
		t.Error("repository non tolto")
	}
	if _, err := os.Stat(release); err != nil {
		t.Error("togliere un repository ha cancellato le release archiviate")
	}
	// la lista sopravvive a un riavvio
	altra := NuovaApp()
	altra.carica()
	if p := altra.Stato(); len(p.Repo) != 1 || p.Repo[0].Tag != "grafica" {
		t.Errorf("dopo il riavvio: %+v", p.Repo)
	}
}

func TestSceltaDellaVersione(t *testing.T) {
	e := nuovoAmbiente(t)
	e.altre = []string{"v0.9.0", "v0.8.0"}
	v := e.aggiungi(config.Locale{Ambito: config.AmbitoUtente})
	s, err := e.app.Scelte(v.ID)
	if err != nil || len(s.Release) != 3 || s.Avviso != "" {
		t.Fatalf("release fra cui scegliere (la bozza non conta): %+v %v", s, err)
	}
	if r := s.Release[0]; r.Tag != "v1.0.0" || !r.Ultima || len(r.Pacchetti) != 1 || !r.Pacchetti[0].Consigliato || r.Pacchetti[0].Nome != e.nomeFile || r.Pacchetti[0].Dimensione == 0 {
		t.Errorf("l'ultima release: %+v", r)
	}
	if s.Release[1].Tag != "v0.9.0" || s.Release[1].Ultima || s.Release[2].Tag != "v0.8.0" {
		t.Errorf("le più vecchie: %+v", s.Release[1:])
	}
	// l'utente sceglie una versione che non è l'ultima: resta quella, e
	// «Aggiorna tutti» non la tocca
	v = e.installa(v.ID, Opzioni{Tag: "v0.9.0"})
	if !v.EsitoUpdate.OK || v.Installato.Tag != "v0.9.0" || !v.Installato.Fissata || v.UpdateAggiornato != "no" || daAggiornare(v) {
		t.Fatalf("versione scelta: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	if v = e.installa(v.ID, Opzioni{Tag: "v0.9.0"}); !v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "già installata") {
		t.Errorf("la stessa versione due volte: %+v", v.EsitoUpdate)
	}
	if v = e.installa(v.ID, Opzioni{Tag: "v0.1.0"}); v.EsitoUpdate.OK || v.Installato.Tag != "v0.9.0" {
		t.Errorf("una versione che non esiste: %+v", v.EsitoUpdate)
	}
	// «Aggiorna» sul programma: passa all'ultima, non è più ferma, e la vecchia non resta
	v = e.installa(v.ID, Opzioni{})
	if !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" || v.Installato.Fissata || v.Installato.Precedente || v.UpdateAggiornato != "si" || !daAggiornare(v) {
		t.Errorf("aggiornamento all'ultima: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	// e si può tornare indietro, scegliendolo
	if v = e.installa(v.ID, Opzioni{Tag: "v0.8.0"}); !v.EsitoUpdate.OK || v.Installato.Tag != "v0.8.0" || !v.Installato.Fissata {
		t.Errorf("ritorno a una versione vecchia: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
}

func TestSceltaDelPacchetto(t *testing.T) {
	e := nuovoAmbiente(t)
	e.pacchetto("x") // fissa il nome del pacchetto di questo sistema
	// un secondo pacchetto dello stesso genere, e file che non c'entrano
	altro := strings.Replace(e.nomeFile, "Prova", "Altro", 1)
	e.extra = []string{altro, "SHA256SUMS.txt"}
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	e.app.controlla(context.Background(), e.voce(v.ID))
	v = e.vista(v.ID)
	if v.Installabile != "si" || !v.Scelta || daAggiornare(v) {
		t.Fatalf("due pacchetti adatti: va chiesto quale, e «Aggiorna tutti» aspetta. %+v", v)
	}
	s, err := e.app.Scelte(v.ID)
	if err != nil || len(s.Release) != 1 || len(s.Release[0].Pacchetti) != 2 || s.Release[0].Pacchetti[0].Consigliato || s.Release[0].Pacchetti[1].Consigliato {
		t.Fatalf("pacchetti fra cui scegliere: %+v %v", s, err)
	}
	if v = e.installa(v.ID, Opzioni{}); v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "va scelto quale") {
		t.Fatalf("senza scelta non si installa a caso: %+v", v.EsitoUpdate)
	}
	p := s.Release[0].Pacchetti[0]
	if p.Nome != e.nomeFile {
		p = s.Release[0].Pacchetti[1]
	}
	v = e.installa(v.ID, Opzioni{Asset: p.Nome, Tipo: p.Tipo})
	if !v.EsitoUpdate.OK || v.Installato.Asset != e.nomeFile || v.Installato.Tipo != p.Tipo || v.Scelta || !daAggiornare(v) {
		t.Fatalf("pacchetto scelto: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	// alla release dopo non si chiede più: si resta sul pacchetto di prima
	e.tag = "v1.1.0"
	e.app.controlla(context.Background(), e.voce(v.ID))
	if v = e.vista(v.ID); v.Scelta || v.UpdateAggiornato != "no" {
		t.Errorf("release nuova con gli stessi pacchetti: %+v", v)
	}
	if v = e.installa(v.ID, Opzioni{}); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.1.0" || v.Installato.Asset != e.nomeFile {
		t.Errorf("aggiornamento senza chiedere: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
}

func TestDisinstalla(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, Tag: "Prove"})
	if _, err := e.app.InfoRimozione(v.ID); err == nil {
		t.Error("non installato: niente da disinstallare")
	}
	v = e.aggiorna(v.ID, false)
	if !v.Installato.Presente {
		t.Fatalf("installazione: %+v", v.EsitoUpdate)
	}
	percorso := v.Installato.Percorso
	// le impostazioni che il programma si è creato usandolo (in modalità di
	// prova si cercano in una cartella della prova, mai fra quelle vere)
	dati := filepath.Join(e.base, "config", "prova-dati", "Prova")
	os.MkdirAll(dati, 0o755)
	os.WriteFile(filepath.Join(dati, "impostazioni.json"), []byte("{}"), 0o644)
	info, err := e.app.InfoRimozione(v.ID)
	if err != nil || !info.Installato.Presente || len(info.Dati) != 1 || info.Dati[0] != dati {
		t.Fatalf("prima di disinstallare: %+v %v", info, err)
	}
	// tenendo le impostazioni
	e.app.disinstalla(context.Background(), e.voce(v.ID), false)
	v = e.vista(v.ID)
	if v.Installato.Presente || !v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "Disinstallato") {
		t.Fatalf("disinstallazione: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	if _, err := os.Stat(percorso); err == nil {
		t.Errorf("il programma è ancora in %s", percorso)
	}
	if _, err := os.Stat(filepath.Join(dati, "impostazioni.json")); err != nil || len(e.cestinati) != 0 {
		t.Error("le impostazioni andavano tenute")
	}
	// non è più fra i programmi da tenere aggiornati, ma resta nella lista con le sue impostazioni locali
	if v.Aggiorna || daAggiornare(v) || v.Tag != "Prove" || v.Ambito != config.AmbitoUtente || len(e.app.Stato().Repo) != 1 {
		t.Errorf("dopo la disinstallazione: %+v", v)
	}
	if voci, _ := os.ReadDir(filepath.Join(e.base, "config", "installati")); len(voci) != 0 {
		t.Errorf("annotazioni rimaste: %v", voci)
	}
	e.app.disinstalla(context.Background(), e.voce(v.ID), false)
	if v = e.vista(v.ID); v.EsitoUpdate.OK {
		t.Error("disinstallare ciò che non c'è: deve dirlo")
	}
	// reinstallato, e tolto con le impostazioni: vanno nel cestino
	if v = e.aggiorna(v.ID, false); !v.Installato.Presente {
		t.Fatalf("reinstallazione: %+v", v.EsitoUpdate)
	}
	e.app.disinstalla(context.Background(), e.voce(v.ID), true)
	v = e.vista(v.ID)
	if v.Installato.Presente || !v.EsitoUpdate.OK || len(e.cestinati) != 1 || e.cestinati[0] != dati || !strings.Contains(v.EsitoUpdate.Messaggio, "cestino") {
		t.Errorf("con le impostazioni: esito %+v, cestinati %v", v.EsitoUpdate, e.cestinati)
	}
	// la configurazione di CentralHub non è mai fra le impostazioni da togliere
	e.app.basiDati = []string{e.base}
	if d := e.app.datiDi(voce{Nome: "config"}, installa.Stato{}); len(d) != 0 {
		t.Errorf("proposta la cartella di configurazione di CentralHub: %v", d)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestNomeEsportazione(t *testing.T) {
	d := time.Date(2026, 10, 5, 23, 59, 0, 0, time.Local)
	if got := nomeEsportazione(d, true); got != "CentralHub-configurazione-2026-10-05-con-chiavi.json" {
		t.Errorf("con le chiavi: %s", got)
	}
	if got := nomeEsportazione(d, false); got != "CentralHub-configurazione-2026-10-05-senza-chiavi.json" {
		t.Errorf("senza le chiavi: %s", got)
	}
}

// attendi aspetta che nessun repository stia più lavorando (i controlli
// partono in background).
func (e *ambiente) attendi() {
	e.t.Helper()
	for i := 0; i < 500; i++ {
		e.app.mu.Lock()
		n := len(e.app.lavori)
		e.app.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatal("i controlli non sono finiti")
}

// nuovaSessione simula la chiusura e la riapertura del programma: stessa
// configurazione su disco, memoria vuota.
func (e *ambiente) nuovaSessione() {
	e.app = NuovaApp()
	e.app.nuovoCli = e.cliente
	e.app.carica()
}

func TestControlloAllAperturaPerChiCostaSoloOgniOra(t *testing.T) {
	e := nuovoAmbiente(t) // senza sito: ogni controllo costa una richiesta all'API
	v := e.aggiungi(config.Locale{Aggiorna: true})

	// prima apertura: nulla ricordato, si controlla
	if !e.app.ControllaAllApertura() {
		t.Fatal("la prima apertura deve controllare")
	}
	e.attendi()
	if e.api != 1 {
		t.Fatalf("la prima apertura doveva costare una richiesta: %d", e.api)
	}

	// riapertura subito dopo, da una sessione nuova: niente richieste, ma i
	// LED si ricordano dall'ultima volta
	e.nuovaSessione()
	if e.app.ControllaAllApertura() {
		t.Fatal("meno di un'ora dopo non si deve controllare")
	}
	e.attendi()
	if e.api != 1 {
		t.Errorf("GitHub interrogato lo stesso: %d richieste", e.api)
	}
	if got := e.vista(v.ID); got.UltimaRelease != "v1.0.0" || got.UpdateAggiornato != "no" || got.VistaIl == "" {
		t.Errorf("l'ultima release non si è ricordata fra le sessioni: %+v", got)
	}

	// «Controlla» controlla sempre
	e.app.ControllaTutti()
	e.attendi()
	if e.api != 2 {
		t.Errorf("Controlla deve interrogare GitHub anche entro l'ora: %d", e.api)
	}

	// più di un'ora dopo l'ultimo controllo, si torna a controllare
	e.app.mu.Lock()
	e.app.visteIl[v.ID] = time.Now().Add(-61 * time.Minute)
	e.app.mu.Unlock()
	if !e.app.ControllaAllApertura() {
		t.Fatal("dopo più di un'ora si deve controllare")
	}
	e.attendi()
	if e.api != 3 {
		t.Errorf("dopo un'ora GitHub non è stato interrogato: %d", e.api)
	}

	// un orologio spostato indietro (istante nel futuro) non blocca i controlli
	e.app.mu.Lock()
	e.app.visteIl[v.ID] = time.Now().Add(3 * time.Hour)
	e.app.mu.Unlock()
	if !e.app.ControllaAllApertura() {
		t.Error("un istante nel futuro non deve bloccare il controllo")
	}
	e.attendi()
}

func TestSondaDelSitoNonConsumaLaQuota(t *testing.T) {
	e := nuovoAmbiente(t)
	e.web = true
	v := e.aggiungi(config.Locale{Aggiorna: true})

	e.app.ControllaTutti()
	e.attendi()
	if e.api != 1 || e.sonda != 1 {
		t.Fatalf("primo controllo: api %d sonda %d (serve l'API la prima volta)", e.api, e.sonda)
	}
	// stesso tag: lo dice il sito, l'API non si tocca
	for i := 0; i < 3; i++ {
		e.app.ControllaTutti()
		e.attendi()
	}
	if e.api != 1 || e.sonda != 4 {
		t.Errorf("a tag invariato non si deve usare l'API: api %d sonda %d", e.api, e.sonda)
	}
	// all'apertura i pubblici si controllano sempre, anche entro l'ora
	if !e.app.ControllaAllApertura() {
		t.Error("i repository pubblici si controllano a ogni apertura (non costa)")
	}
	e.attendi()
	if e.api != 1 {
		t.Errorf("l'apertura ha consumato quota: %d", e.api)
	}
	// esce una release nuova: la sonda se ne accorge e solo allora si chiede l'API
	e.tag = "v1.1.0"
	e.app.ControllaTutti()
	e.attendi()
	if got := e.vista(v.ID); got.UltimaRelease != "v1.1.0" || e.api != 2 {
		t.Errorf("release nuova: %+v api %d", got, e.api)
	}
	// l'elenco dei pacchetti vecchio di un giorno si richiede comunque
	e.app.mu.Lock()
	e.app.prese[v.ID] = time.Now().Add(-25 * time.Hour)
	e.app.mu.Unlock()
	e.app.ControllaTutti()
	e.attendi()
	if e.api != 3 {
		t.Errorf("elenco vecchio non rinnovato: api %d", e.api)
	}
	// se il sito non risponde si ripiega sull'API
	e.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	e.app.ControllaTutti()
	e.attendi()
}

func TestSommeDiControllo(t *testing.T) {
	prova := func(somme string) (Vista, *ambiente) {
		e := nuovoAmbiente(t)
		e.somme = somme
		v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
		return e.aggiorna(v.ID, false), e
	}
	for _, c := range []string{"digest", "file"} {
		if v, _ := prova(c); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" {
			t.Errorf("con le somme %q deve installare: %+v", c, v.EsitoUpdate)
		}
	}
	v, _ := prova("nessuna")
	if v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "somma di controllo") || v.Installato.Presente {
		t.Errorf("senza nessuna somma non si installa: %+v", v.EsitoUpdate)
	}
	v, _ = prova("sbagliata")
	if v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "non corrisponde") || v.Installato.Presente {
		t.Errorf("una somma che non torna non si installa: %+v", v.EsitoUpdate)
	}
}

func TestArchiviazioneVerificata(t *testing.T) {
	e := nuovoAmbiente(t)
	master := filepath.Join(e.base, "master")
	v := e.aggiungi(config.Locale{CartellaArchivio: master})
	if v = e.archivia(v.ID); !v.EsitoArchivio.OK || !strings.Contains(v.EsitoArchivio.Messaggio, "1 pacchetti verificati") ||
		!strings.Contains(v.EsitoArchivio.Messaggio, "1 senza somma") {
		t.Errorf("l'archiviazione deve dire cosa ha verificato (il pacchetto sì, note.txt non ha somma): %+v", v.EsitoArchivio)
	}
	// una somma che non torna ferma l'archiviazione
	e2 := nuovoAmbiente(t)
	e2.somme = "sbagliata"
	master2 := filepath.Join(e2.base, "master")
	v2 := e2.aggiungi(config.Locale{CartellaArchivio: master2})
	if v2 = e2.archivia(v2.ID); v2.EsitoArchivio.OK || !strings.Contains(v2.EsitoArchivio.Messaggio, "non corrisponde") {
		t.Errorf("un file che non torna non va archiviato: %+v", v2.EsitoArchivio)
	}
	if scarica := filepath.Join(master2, "v1.0.0"); func() bool { _, err := os.Stat(scarica); return err == nil }() {
		t.Error("è comparsa la cartella della release con un file che non torna")
	}
}

func TestInstallaDallArchivio(t *testing.T) {
	e := nuovoAmbiente(t)
	master := filepath.Join(e.base, "master")
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: master})
	if s, err := e.app.Scelte(v.ID); err != nil || len(s.Archivio) != 0 {
		t.Fatalf("senza archiviazione non c'è nulla da cui installare: %+v %v", s, err)
	}
	e.archivia(v.ID)
	s, err := e.app.Scelte(v.ID)
	if err != nil || len(s.Archivio) != 1 || s.Archivio[0].Tag != "v1.0.0" || len(s.Archivio[0].Pacchetti) != 1 || s.Archivio[0].Pacchetti[0].Nome != e.nomeFile {
		t.Fatalf("l'archivio deve proporre la release archiviata: %+v %v", s.Archivio, err)
	}
	// senza rete: il server non c'è più
	e.srv.Close()
	// la finestra «Installa» deve poter offrire l'archivio: l'elenco di GitHub è vuoto ma
	// non nil (nel JSON sarebbe null e l'interfaccia lo scorrerebbe: trovato sul sistema vero).
	// Il controllo appena fatto si riuserebbe (scorciatoia): lo si invecchia, come dopo mezzo minuto.
	e.app.mu.Lock()
	e.app.visteIl[v.ID] = time.Now().Add(-time.Hour)
	e.app.mu.Unlock()
	s, err = e.app.Scelte(v.ID)
	if err != nil || s.Release == nil || len(s.Release) != 0 || len(s.Archivio) != 1 || s.Avviso == "" {
		t.Fatalf("senza GitHub Scelte deve dare Release vuoto (non nil), l'archivio e un avviso: %+v %v", s, err)
	}
	if j, _ := json.Marshal(s); !strings.Contains(string(j), `"release":[]`) {
		t.Errorf("il JSON per l'interfaccia deve avere release:[]: %s", j)
	}
	v = e.installa(v.ID, Opzioni{Da: "archivio", Tag: "v1.0.0"})
	if !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" || !v.Installato.Nostro {
		t.Fatalf("installazione dall'archivio: %+v esito %+v", v.Installato, v.EsitoUpdate)
	}
	// l'archivio non si tocca
	if _, err := os.Stat(filepath.Join(master, "v1.0.0", e.nomeFile)); err != nil {
		t.Error("il pacchetto è sparito dall'archivio")
	}
}

// Dopo un lavoro fallito la scheda mostra l'errore del lavoro, non quello
// vecchio di un controllo.
func TestErroreDelLavoroPrevaleSuQuelloDelControllo(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{CartellaArchivio: filepath.Join(e.base, "master")})
	e.app.mu.Lock()
	e.app.errori[v.ID] = "GitHub non è raggiungibile"
	e.app.mu.Unlock()
	e.app.fallito(context.Background(), e.voce(v.ID), modoUpdate, "v1.0.0", errors.New("il file scaricato non corrisponde a quello pubblicato"))
	if got := e.vista(v.ID); got.Errore != "" || got.EsitoUpdate == nil || !strings.Contains(got.EsitoUpdate.Messaggio, "non corrisponde") {
		t.Errorf("errore vecchio ancora in primo piano: errore %q esito %+v", got.Errore, got.EsitoUpdate)
	}
}

func TestInstallaDallArchivioControllaLaSomma(t *testing.T) {
	e := nuovoAmbiente(t)
	master := filepath.Join(e.base, "master")
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: master})
	e.archivia(v.ID)
	// il file dell'archivio viene alterato
	if err := os.WriteFile(filepath.Join(master, "v1.0.0", e.nomeFile), []byte("alterato"), 0o644); err != nil {
		t.Fatal(err)
	}
	v = e.installa(v.ID, Opzioni{Da: "archivio", Tag: "v1.0.0"})
	if v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "non corrisponde") || v.Installato.Presente {
		t.Errorf("un file alterato nell'archivio non si installa: %+v", v.EsitoUpdate)
	}
	// una release che non c'è nell'archivio, o un nome con percorso
	for _, tag := range []string{"v9.9.9", "../fuori"} {
		if v = e.installa(v.ID, Opzioni{Da: "archivio", Tag: tag}); v.EsitoUpdate.OK {
			t.Errorf("%q non è nell'archivio", tag)
		}
	}
}

func TestIndirizzoRepoENovita(t *testing.T) {
	if u, err := indirizzoRepo("storytold/photocraft"); err != nil || u != "https://github.com/storytold/photocraft" {
		t.Errorf("indirizzo: %q %v", u, err)
	}
	for _, male := range []string{"", "a b/c", "javascript:alert(1)"} {
		if u, err := indirizzoRepo(male); err == nil {
			t.Errorf("%q non è un repository: %q", male, u)
		}
	}
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{})
	if _, err := e.app.Novita(v.ID); err == nil {
		t.Error("prima del controllo non ci sono note")
	}
	e.app.controlla(context.Background(), e.voce(v.ID))
	if n, err := e.app.Novita(v.ID); err != nil || n.Tag != "v1.0.0" || n.Testo != "Novità della v1.0.0" {
		t.Errorf("note: %+v %v", n, err)
	}
	if got := e.vista(v.ID); !got.HaNote {
		t.Error("la scheda deve dire che ci sono delle note")
	}
}

// prova installa la prima release con le impostazioni date. proprietarioFirma
// rende «a» (il proprietario dei repository finti) uno di quelli che firmano.
func provaFirma(t *testing.T, firma string, ctl func(e *ambiente), cfg func(r *config.Repo)) Vista {
	t.Helper()
	e := nuovoAmbiente(t)
	e.firma = firma
	if ctl != nil {
		ctl(e)
	}
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	if cfg != nil {
		r := config.Repo{ID: v.ID, Nome: "Prova", Repo: "a/Prova", Locale: config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente}}
		cfg(&r)
		if _, err := e.app.SalvaRepo(r); err != nil {
			t.Fatal(err)
		}
	}
	return e.aggiorna(v.ID, false)
}

func TestPretendiFirma(t *testing.T) {
	pretendi := func(r *config.Repo) { r.PretendiFirma = true }
	if v := provaFirma(t, "valida", nil, pretendi); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" || !v.PretendiFirma {
		t.Errorf("release firmata: %+v", v.EsitoUpdate)
	}
	for firma, dentro := range map[string]string{
		"":         "non è firmata",
		"altra":    "nessuna chiave fidata",
		"alterata": "nessuna chiave fidata",
		"riusata":  "nessuna chiave fidata",
		"estranea": "non è nel formato di CentralHub",
	} {
		v := provaFirma(t, firma, nil, pretendi)
		if v.EsitoUpdate.OK || v.Installato.Presente || !strings.Contains(v.EsitoUpdate.Messaggio, dentro) {
			t.Errorf("firma %q: deve rifiutare con %q: %+v installato %v", firma, dentro, v.EsitoUpdate, v.Installato.Presente)
		}
	}
}

// La verifica è sempre attiva: una firma presente ma sbagliata fa rifiutare la
// release anche se il repository non ha l'interruttore e non è di chi firma.
func TestFirmaSbagliataSiRifiutaSempre(t *testing.T) {
	for _, firma := range []string{"altra", "alterata", "riusata"} {
		v := provaFirma(t, firma, nil, nil)
		if v.EsitoUpdate.OK || v.Installato.Presente || !strings.Contains(v.EsitoUpdate.Messaggio, "nessuna chiave fidata") {
			t.Errorf("firma %q senza interruttore: %+v", firma, v.EsitoUpdate)
		}
	}
	// una firma in un altro formato non è nostra: si ignora e si usa il digest
	if v := provaFirma(t, "estranea", nil, nil); !v.EsitoUpdate.OK {
		t.Errorf("la firma di un altro programma non deve bloccare: %+v", v.EsitoUpdate)
	}
	// repository di terzi, senza firma: come prima
	if v := provaFirma(t, "", nil, nil); !v.EsitoUpdate.OK || v.FirmaRichiesta {
		t.Errorf("senza firma e senza obbligo: %+v richiesta=%v", v.EsitoUpdate, v.FirmaRichiesta)
	}
}

// Togliere la firma non basta a farla ignorare.
func TestFirmaTolta(t *testing.T) {
	// 1. proprietario che firma: dopo la data, senza firma si rifiuta
	nostro := func(e *ambiente) {
		vecchi := proprietariFirmati
		proprietariFirmati = []string{"a"}
		t.Cleanup(func() { proprietariFirmati = vecchi })
		e.pubblicata = firmaObbligatoriaDal.Add(time.Hour).Format(time.RFC3339)
	}
	v := provaFirma(t, "", nostro, nil)
	if v.EsitoUpdate.OK || v.Installato.Presente || !strings.Contains(v.EsitoUpdate.Messaggio, "obbligatoria") || !v.FirmaRichiesta {
		t.Errorf("proprietario che firma, release nuova senza firma: %+v", v.EsitoUpdate)
	}
	// una release di prima della firma si installa ancora
	vecchia := func(e *ambiente) {
		nostro(e)
		e.pubblicata = firmaObbligatoriaDal.Add(-24 * time.Hour).Format(time.RFC3339)
	}
	if v := provaFirma(t, "", vecchia, nil); !v.EsitoUpdate.OK {
		t.Errorf("release di prima della firma: %+v", v.EsitoUpdate)
	}
	// 2. a trinquete: visto un repository firmare, da lì non scende più
	e := nuovoAmbiente(t)
	e.firma = "valida"
	id := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente}).ID
	if v := e.aggiorna(id, false); !v.EsitoUpdate.OK || !v.PretendiFirma {
		t.Fatalf("release firmata: %+v pretendi=%v", v.EsitoUpdate, v.PretendiFirma)
	}
	e.nuovaSessione() // l'interruttore è nella configurazione, sopravvive
	if !e.vista(id).PretendiFirma {
		t.Fatal("l'interruttore doveva restare acceso dopo il riavvio")
	}
	e.firma = ""
	e.tag = "v1.1.0" // una release nuova, senza più la firma
	e.app.controlla(context.Background(), e.voce(id))
	if v := e.aggiorna(id, false); v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" || !strings.Contains(v.EsitoUpdate.Messaggio, "non è firmata") {
		t.Errorf("firma tolta dopo averla vista: %+v installato %s", v.EsitoUpdate, v.Installato.Tag)
	}
}

// Una release più vecchia presentata come ultima non fa scendere di versione.
func TestNonSiScendeDiVersione(t *testing.T) {
	e := nuovoAmbiente(t)
	e.tag = "v1.1.0"
	e.altre = []string{"v1.0.0"}
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	if v = e.aggiorna(v.ID, false); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.1.0" {
		t.Fatalf("prima installazione: %+v", v.EsitoUpdate)
	}
	e.tag = "v1.0.0" // GitHub «ultima» ora è più vecchia: ripubblicata a mano o attacco
	e.app.controlla(context.Background(), e.voce(v.ID))
	if v = e.aggiorna(v.ID, false); v.EsitoUpdate.OK || v.Installato.Tag != "v1.1.0" || !strings.Contains(v.EsitoUpdate.Messaggio, "più vecchia") {
		t.Errorf("discesa di versione di nascosto: %+v installato %s", v.EsitoUpdate, v.Installato.Tag)
	}
	// scelta dall'utente, si può
	if v = e.installa(v.ID, Opzioni{Tag: "v1.0.0"}); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" {
		t.Errorf("scelta esplicita di una versione più vecchia: %+v", v.EsitoUpdate)
	}
}

func TestPretendiFirmaFirmaSenzaSomme(t *testing.T) {
	e := nuovoAmbiente(t)
	e.firma = "valida"
	e.app.nuovoCli = func(token string) *github.Client { return e.cliente(token) }
	chiaviFidate = func() []ed25519.PublicKey { return nil } // nessuna chiave incorporata
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	e.app.SalvaRepo(config.Repo{ID: v.ID, Nome: "Prova", Repo: "a/Prova", PretendiFirma: true, Locale: config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente}})
	if v = e.aggiorna(v.ID, false); v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "nessuna chiave pubblica incorporata") {
		t.Errorf("senza chiavi fidate non si può verificare: %+v", v.EsitoUpdate)
	}
}

func TestPretendiFirmaArchivioEDaArchivio(t *testing.T) {
	e := nuovoAmbiente(t)
	e.firma = "valida"
	master := filepath.Join(e.base, "master")
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: master})
	e.app.SalvaRepo(config.Repo{ID: v.ID, Nome: "Prova", Repo: "a/Prova", PretendiFirma: true,
		Locale: config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente, CartellaArchivio: master}})
	if v = e.archivia(v.ID); !v.EsitoArchivio.OK {
		t.Fatalf("archiviazione firmata: %+v", v.EsitoArchivio)
	}
	for _, f := range []string{"SHA256SUMS.txt", "SHA256SUMS.txt.sig"} {
		if _, err := os.Stat(filepath.Join(master, "v1.0.0", f)); err != nil {
			t.Errorf("manca %s nell'archivio", f)
		}
	}
	// senza rete: la firma si controlla sui file archiviati
	e.srv.Close()
	if v = e.installa(v.ID, Opzioni{Da: "archivio", Tag: "v1.0.0"}); !v.EsitoUpdate.OK || v.Installato.Tag != "v1.0.0" {
		t.Fatalf("installazione firmata dall'archivio: %+v", v.EsitoUpdate)
	}
	// somme alterate nell'archivio: la firma non torna più
	if err := e.app.Disinstalla(v.ID, false); err != nil {
		t.Fatal(err)
	}
	e.attendi()
	if err := os.WriteFile(filepath.Join(master, "v1.0.0", "SHA256SUMS.txt"), []byte(strings.Repeat("0", 64)+"  "+e.nomeFile+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v = e.installa(v.ID, Opzioni{Da: "archivio", Tag: "v1.0.0"})
	if v.EsitoUpdate.OK || !strings.Contains(v.EsitoUpdate.Messaggio, "nessuna chiave fidata") {
		t.Errorf("archivio con somme alterate: %+v", v.EsitoUpdate)
	}
}

func TestIntegritaDopoInstallazione(t *testing.T) {
	e := nuovoAmbiente(t)
	v := e.aggiungi(config.Locale{Aggiorna: true, Ambito: config.AmbitoUtente})
	v = e.aggiorna(v.ID, false)
	if !v.EsitoUpdate.OK || v.Integrita != "ok" {
		t.Fatalf("appena installato deve risultare integro: %+v %q", v.EsitoUpdate, v.Integrita)
	}
	// nuova sessione: all'apertura si ricontrolla
	e.nuovaSessione()
	if got := e.vista(v.ID); got.Integrita != "" {
		t.Errorf("prima della verifica non si sa: %q", got.Integrita)
	}
	e.app.VerificaIntegrita()
	attendiIntegrita(t, e, v.ID, "ok")
	// il file installato viene alterato
	if err := os.WriteFile(v.Installato.Percorso, []byte("alterato"), 0o755); err != nil {
		// su macOS e Windows il programma è una cartella
		var primo string
		filepath.WalkDir(v.Installato.Percorso, func(p string, d os.DirEntry, _ error) error {
			if primo == "" && d != nil && d.Type().IsRegular() {
				primo = p
			}
			return nil
		})
		if primo == "" {
			t.Fatal("niente da alterare in", v.Installato.Percorso)
		}
		os.WriteFile(primo, []byte("alterato"), 0o755)
	}
	e.nuovaSessione()
	e.app.VerificaIntegrita()
	attendiIntegrita(t, e, v.ID, "diversa")
	if got := e.vista(v.ID); len(got.IntegritaFile) == 0 {
		t.Error("deve dire quale file è diverso")
	}
	// disinstallato: il manifesto sparisce
	if err := e.app.Disinstalla(v.ID, false); err != nil {
		t.Fatal(err)
	}
	e.attendi()
	e.nuovaSessione()
	e.app.VerificaIntegrita()
	time.Sleep(100 * time.Millisecond)
	if got := e.vista(v.ID); got.Integrita != "" {
		t.Errorf("dopo la disinstallazione: %q", got.Integrita)
	}
}

func attendiIntegrita(t *testing.T, e *ambiente, id, atteso string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if e.vista(id).Integrita == atteso {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("integrità attesa %q, vista %q", atteso, e.vista(id).Integrita)
}
