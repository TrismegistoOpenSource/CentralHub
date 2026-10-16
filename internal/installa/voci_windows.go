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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Gli installatori di Windows (setup.exe, .msi): CentralHub li avvia e poi
// legge dal registro, fra le app installate, la voce che hanno lasciato. È
// la stessa voce che mostra «Impostazioni › App», e dice come disinstallare.

func init() {
	leggiVoci = vociWindows
	voceElencata = func(chiave string) bool {
		radice, nome, _ := strings.Cut(chiave, ":")
		for _, r := range radici {
			if r.nome == radice {
				_, c := voceWindows(r, nome)
				return c
			}
		}
		return false
	}
	avviaInstallatore = installatoreWindows
	avviaDisinstallatore = disinstallatoreWindows
}

// radice è una delle chiavi del registro in cui i programmi si registrano
// per la disinstallazione.
type radice struct {
	nome     string
	chiave   registry.Key
	percorso string
	vista    uint32
}

const chiaveDisinstallazione = `Software\Microsoft\Windows\CurrentVersion\Uninstall`

// radici: per tutto il computer (programmi a 64 e a 32 bit) e per l'utente.
// Sostituibile nelle prove, che non scrivono mai nelle chiavi vere.
var radici = []radice{
	{"HKLM64", registry.LOCAL_MACHINE, chiaveDisinstallazione, registry.WOW64_64KEY},
	{"HKLM32", registry.LOCAL_MACHINE, chiaveDisinstallazione, registry.WOW64_32KEY},
	{"HKCU", registry.CURRENT_USER, chiaveDisinstallazione, 0},
}

func vociWindows() map[string]VoceSistema {
	voci := map[string]VoceSistema{}
	for _, r := range radici {
		k, err := registry.OpenKey(r.chiave, r.percorso, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|r.vista)
		if err != nil {
			continue
		}
		nomi, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, n := range nomi {
			if v, ok := voceWindows(r, n); ok {
				voci[v.Chiave] = v
			}
		}
	}
	return voci
}

func voceWindows(r radice, nome string) (VoceSistema, bool) {
	k, err := registry.OpenKey(r.chiave, r.percorso+`\`+nome, registry.QUERY_VALUE|r.vista)
	if err != nil {
		return VoceSistema{}, false
	}
	defer k.Close()
	testo := func(n string) string { s, _, _ := k.GetStringValue(n); return strings.TrimSpace(s) }
	numero := func(n string) uint64 { x, _, _ := k.GetIntegerValue(n); return x }
	v := VoceSistema{
		Chiave: r.nome + ":" + nome, Nome: testo("DisplayName"), Versione: testo("DisplayVersion"), Data: testo("InstallDate"),
		Posizione: strings.Trim(testo("InstallLocation"), `"`), Disinstalla: testo("UninstallString"), MSI: numero("WindowsInstaller") == 1,
	}
	// senza nome non compare fra le app installate; i componenti di sistema nemmeno
	if v.Nome == "" || numero("SystemComponent") == 1 {
		return VoceSistema{}, false
	}
	return v, true
}

// installatoreWindows avvia un setup.exe o un .msi e aspetta che si chiuda.
// Lo lancia la shell di Windows (Start-Process), come un doppio clic: così
// un installatore che vuole i permessi di amministratore li chiede da sé. Si
// aspetta solo il processo dell'installatore, non i programmi che avvia alla
// fine.
func installatoreWindows(ctx context.Context, file string) (int, error) {
	// Il percorso passa da una variabile d'ambiente, non dal testo del comando.
	avvio := `Start-Process -FilePath $env:CENTRALHUB_FILE -WorkingDirectory $env:CENTRALHUB_DIR -PassThru`
	if strings.HasSuffix(strings.ToLower(file), ".msi") {
		avvio = `Start-Process -FilePath msiexec.exe -ArgumentList '/i', ('"' + $env:CENTRALHUB_FILE + '"') -WorkingDirectory $env:CENTRALHUB_DIR -PassThru`
	}
	// $p.Handle va letto prima dell'uscita, altrimenti ExitCode resta vuoto.
	// 9009 distingue «non è partito» da qualunque codice dell'installatore.
	script := `try { $p = ` + avvio + `; $h = $p.Handle } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 9009 }; $p.WaitForExit(); exit $p.ExitCode`
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "CENTRALHUB_FILE="+file, "CENTRALHUB_DIR="+filepath.Dir(file))
	nascondi(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var uscita *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case !errors.As(err, &uscita):
		return 0, fmt.Errorf("l'installatore non è partito: %w", err)
	case uscita.ExitCode() == 9009:
		return 0, fmt.Errorf("l'installatore non è partito (permessi negati, o il file non è un programma): %s", strings.TrimSpace(string(out)))
	}
	return uscita.ExitCode(), nil
}

var reGUID = regexp.MustCompile(`^\{[0-9A-Fa-f-]{36}\}$`)

// attesaDisinstallatore: quanto si aspetta, dopo la chiusura del comando di
// disinstallazione, che la voce sparisca. Molti disinstallatori si copiano
// in una cartella temporanea e proseguono da lì: il comando torna subito,
// ma la finestra è ancora aperta davanti all'utente.
var attesaDisinstallatore = 3 * time.Minute

// disinstallatoreWindows lancia la disinstallazione dichiarata da una voce,
// come fa «Impostazioni › App», e aspetta che la voce sparisca dal registro.
func disinstallatoreWindows(ctx context.Context, v VoceSistema) error {
	_, nome, _ := strings.Cut(v.Chiave, ":")
	var cmd *exec.Cmd
	switch {
	case reGUID.MatchString(nome) && (v.MSI || strings.Contains(strings.ToLower(v.Disinstalla), "msiexec")):
		cmd = exec.CommandContext(ctx, "msiexec.exe", "/x", nome)
	case v.Disinstalla == "":
		return errors.New(v.Nome + " non dichiara come si disinstalla: va tolto da Impostazioni › App")
	default:
		// Il comando è già una riga di comando completa: la esegue cmd, che
		// per un disinstallatore che vuole i permessi di amministratore fa
		// partire la richiesta del sistema.
		cmd = exec.CommandContext(ctx, "cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + v.Disinstalla + `"`, HideWindow: true, CreationFlags: 0x08000000}
	}
	if cmd.SysProcAttr == nil {
		nascondi(cmd)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	attesa := attesaDisinstallatore
	if err != nil {
		attesa = 5 * time.Second // comando fallito o rifiutato: la voce non sparirà
	}
	for fine := time.Now().Add(attesa); ; time.Sleep(time.Second) {
		if !voceElencata(v.Chiave) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(fine) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("la disinstallazione di %s non è riuscita o è stata annullata: %s", v.Nome, strings.TrimSpace(string(out)+" "+err.Error()))
	}
	return errors.New("la disinstallazione di " + v.Nome + " non risulta finita: se la sua finestra è ancora aperta va completata lì, altrimenti è stata annullata")
}
