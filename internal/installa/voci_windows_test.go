// SPDX-License-Identifier: MIT

package installa

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// registroDiProva sposta la ricerca delle app installate in una chiave
// temporanea dell'utente: le prove non scrivono mai nelle chiavi vere di
// Windows. Restituisce il percorso della chiave (sotto HKCU).
func registroDiProva(t *testing.T) string {
	t.Helper()
	percorso := fmt.Sprintf(`Software\CentralHub-prova-%d-%d`, os.Getpid(), time.Now().UnixNano())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, percorso, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	k.Close()
	vecchie := radici
	radici = []radice{{"HKCU", registry.CURRENT_USER, percorso, 0}}
	t.Cleanup(func() {
		radici = vecchie
		if k, err := registry.OpenKey(registry.CURRENT_USER, percorso, registry.ALL_ACCESS); err == nil {
			nomi, _ := k.ReadSubKeyNames(-1)
			for _, n := range nomi {
				registry.DeleteKey(k, n)
			}
			k.Close()
		}
		registry.DeleteKey(registry.CURRENT_USER, percorso)
	})
	return percorso
}

func scriviVoce(t *testing.T, percorso, nome string, testi map[string]string, numeri map[string]uint32) {
	t.Helper()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, percorso+`\`+nome, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for n, v := range testi {
		if err := k.SetStringValue(n, v); err != nil {
			t.Fatal(err)
		}
	}
	for n, v := range numeri {
		if err := k.SetDWordValue(n, v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVociWindows(t *testing.T) {
	// le chiavi vere si leggono senza errori (sola lettura): su un Windows
	// qualunque c'è almeno un programma installato
	if len(vociWindows()) == 0 {
		t.Error("nessuna app installata letta dal registro vero")
	}
	p := registroDiProva(t)
	scriviVoce(t, p, "Prova", map[string]string{"DisplayName": "Prova Studio", "DisplayVersion": "1.2.0", "InstallLocation": `"C:\Programmi\Prova\"`,
		"UninstallString": `"C:\Programmi\Prova\unins000.exe" /x`}, nil)
	scriviVoce(t, p, "{11111111-2222-3333-4444-555555555555}", map[string]string{"DisplayName": "Prova MSI", "UninstallString": "MsiExec.exe /I{11111111-2222-3333-4444-555555555555}"},
		map[string]uint32{"WindowsInstaller": 1})
	scriviVoce(t, p, "SenzaNome", map[string]string{"UninstallString": "x"}, nil)
	scriviVoce(t, p, "Componente", map[string]string{"DisplayName": "Componente di sistema"}, map[string]uint32{"SystemComponent": 1})
	voci := vociWindows()
	if len(voci) != 2 {
		t.Fatalf("voci lette: %+v", voci)
	}
	v := voci["HKCU:Prova"]
	if v.Nome != "Prova Studio" || v.Versione != "1.2.0" || v.Posizione != `C:\Programmi\Prova\` || v.MSI || !strings.Contains(v.Disinstalla, "unins000.exe") {
		t.Errorf("voce: %+v", v)
	}
	if m := voci["HKCU:{11111111-2222-3333-4444-555555555555}"]; !m.MSI {
		t.Errorf("voce di Windows Installer: %+v", m)
	}
	if !voceElencata("HKCU:Prova") || voceElencata("HKCU:SenzaNome") || voceElencata("HKCU:NonEsiste") || voceElencata("HKLM64:Prova") {
		t.Error("voceElencata")
	}
	if got, ok := voceDiCartella(voci, `c:\programmi\prova`); !ok || got.Chiave != "HKCU:Prova" {
		t.Errorf("voce dalla cartella: %+v %v", got, ok)
	}
}

// Un installatore e un disinstallatore veri, fatti con due file di comandi:
// il primo registra il programma come fa un setup, il secondo lo toglie.
func TestInstallatoreEDisinstallatoreWindows(t *testing.T) {
	p := registroDiProva(t)
	base := t.TempDir()
	chiave := `HKCU\` + p + `\Prova`
	setup := filepath.Join(base, "Prova Setup (1.0).cmd") // spazi e parentesi nel nome
	os.WriteFile(setup, []byte("@echo off\r\nreg add \""+chiave+"\" /v DisplayName /d \"Prova\" /f >nul\r\n"+
		"reg add \""+chiave+"\" /v UninstallString /d \"reg delete \\\""+chiave+"\\\" /f\" /f >nul\r\nexit 0\r\n"), 0o644)
	vecchia := attesaDisinstallatore
	attesaDisinstallatore = 20 * time.Second
	t.Cleanup(func() { attesaDisinstallatore = vecchia })

	q := nuovaProva(t)
	r := q.richiesta("windows", "", "v1.0", setup)
	r.Tipo = TipoInstallatore
	e, err := Installa(r)
	if err != nil {
		t.Fatal(err)
	}
	s := LeggiStato(q.reg, "Prova", "windows", q.c)
	if !s.Presente || s.Tipo != TipoInstallatore || s.Tag != "v1.0" || e.Asset != "Prova Setup (1.0).cmd" {
		t.Fatalf("dopo l'installatore: %+v %+v", s, e)
	}
	// il disinstallatore dichiarato toglie la voce: CentralHub lo lancia e aspetta
	if _, err := Disinstalla(Rimozione{Nome: "Prova", Cartelle: q.c, CartellaRegistro: q.reg}); err != nil {
		t.Fatal(err)
	}
	if voceElencata("HKCU:Prova") || LeggiStato(q.reg, "Prova", "windows", q.c).Presente {
		t.Error("dopo il disinstallatore il programma risulta ancora installato")
	}

	// il codice di uscita dell'installatore arriva fin qui
	fallisce := filepath.Join(base, "fallisce.cmd")
	os.WriteFile(fallisce, []byte("@exit 7\r\n"), 0o644)
	if codice, err := installatoreWindows(context.Background(), fallisce); err != nil || codice != 7 {
		t.Errorf("codice di uscita: %d %v", codice, err)
	}
	// un file che non c'è: non è partito, e lo si dice
	if _, err := installatoreWindows(context.Background(), filepath.Join(base, "non-esiste.exe")); err == nil || !strings.Contains(err.Error(), "non è partito") {
		t.Errorf("installatore inesistente: %v", err)
	}
	// un disinstallatore che non toglie la voce: dopo l'attesa è un errore
	scriviVoce(t, p, "Resta", map[string]string{"DisplayName": "Resta", "UninstallString": "cmd /c exit 0"}, nil)
	attesaDisinstallatore = 2 * time.Second
	if err := disinstallatoreWindows(context.Background(), vociWindows()["HKCU:Resta"]); err == nil {
		t.Error("disinstallazione che non toglie niente: deve dirlo")
	}
	scriviVoce(t, p, "Muta", map[string]string{"DisplayName": "Muta"}, nil)
	if err := disinstallatoreWindows(context.Background(), vociWindows()["HKCU:Muta"]); err == nil {
		t.Error("voce senza comando di disinstallazione: deve dirlo")
	}
}

// Lo script di rimozione per tutto il computer, eseguito davvero (su una
// cartella temporanea non servono i permessi di amministratore).
func TestScriptWindowsRimuoviDavvero(t *testing.T) {
	base := filepath.Join(t.TempDir(), "con spazi e l'apostrofo è qui")
	dir := filepath.Join(base, "Prova")
	lnk := filepath.Join(base, "menu", "Prova.lnk")
	scrivi(t, filepath.Join(dir, "Prova.exe"), "exe")
	scrivi(t, filepath.Join(dir, "LICENZE", "LICENSE"), "gpl")
	scrivi(t, filepath.Join(dir+".precedente", "Prova.exe"), "vecchio")
	scrivi(t, lnk, "collegamento")
	scrivi(t, filepath.Join(base, "Altro", "x"), "non si tocca")
	file := filepath.Join(base, "rimuovi.ps1")
	os.WriteFile(file, []byte("\ufeff"+ScriptWindowsRimuovi(dir, lnk)), 0o600) // con il BOM, come eseguiPrivilegiato
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if esiste(dir) || esiste(dir+".precedente") || esiste(lnk) || !esiste(filepath.Join(base, "Altro", "x")) {
		t.Error("lo script di rimozione non ha tolto ciò che doveva, o ha tolto altro")
	}
	// a cose già tolte non è un errore
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file).CombinedOutput(); err != nil {
		t.Errorf("seconda esecuzione: %v %s", err, out)
	}
}
