// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func assoluta(nome string) string {
	if runtime.GOOS == "windows" {
		return `C:\prove\` + nome
	}
	return "/prove/" + nome
}

func TestPulisciRepo(t *testing.T) {
	buoni := map[string]string{
		"TrismegistoOpenSource/RsyncGUI":                        "TrismegistoOpenSource/RsyncGUI",
		" https://github.com/TrismegistoOpenSource/RsyncGUI ":   "TrismegistoOpenSource/RsyncGUI",
		"https://github.com/TrismegistoOpenSource/RsyncGUI.git": "TrismegistoOpenSource/RsyncGUI",
		"github.com/a/b/": "a/b",
		"https://github.com/a/b/releases/tag/v1.0.0":               "a/b",
		"git@github.com:a/b.git":                                   "a/b",
		"https://www.github.com/utente-1/hyprland-cachyos-install": "utente-1/hyprland-cachyos-install",
		"a/nome.con.punti_e-trattini":                              "a/nome.con.punti_e-trattini",
	}
	for in, atteso := range buoni {
		got, err := PulisciRepo(in)
		if err != nil || got != atteso {
			t.Errorf("PulisciRepo(%q) = %q, %v; atteso %q", in, got, err, atteso)
		}
	}
	for _, in := range []string{"", "solo-nome", "a/", "/b", "a b/c", "a/b c", "-a/b", "a/..", "a/b?x=1", "../../etc"} {
		if got, err := PulisciRepo(in); err == nil {
			t.Errorf("PulisciRepo(%q) = %q: doveva essere rifiutato", in, got)
		}
	}
}

func TestSalvaCaricaConCopia(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(VariabileCartella, dir)

	c, err := Carica()
	if err != nil || len(c.Repo) != 0 {
		t.Fatalf("primo avvio: %+v, %v", c, err)
	}
	c.Repo = append(c.Repo, Repo{ID: "uno", Nome: "PhotoCraft", Repo: "storytold/photocraft", Locale: Locale{Tag: "Storytold", CartellaArchivio: assoluta("photo")}})
	if err := Salva(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json.bak")); err == nil {
		t.Error("alla prima scrittura non c'è niente di cui fare la copia")
	}
	c.Repo[0].Nome = "Cambiato"
	if err := Salva(c); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(filepath.Join(dir, "config.json.bak"))
	if err != nil || !strings.Contains(string(bak), `"PhotoCraft"`) || strings.Contains(string(bak), "Cambiato") {
		t.Errorf("la copia .bak deve contenere la versione precedente: %s, %v", bak, err)
	}
	di, err := Carica()
	if err != nil || len(di.Repo) != 1 || di.Repo[0].Nome != "Cambiato" || di.Repo[0].Tag != "Storytold" {
		t.Errorf("riletto: %+v, %v", di, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json.tmp")); err == nil {
		t.Error("è rimasto il file provvisorio")
	}
}

func TestChiaviEsportazione(t *testing.T) {
	t.Setenv(VariabileCartella, t.TempDir())
	c := Config{Repo: []Repo{
		{ID: "x", Nome: "PhotoCraft", Repo: "https://github.com/storytold/photocraft", Asset: "*-Windows.zip",
			Locale: Locale{Tag: "Storytold", CartellaArchivio: assoluta("photo"), Aggiorna: true, Ambito: AmbitoSistema}},
		{ID: "y", Nome: "Privato", Repo: "me/privato", Token: "github_pat_segreto_uno"},
		{ID: "z", Nome: "Altro", Repo: "me/altro", Privato: true},
	}}
	if c.Chiavi() != 1 {
		t.Errorf("chiavi: %d", c.Chiavi())
	}
	dir := t.TempDir()

	// senza chiavi: tutto il resto c'è, la chiave no, e si sa che serve
	senza := filepath.Join(dir, "senza.json")
	if err := Esporta(senza, c, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(senza)
	if strings.Contains(string(b), "github_pat_") || strings.Contains(string(b), `"token"`) {
		t.Errorf("esportazione senza chiavi con una chiave dentro: %s", b)
	}
	di, err := Importa(senza)
	if err != nil {
		t.Fatal(err)
	}
	p := di.Repo[0]
	if p.Repo != "storytold/photocraft" || !p.Aggiorna || p.Asset != "*-Windows.zip" || p.Tag != "Storytold" || p.Ambito != AmbitoSistema || p.CartellaArchivio != assoluta("photo") {
		t.Errorf("importata: %+v", di)
	}
	if !di.Repo[1].Privato || di.Repo[1].Token != "" || !di.Repo[2].Privato {
		t.Errorf("i repository privati restano privati, senza chiave: %+v", di.Repo[1:])
	}
	if c.Repo[1].Token == "" {
		t.Error("esportare senza chiavi ha tolto la chiave dalla configurazione in uso")
	}

	// con le chiavi: tornano identiche
	con := filepath.Join(dir, "con.json")
	if err := Esporta(con, c, true); err != nil {
		t.Fatal(err)
	}
	di, err = Importa(con)
	if err != nil || di.Repo[1].Token != "github_pat_segreto_uno" || !di.Repo[1].Privato || di.Chiavi() != 1 {
		t.Errorf("con le chiavi: %+v, %v", di.Repo, err)
	}
	if fi, err := os.Stat(con); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("il file con le chiavi dev'essere leggibile solo da chi lo crea: %v %v", fi, err)
	}
}

func TestChiaveNonValida(t *testing.T) {
	for _, tok := range []string{"due parole", "a\nb", strings.Repeat("x", 600)} {
		r := Repo{Repo: "a/b", Token: tok}
		err := r.Normalizza()
		if err == nil {
			t.Errorf("chiave %q accettata", tok)
		} else if strings.Contains(err.Error(), tok) {
			t.Error("l'errore non deve contenere la chiave")
		}
	}
	r := Repo{Repo: "a/b", Token: "  github_pat_ok \n"}
	if err := r.Normalizza(); err != nil || r.Token != "github_pat_ok" || !r.Privato {
		t.Errorf("una chiave si ripulisce e rende privato il repository: %+v %v", r, err)
	}
}

// I file della 0.1.0 avevano i repository dell'autore nel programma e qui
// solo le loro impostazioni: diventano repository normali, senza chiave.
func TestMigraDaIncorporati(t *testing.T) {
	cartella := assoluta("Archivio") + string(filepath.Separator) + "AuroraStudio"
	b := []byte(`{"versione":1,"repo":[{"id":"a1","nome":"PhotoCraft","repo":"storytold/photocraft"}],
	  "incorporati":{
	    "me/aurorastudio":{"tag":"Mie","cartellaArchivio":` + jsonStr(cartella) + `,"ambito":"utente"},
	    "me/nuova":{"aggiorna":true},
	    "storytold/photocraft":{"tag":"ignorato"},
	    "non valido":{}}}`)
	c, err := Decodifica(b)
	if err != nil {
		t.Fatal(err)
	}
	if c.Incorporati != nil || len(c.Repo) != 3 {
		t.Fatalf("migrazione: %+v", c)
	}
	a := c.Repo[1]
	if a.Nome != "AuroraStudio" || a.Repo != "me/aurorastudio" || !a.Privato || a.Token != "" || a.Tag != "Mie" || a.Ambito != AmbitoUtente || a.CartellaArchivio != cartella || a.ID != IDDaRepo("ME/AuroraStudio") {
		t.Errorf("repository migrato: %+v", a)
	}
	n := c.Repo[2]
	if n.Nome != "nuova" || !n.Aggiorna {
		t.Errorf("senza cartella il nome è quello del repository: %+v", n)
	}
	if c.Repo[0].Tag != "" {
		t.Errorf("un repository già in lista non si tocca: %+v", c.Repo[0])
	}
	// salvato, non porta più la vecchia sezione
	t.Setenv(VariabileCartella, t.TempDir())
	if err := Salva(c); err != nil {
		t.Fatal(err)
	}
	f, _ := PercorsoFile()
	if raw, _ := os.ReadFile(f); strings.Contains(string(raw), "incorporati") {
		t.Errorf("la vecchia sezione non va riscritta: %s", raw)
	}
}

func TestPercorsoDiUnAltroSistema(t *testing.T) {
	// un file esportato su un altro sistema non deve far fallire l'importazione
	altro := `/Volumes/Esterno/GitHub`
	if runtime.GOOS == "windows" {
		altro = `/Volumes/Esterno/GitHub`
	} else if !strings.HasPrefix(assoluta("x"), "/") {
		altro = `C:\Esterno\GitHub`
	}
	c, err := Decodifica([]byte(`{"repo":[{"nome":"x","repo":"a/b","cartellaArchivio":` + jsonStr(altro) + `}]}`))
	if runtime.GOOS == "windows" {
		if err != nil || c.Repo[0].CartellaArchivio != "" {
			t.Errorf("percorso di un altro sistema: %+v %v", c, err)
		}
	} else if err != nil {
		t.Errorf("percorso di un altro sistema: %v", err)
	}
	if _, err := Decodifica([]byte(`{"repo":[{"nome":"x","repo":"a/b","cartellaArchivio":"relativa/qui"}]}`)); err == nil {
		t.Error("un percorso relativo non è di nessun sistema: va rifiutato")
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDecodificaRifiutaERipara(t *testing.T) {
	if _, err := Decodifica([]byte("non è json")); err == nil {
		t.Error("un file qualsiasi non è una configurazione")
	}
	if _, err := Decodifica([]byte(`{"repo":[{"nome":"x","repo":"senza-barra"}]}`)); err == nil {
		t.Error("repo non valido: va rifiutato")
	}
	if _, err := Decodifica([]byte(`{"repo":[{"nome":"x","repo":"a/b","cartellaArchivio":"relativa"}]}`)); err == nil {
		t.Error("cartella relativa: va rifiutata")
	}
	if _, err := Decodifica([]byte(`{"repo":[{"repo":"a/b","ambito":"ovunque"}]}`)); err == nil {
		t.Error("ambito sconosciuto: va rifiutato")
	}
	// ID mancanti o doppi vengono rigenerati; nome vuoto = nome del
	// repository; la cartella d'archivio è facoltativa
	c, err := Decodifica([]byte(`{"repo":[{"id":"k","repo":"a/b"},{"id":"k","repo":"a/c"},{"repo":"a/d"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo[0].ID == c.Repo[1].ID || c.Repo[1].ID == "" || c.Repo[2].ID == "" {
		t.Errorf("ID non riparati: %+v", c.Repo)
	}
	if c.Repo[0].Nome != "b" || c.Repo[0].CartellaArchivio != "" {
		t.Errorf("valori predefiniti: %+v", c.Repo[0])
	}
}

func TestEsiti(t *testing.T) {
	t.Setenv(VariabileCartella, t.TempDir())
	if m := CaricaEsiti(); len(m) != 0 {
		t.Fatalf("esiti iniziali: %v", m)
	}
	if err := SalvaEsiti(map[string]Esito{"a|update": {OK: true, Messaggio: "installato", Tag: "v1", Quando: "2026-10-13T10:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if m := CaricaEsiti(); !m["a|update"].OK || m["a|update"].Tag != "v1" {
		t.Errorf("riletti: %v", m)
	}
}
