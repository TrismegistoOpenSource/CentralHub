// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// seStesso dice se il bersaglio è il programma in esecuzione (CentralHub che
// aggiorna CentralHub).
func seStesso(bersaglio string) bool {
	io := os.Getenv("APPIMAGE") // dentro un'AppImage l'eseguibile vero sta altrove
	if io == "" {
		var err error
		if io, err = os.Executable(); err != nil {
			return false
		}
	}
	reale := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	io, bersaglio = reale(io), reale(bersaglio)
	if strings.EqualFold(io, bersaglio) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(io), strings.ToLower(bersaglio)+string(filepath.Separator))
}

// inEsecuzione dice se c'è un processo lanciato da quel percorso (su Windows:
// dalla cartella del programma).
func inEsecuzione(percorso, goos string) bool {
	if goos == "windows" {
		return apertoWindows(percorso)
	}
	pgrep, err := exec.LookPath("pgrep")
	if err != nil {
		return false
	}
	ctx, annulla := context.WithTimeout(context.Background(), 5*time.Second)
	defer annulla()
	// pgrep legge il modello come espressione regolare
	return exec.CommandContext(ctx, pgrep, "-f", "--", regexp.QuoteMeta(percorso)).Run() == nil
}

// integra fa ciò che resta per poter lanciare il programma come gli altri:
// la voce nel menu delle applicazioni su Linux, il collegamento nel menu
// Start su Windows. Su macOS non serve niente. Restituisce il file creato.
func integra(r Richiesta, percorso string) (string, error) {
	switch r.GOOS {
	case "linux":
		return voceLinux(r, percorso)
	case "windows":
		return collegamentoWindows(r, percorso)
	}
	return "", nil
}

func cartellaDati() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
		return d, nil
	}
	casa, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(casa, ".local", "share"), nil
}

// VoceDesktop è il contenuto del file .desktop di un programma installato.
func VoceDesktop(nome, eseguibile, icona, id string) string {
	// Nel campo Exec un percorso fra virgolette vuole \ davanti a " ` $ \.
	exe := `"` + regexp.MustCompile("[\"`$\\\\]").ReplaceAllString(eseguibile, `\$0`) + `"`
	var b strings.Builder
	b.WriteString("[Desktop Entry]\nType=Application\n")
	b.WriteString("Name=" + strings.NewReplacer("\n", " ", "\r", " ").Replace(nome) + "\n")
	b.WriteString("Exec=" + exe + " %U\n")
	if icona != "" {
		b.WriteString("Icon=" + icona + "\n")
	}
	b.WriteString("Terminal=false\nCategories=Utility;\n")
	b.WriteString("StartupWMClass=" + NomeProgramma(nome) + "\n")
	b.WriteString("X-CentralHub-Repo=" + id + "\n")
	return b.String()
}

func nomeVoce(nome string) string {
	return "centralhub-" + strings.ToLower(NomeProgramma(nome)) + ".desktop"
}

func voceLinux(r Richiesta, appimage string) (string, error) {
	dati, err := cartellaDati()
	if err != nil {
		return "", err
	}
	icona, errIcona := iconaAppImage(appimage, filepath.Join(r.CartellaRegistro, NomeProgramma(r.Nome)+".png"))
	voce := filepath.Join(dati, "applications", nomeVoce(r.Nome))
	if err := os.MkdirAll(filepath.Dir(voce), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(voce, []byte(VoceDesktop(r.Nome, appimage, icona, r.ID)), 0o644); err != nil {
		return "", err
	}
	// il menu si aggiorna da solo; dove c'è, lo si avvisa (non è un errore se manca)
	if u, err := exec.LookPath("update-desktop-database"); err == nil {
		ctx, annulla := context.WithTimeout(context.Background(), 10*time.Second)
		defer annulla()
		exec.CommandContext(ctx, u, filepath.Dir(voce)).Run()
	}
	if errIcona != nil {
		return voce, fmt.Errorf("icona non trovata nell'AppImage (la voce c'è, con l'icona generica): %w", errIcona)
	}
	return voce, nil
}

// apice protegge un testo per la shell: fra apici singoli, con gli apici
// interni chiusi e riaperti.
func apice(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ScriptLinuxSistema è lo script che, con i permessi di amministratore,
// mette l'AppImage in /opt/<Nome>/ con la sua icona e la voce nel menu di
// tutti gli utenti, e toglie la versione vecchia. I file arrivano già pronti
// da una cartella dell'utente: lo script li copia e basta.
func ScriptLinuxSistema(appimage, bersaglio, icona, voce, voceBersaglio string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -e\n")
	b.WriteString("mkdir -p " + apice(filepath.Dir(bersaglio)) + "\n")
	b.WriteString("if [ -e " + apice(bersaglio) + " ]; then mv -f " + apice(bersaglio) + " " + apice(bersaglio+suffissoPrecedente) + "; fi\n")
	b.WriteString("install -m 0755 " + apice(appimage) + " " + apice(bersaglio) + "\n")
	if icona != "" {
		b.WriteString("install -m 0644 " + apice(icona) + " " + apice(strings.TrimSuffix(bersaglio, ".AppImage")+".png") + "\n")
	}
	b.WriteString("install -D -m 0644 " + apice(voce) + " " + apice(voceBersaglio) + "\n")
	// riuscito lo scambio, la versione vecchia si cancella
	b.WriteString("rm -f " + apice(bersaglio+suffissoPrecedente) + "\n")
	return b.String()
}

func installaLinuxSistema(r Richiesta, bersaglio string) (voceBersaglio string, avvisi []string, err error) {
	lavoro := filepath.Join(r.CartellaRegistro, "preparazione")
	if err := os.MkdirAll(lavoro, 0o700); err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(lavoro)
	// l'icona si estrae da utente normale, da una copia eseguibile
	copiaApp := filepath.Join(lavoro, filepath.Base(bersaglio))
	if err := copia(r.File, copiaApp, 0o755); err != nil {
		return "", nil, err
	}
	icona, errIcona := "", error(nil)
	if r.GOOS == runtime.GOOS {
		icona, errIcona = iconaAppImage(copiaApp, filepath.Join(lavoro, "icona.png"))
	}
	iconaFinale := ""
	if errIcona != nil {
		avvisi = append(avvisi, "icona non trovata nell'AppImage: la voce di menu avrà l'icona generica")
	} else if icona != "" {
		iconaFinale = strings.TrimSuffix(bersaglio, ".AppImage") + ".png"
	}
	voce := filepath.Join(lavoro, nomeVoce(r.Nome))
	if err := os.WriteFile(voce, []byte(VoceDesktop(r.Nome, bersaglio, iconaFinale, r.ID)), 0o644); err != nil {
		return "", nil, err
	}
	voceBersaglio = "/usr/share/applications/" + nomeVoce(r.Nome)
	if err := r.privilegiato("linux", ScriptLinuxSistema(copiaApp, bersaglio, icona, voce, voceBersaglio)); err != nil {
		return "", nil, err
	}
	return voceBersaglio, avvisi, nil
}

// apicePS protegge un testo per PowerShell: fra apici singoli, con gli
// apici interni raddoppiati.
func apicePS(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func menuStartComune() string {
	d := os.Getenv("ProgramData")
	if d == "" {
		d = `C:\ProgramData`
	}
	return filepath.Join(d, "Microsoft", "Windows", "Start Menu", "Programs")
}

// ScriptWindowsSistema è lo script PowerShell che, con i permessi di
// amministratore, mette la cartella del programma in %ProgramFiles% (la
// vecchia si mette da parte come ".precedente" e a copia riuscita si
// cancella) e crea il collegamento nel menu Start di tutti gli utenti.
func ScriptWindowsSistema(nuovo, bersaglio, collegamento, exe string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	b.WriteString("$n = " + apicePS(nuovo) + "\r\n$t = " + apicePS(bersaglio) + "\r\n$p = $t + '.precedente'\r\n")
	b.WriteString("if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }\r\n")
	b.WriteString("if (Test-Path -LiteralPath $t) { Move-Item -LiteralPath $t -Destination $p }\r\n")
	b.WriteString("try { Copy-Item -LiteralPath $n -Destination $t -Recurse -Force }\r\n")
	b.WriteString("catch { if (Test-Path -LiteralPath $t) { Remove-Item -LiteralPath $t -Recurse -Force }; if (Test-Path -LiteralPath $p) { Move-Item -LiteralPath $p -Destination $t }; throw }\r\n")
	// riuscita la copia, la versione vecchia si cancella
	b.WriteString("if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }\r\n")
	if exe != "" && collegamento != "" {
		b.WriteString("$s = (New-Object -ComObject WScript.Shell).CreateShortcut(" + apicePS(collegamento) + ")\r\n")
		b.WriteString("$s.TargetPath = Join-Path $t " + apicePS(exe) + "\r\n$s.WorkingDirectory = $t\r\n$s.Save()\r\n")
	}
	return b.String()
}

// eseguiPrivilegiato lancia uno script con i permessi di amministratore: è
// il sistema a chiedere la conferma o la password (pkexec su Linux, UAC su
// Windows). Lo script sta in un file temporaneo leggibile solo dall'utente.
func eseguiPrivilegiato(goos, script string) error {
	est := map[string]string{"windows": ".ps1"}[goos]
	f, err := os.CreateTemp("", "centralhub-installa-*"+est)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if goos == "windows" {
		script = "\ufeff" + script // senza BOM PowerShell 5.1 legge gli accenti dei percorsi come cp1252
	}
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ctx, annulla := context.WithTimeout(context.Background(), 10*time.Minute)
	defer annulla()
	var cmd *exec.Cmd
	switch goos {
	case "linux":
		pkexec, err := exec.LookPath("pkexec")
		if err != nil {
			return errors.New("per installare per tutto il computer serve pkexec (pacchetto polkit), che su questo sistema non c'è")
		}
		cmd = exec.CommandContext(ctx, pkexec, "/bin/sh", f.Name())
	case "darwin":
		// La finestra della password la mostra il sistema (osascript, "with
		// administrator privileges"). Il percorso dello script arriva come
		// argomento, non nel testo: nessun carattere da proteggere.
		cmd = exec.CommandContext(ctx, "/usr/bin/osascript",
			"-e", "on run argv",
			"-e", `do shell script "/bin/sh " & quoted form of (item 1 of argv) with administrator privileges`,
			"-e", "end run", f.Name())
	case "windows":
		// Il percorso dello script passa da una variabile d'ambiente, non
		// dal testo del comando. Il codice di uscita è quello dello script;
		// se la richiesta viene rifiutata Start-Process dà errore, e senza
		// 'Stop' si uscirebbe con 0 come se l'installazione fosse riuscita.
		esterno := `$ErrorActionPreference = 'Stop'; $p = Start-Process powershell -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList ` +
			`'-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',('"' + $env:CENTRALHUB_SCRIPT + '"'); exit $p.ExitCode`
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", esterno)
		cmd.Env = append(os.Environ(), "CENTRALHUB_SCRIPT="+f.Name())
		nascondi(cmd)
	default:
		return fmt.Errorf("permessi di amministratore non previsti su %s", goos)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("la richiesta dei permessi di amministratore non ha avuto risposta")
		}
		return fmt.Errorf("installazione per tutto il computer non riuscita o non autorizzata: %s", strings.TrimSpace(string(out)+" "+err.Error()))
	}
	return nil
}

// iconaAppImage tira fuori l'icona dall'AppImage. Lo fa il programma
// d'avvio dell'AppImage stessa (--appimage-extract), in una cartella
// temporanea: funziona anche senza FUSE.
func iconaAppImage(appimage, dest string) (string, error) {
	tmp, err := os.MkdirTemp("", "centralhub-icona-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	ctx, annulla := context.WithTimeout(context.Background(), 30*time.Second)
	defer annulla()
	cmd := exec.CommandContext(ctx, appimage, "--appimage-extract", "*.png")
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
	}
	radice := filepath.Join(tmp, "squashfs-root")
	var png []string
	filepath.WalkDir(radice, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(strings.ToLower(p), ".png") {
			png = append(png, p)
		}
		return nil
	})
	if len(png) == 0 {
		return "", fmt.Errorf("nessun file .png")
	}
	// prima quella al primo livello (l'icona dell'app), poi la più grande
	sort.SliceStable(png, func(i, j int) bool {
		ci, cj := filepath.Dir(png[i]) == radice, filepath.Dir(png[j]) == radice
		if ci != cj {
			return ci
		}
		fi, _ := os.Stat(png[i])
		fj, _ := os.Stat(png[j])
		return fi != nil && fj != nil && fi.Size() > fj.Size()
	})
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := copia(png[0], dest, 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

// EseguibileWindows trova l'exe da lanciare in una cartella installata:
// quello che si chiama come il programma, altrimenti l'unico exe al primo
// livello.
func EseguibileWindows(cartella, nome string) (string, error) {
	voci, err := os.ReadDir(cartella)
	if err != nil {
		return "", err
	}
	var exe []string
	for _, v := range voci {
		if !v.IsDir() && strings.HasSuffix(strings.ToLower(v.Name()), ".exe") {
			if strings.EqualFold(strings.TrimSuffix(v.Name(), filepath.Ext(v.Name())), NomeProgramma(nome)) {
				return filepath.Join(cartella, v.Name()), nil
			}
			exe = append(exe, v.Name())
		}
	}
	if len(exe) == 1 {
		return filepath.Join(cartella, exe[0]), nil
	}
	return "", fmt.Errorf("in %s ci sono %d exe: non si sa quale lanciare", cartella, len(exe))
}

func collegamentoWindows(r Richiesta, cartella string) (string, error) {
	exe, err := EseguibileWindows(cartella, r.Nome)
	if err != nil {
		return "", err
	}
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return "", fmt.Errorf("variabile APPDATA assente")
	}
	lnk := filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", NomeProgramma(r.Nome)+".lnk")
	if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
		return "", err
	}
	// I percorsi passano dalle variabili d'ambiente, non dal testo del
	// comando: nessun carattere da proteggere.
	script := `$s=(New-Object -ComObject WScript.Shell).CreateShortcut($env:CENTRALHUB_LNK);` +
		`$s.TargetPath=$env:CENTRALHUB_EXE;$s.WorkingDirectory=$env:CENTRALHUB_DIR;$s.Save()`
	ctx, annulla := context.WithTimeout(context.Background(), 30*time.Second)
	defer annulla()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "CENTRALHUB_LNK="+lnk, "CENTRALHUB_EXE="+exe, "CENTRALHUB_DIR="+cartella)
	nascondi(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
	}
	return lnk, nil
}
