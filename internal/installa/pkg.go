// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// I pacchetti .pkg di macOS: li apre l'Installer del sistema, che mostra la
// sua procedura e chiede la password; dove finiscono i file lo decide il
// pacchetto. CentralHub legge dal pacchetto gli identificativi che lascerà
// fra le ricevute del sistema (pkgutil --pkgs), li confronta prima e dopo
// per sapere se l'installazione è avvenuta davvero, e li annota. Per
// disinstallare toglie i file elencati nelle ricevute, con la password
// dell'amministratore, e poi chiede a pkgutil di dimenticare il pacchetto.

const installerMac = "/System/Library/CoreServices/Installer.app"

// PkgContenuto è ciò che un pacchetto ha messo sul disco, secondo la sua
// ricevuta.
type PkgContenuto struct {
	ID string
	// Volume e Radice: il volume ("/" per il disco di avvio) e la cartella,
	// assoluta, rispetto a cui sono scritti i percorsi.
	Volume string
	Radice string
	// File e Cartelle, relativi alla Radice.
	File     []string
	Cartelle []string
}

// Ciò che parla con il sistema: sostituibile nelle prove.
var (
	// pkgInfo legge dal file .pkg gli identificativi dei pacchetti che installa.
	pkgInfo = leggiPkg
	// pkgStato dice se c'è la ricevuta di un pacchetto, e di quale versione
	// e di quando.
	pkgStato = ricevutaPkg
	// pkgContenuto legge dalla ricevuta cosa il pacchetto ha installato.
	pkgContenuto = contenutoPkg
	// avviaPkg apre il pacchetto con l'Installer e aspetta che si chiuda.
	avviaPkg = func(ctx context.Context, file string) error {
		out, err := exec.CommandContext(ctx, "/usr/bin/open", "-W", "-n", "-a", installerMac, file).CombinedOutput()
		if err != nil {
			return fmt.Errorf("l'Installer non si è avviato: %v %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

// reIDPkg: identificativo di un pacchetto (stile "com.azienda.programma").
var reIDPkg = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// pkgInstallato dice se tutte le ricevute annotate (separate da virgola)
// risultano ancora presenti.
func pkgInstallato(chiave string) bool {
	ids := strings.Split(chiave, ",")
	for _, id := range ids {
		if _, _, ok := pkgStato(id); !ok {
			return false
		}
	}
	return chiave != ""
}

// leggiPkg tira fuori da un .pkg (un archivio xar) i PackageInfo e ne legge
// gli identificativi: senza installare né espandere i file del pacchetto.
func leggiPkg(file string) ([]string, error) {
	ctx, annulla := context.WithTimeout(context.Background(), time.Minute)
	defer annulla()
	elenco, err := exec.CommandContext(ctx, "/usr/bin/xar", "-tf", file).Output()
	if err != nil {
		return nil, fmt.Errorf("%s non è un pacchetto .pkg leggibile: %w", filepath.Base(file), err)
	}
	tmp, err := os.MkdirTemp("", "centralhub-pkg-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	var ids []string
	visti := map[string]bool{}
	for _, riga := range strings.Split(string(elenco), "\n") {
		riga = strings.TrimSpace(riga)
		if riga != "PackageInfo" && !strings.HasSuffix(riga, ".pkg/PackageInfo") {
			continue
		}
		if out, err := exec.CommandContext(ctx, "/usr/bin/xar", "-x", "-f", file, "-C", tmp, riga).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("%s: %v %s", filepath.Base(file), err, strings.TrimSpace(string(out)))
		}
		b, err := os.ReadFile(filepath.Join(tmp, filepath.FromSlash(riga)))
		if err != nil {
			return nil, err
		}
		var info struct {
			ID string `xml:"identifier,attr"`
		}
		if err := xml.Unmarshal(b, &info); err != nil || !reIDPkg.MatchString(info.ID) {
			return nil, fmt.Errorf("%s: PackageInfo non leggibile in %s", filepath.Base(file), riga)
		}
		if !visti[info.ID] {
			visti[info.ID] = true
			ids = append(ids, info.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s: non trovo cosa installa (nessun PackageInfo): CentralHub non saprebbe seguirlo né toglierlo", filepath.Base(file))
	}
	return ids, nil
}

// infoRicevuta legge i campi di `pkgutil --pkg-info`.
func infoRicevuta(id string) (map[string]string, bool) {
	if !reIDPkg.MatchString(id) {
		return nil, false
	}
	ctx, annulla := context.WithTimeout(context.Background(), 20*time.Second)
	defer annulla()
	out, err := exec.CommandContext(ctx, "/usr/sbin/pkgutil", "--pkg-info", id).Output()
	if err != nil {
		return nil, false
	}
	campi := map[string]string{}
	for _, riga := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(riga, ": "); ok {
			campi[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return campi, true
}

func ricevutaPkg(id string) (versione, quando string, ok bool) {
	c, ok := infoRicevuta(id)
	if !ok {
		return "", "", false
	}
	return c["version"], c["install-time"], true
}

func contenutoPkg(id string) (PkgContenuto, error) {
	c, ok := infoRicevuta(id)
	if !ok {
		return PkgContenuto{}, fmt.Errorf("il sistema non ha la ricevuta del pacchetto %s", id)
	}
	volume := c["volume"]
	if volume == "" {
		volume = "/"
	}
	res := PkgContenuto{ID: id, Volume: volume, Radice: filepath.Join(volume, c["location"])}
	ctx, annulla := context.WithTimeout(context.Background(), 2*time.Minute)
	defer annulla()
	elenca := func(solo string) ([]string, error) {
		out, err := exec.CommandContext(ctx, "/usr/sbin/pkgutil", solo, "--files", id).Output()
		if err != nil {
			return nil, fmt.Errorf("elenco dei file di %s non letto: %w", id, err)
		}
		var v []string
		for _, riga := range strings.Split(string(out), "\n") {
			if riga = strings.TrimRight(riga, "\r"); riga != "" {
				v = append(v, riga)
			}
		}
		return v, nil
	}
	var err error
	if res.File, err = elenca("--only-files"); err != nil {
		return PkgContenuto{}, err
	}
	if res.Cartelle, err = elenca("--only-dirs"); err != nil {
		return PkgContenuto{}, err
	}
	return res, nil
}

// appDi trova le app che i pacchetti hanno messo sul disco (le cartelle .app
// che non stanno dentro un'altra .app).
func appDi(contenuti []PkgContenuto) []string {
	var app []string
	for _, c := range contenuti {
		for _, d := range c.Cartelle {
			if strings.HasSuffix(d, ".app") && !strings.Contains(strings.TrimSuffix(d, ".app"), ".app/") {
				app = append(app, filepath.Join(c.Radice, d))
			}
		}
	}
	sort.Strings(app)
	return app
}

func installaPkg(r Richiesta) (Esito, error) {
	if r.GOOS != "darwin" {
		return Esito{}, fmt.Errorf("i pacchetti .pkg non si installano su %s", r.GOOS)
	}
	asset := filepath.Base(r.File)
	ids, err := pkgInfo(r.File)
	if err != nil {
		return Esito{}, err
	}
	gia, _ := leggiRegistro(r.CartellaRegistro, r.Nome)
	if gia.Tipo != TipoPkg {
		gia = Registro{}
	}
	if gia.Percorso != "" && esiste(gia.Percorso) && inEsecuzione(percorsoProcessi(gia.Percorso, r.GOOS), r.GOOS) {
		return Esito{}, &ErrInEsecuzione{r.Nome}
	}
	prima := map[string]string{}
	for _, id := range ids {
		if v, q, ok := pkgStato(id); ok {
			prima[id] = v + "|" + q
		}
	}
	if err := avviaPkg(r.Ctx, r.File); err != nil {
		return Esito{}, err
	}
	if r.Ctx.Err() != nil {
		return Esito{}, r.Ctx.Err()
	}
	// l'installazione è avvenuta se c'è una ricevuta nuova o rifatta ora
	var fatti []string
	for _, id := range ids {
		if v, q, ok := pkgStato(id); ok && (prima[id] == "" || prima[id] != v+"|"+q) {
			fatti = append(fatti, id)
		}
	}
	if len(fatti) == 0 {
		return Esito{}, errors.New("l'Installer si è chiuso, ma il pacchetto non risulta installato: è stato annullato o non è riuscito")
	}
	esito := Esito{Asset: asset}
	var contenuti []PkgContenuto
	for _, id := range fatti {
		if c, err := pkgContenuto(id); err == nil {
			contenuti = append(contenuti, c)
		}
	}
	if app := appDi(contenuti); len(app) == 1 {
		esito.Percorso = app[0]
	}
	reg := Registro{ID: r.ID, Nome: r.Nome, Repo: r.Repo, Tag: r.Tag, Asset: asset, Tipo: TipoPkg, Fissata: r.Fissata,
		Percorso: esito.Percorso, Chiave: strings.Join(fatti, ","), NomeMostrato: fatti[0], Quando: time.Now().Format(time.RFC3339)}
	scriviRegistro(r, reg, &esito)
	return esito, nil
}

// pkgPrefissiAmmessi sono le cartelle in cui la disinstallazione accetta di
// toccare i file di un pacchetto: i pacchetti dei programmi stanno qui.
// Un file fuori da queste (un pezzo del sistema) blocca tutto: lo toglierà a
// mano chi sa cosa fa. Sostituibile nelle prove.
var pkgPrefissiAmmessi = []string{"/Applications", "/Library", "/usr/local", "/opt", "/Users"}

// pkgCartelleComuni sono cartelle condivise che, anche se un pacchetto le
// elenca, non si toccano mai: restano com'è finché non sono vuote per conto loro.
var pkgCartelleComuni = map[string]bool{
	"/Applications": true, "/Library": true, "/usr": true, "/usr/local": true, "/opt": true, "/Users": true,
	"/usr/local/bin": true, "/usr/local/lib": true, "/usr/local/share": true, "/usr/local/etc": true,
	"/usr/local/include": true, "/usr/local/sbin": true, "/usr/local/var": true, "/usr/local/opt": true,
	"/Library/Application Support": true, "/Library/LaunchDaemons": true, "/Library/LaunchAgents": true,
	"/Library/PrivilegedHelperTools": true, "/Library/Preferences": true, "/Library/Frameworks": true,
	"/Library/Extensions": true, "/Library/Fonts": true, "/Library/Audio": true, "/Library/Audio/Plug-Ins": true,
	"/Library/Internet Plug-Ins": true, "/Library/Logs": true, "/Library/Caches": true,
}

func sottoPrefissi(p string, prefissi []string) bool {
	for _, x := range prefissi {
		if p == x || strings.HasPrefix(p, x+"/") {
			return true
		}
	}
	return false
}

// ScriptPkgRimuovi è lo script che, con i permessi di amministratore, toglie
// i file dei pacchetti elencati nelle loro ricevute, poi le cartelle rimaste
// vuote (dalle più profonde) e infine le ricevute. Non parte se un file è
// fuori dalle cartelle dei programmi.
func ScriptPkgRimuovi(contenuti []PkgContenuto) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")
	var cartelle []string
	for _, c := range contenuti {
		if c.Volume != "/" {
			return "", fmt.Errorf("il pacchetto %s è installato su un altro volume (%s): CentralHub non lo toglie", c.ID, c.Volume)
		}
		for _, f := range c.File {
			if !filepath.IsLocal(f) {
				return "", fmt.Errorf("il pacchetto %s elenca un percorso non ammesso: %s", c.ID, f)
			}
			p := filepath.Join(c.Radice, f)
			if !sottoPrefissi(p, pkgPrefissiAmmessi) {
				return "", fmt.Errorf("il pacchetto %s ha file fuori dalle cartelle dei programmi (%s): CentralHub non li toglie, va tolto a mano", c.ID, p)
			}
			b.WriteString("rm -f -- " + apice(p) + "\n")
		}
		for _, d := range c.Cartelle {
			if !filepath.IsLocal(d) {
				continue
			}
			p := filepath.Join(c.Radice, d)
			if sottoPrefissi(p, pkgPrefissiAmmessi) && !pkgCartelleComuni[p] {
				cartelle = append(cartelle, p)
			}
		}
	}
	// prima le più profonde: rmdir toglie solo quelle già vuote
	sort.Slice(cartelle, func(i, j int) bool { return strings.Count(cartelle[i], "/") > strings.Count(cartelle[j], "/") })
	for _, d := range cartelle {
		b.WriteString("rmdir -- " + apice(d) + " 2>/dev/null || true\n")
	}
	for _, c := range contenuti {
		b.WriteString("/usr/sbin/pkgutil --forget " + apice(c.ID) + " >/dev/null\n")
	}
	return b.String(), nil
}

// disinstallaPkg toglie un programma installato con un .pkg.
func disinstallaPkg(d Rimozione, chiave string) error {
	var contenuti []PkgContenuto
	for _, id := range strings.Split(chiave, ",") {
		if _, _, ok := pkgStato(id); !ok {
			continue // già dimenticato dal sistema
		}
		c, err := pkgContenuto(id)
		if err != nil {
			return err
		}
		contenuti = append(contenuti, c)
	}
	if len(contenuti) == 0 {
		return nil
	}
	script, err := ScriptPkgRimuovi(contenuti)
	if err != nil {
		return err
	}
	return d.privilegiato("darwin", script)
}
