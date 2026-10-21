// SPDX-License-Identifier: MIT

// Firma è lo strumento di chi pubblica: crea la coppia di chiavi e firma il
// SHA256SUMS.txt di una release. Si usa sul computer di chi pubblica, mai su
// GitHub e mai dentro il workspace sincronizzato.
//
//	go run ./cmd/firma genera [cartella]                  crea la chiave (predefinita: ~/.centralhub-firma)
//	go run ./cmd/firma firma FILE REPO TAG [cartella]     scrive FILE.sig, legata a quel repository e a quel tag
//	go run ./cmd/firma verifica FILE FIRMA REPO TAG [chiave-pubblica]
package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"centralhub/internal/firma"
)

const (
	nomePrivata  = "chiave-privata.txt"
	nomePubblica = "chiave-pubblica.txt"
)

func main() {
	if err := esegui(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "errore:", err)
		os.Exit(1)
	}
}

func cartellaPredefinita() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ".centralhub-firma"
	}
	return filepath.Join(h, ".centralhub-firma")
}

func argomento(a []string, i int, predefinito string) string {
	if len(a) > i {
		return a[i]
	}
	return predefinito
}

func esegui(a []string) error {
	if len(a) == 0 {
		return fmt.Errorf("uso: firma genera [cartella] | firma firma FILE REPO TAG [cartella] | firma verifica FILE FIRMA REPO TAG [chiave-pubblica]")
	}
	switch a[0] {
	case "genera":
		return genera(argomento(a, 1, cartellaPredefinita()))
	case "firma":
		if len(a) < 4 {
			return fmt.Errorf("uso: firma firma FILE REPO TAG [cartella] (la firma è legata a un repository e a un tag)")
		}
		return firmaFile(a[1], a[2], a[3], argomento(a, 4, cartellaPredefinita()))
	case "verifica":
		if len(a) < 5 {
			return fmt.Errorf("uso: firma verifica FILE FIRMA REPO TAG [chiave-pubblica]")
		}
		return verifica(a[1], a[2], a[3], a[4], argomento(a, 5, ""))
	}
	return fmt.Errorf("comando sconosciuto: %s", a[0])
}

func genera(cartella string) error {
	priv := filepath.Join(cartella, nomePrivata)
	if _, err := os.Stat(priv); err == nil {
		return fmt.Errorf("%s esiste già: non la sovrascrivo (se la perdi non puoi più firmare)", priv)
	}
	if abs, err := filepath.Abs(cartella); err == nil && strings.Contains(filepath.ToSlash(abs), "Documents/AiWorkspace") {
		return fmt.Errorf("%s è dentro il workspace, sincronizzato da MEGAsync: la chiave privata deve stare fuori", abs)
	}
	pub, privata, err := firma.Nuova()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cartella, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(priv, []byte(firma.CodificaPrivata(privata)+"\n"), 0o600); err != nil {
		return err
	}
	p := firma.CodificaPubblica(pub)
	if err := os.WriteFile(filepath.Join(cartella, nomePubblica), []byte(p+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("Chiave privata: %s  (SEGRETA: non condividerla e fanne una copia al sicuro)\n", priv)
	fmt.Printf("Chiave pubblica (si può dare a chiunque, va incorporata in CentralHub):\n\n%s\n\n", p)
	return nil
}

func leggiPrivata(cartella string) (privata []byte, err error) {
	b, err := os.ReadFile(filepath.Join(cartella, nomePrivata))
	if err != nil {
		return nil, fmt.Errorf("la chiave privata non si legge (%v): `firma genera` la crea", err)
	}
	return b, nil
}

func firmaFile(file, repo, tag, cartella string) error {
	kb, err := leggiPrivata(cartella)
	if err != nil {
		return err
	}
	priv, err := firma.DecodificaPrivata(string(kb))
	if err != nil {
		return err
	}
	contenuto, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := firma.Firma(priv, repo, tag, contenuto)
	if err != nil {
		return err
	}
	// controllo subito: una firma che non si verifica non va pubblicata
	if err := firma.Verifica(repo, tag, contenuto, sig, []ed25519.PublicKey{priv.Public().(ed25519.PublicKey)}); err != nil {
		return fmt.Errorf("la firma appena fatta non si verifica: %w", err)
	}
	if err := os.WriteFile(file+".sig", []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Println("scritto", file+".sig")
	return nil
}

func verifica(file, sigFile, repo, tag, chiave string) error {
	contenuto, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sigFile)
	if err != nil {
		return err
	}
	chiavi := firma.Fidate()
	if chiave != "" {
		s := chiave
		if b, err := os.ReadFile(chiave); err == nil {
			s = string(b)
		}
		k, err := firma.DecodificaPubblica(s)
		if err != nil {
			return err
		}
		chiavi = []ed25519.PublicKey{k}
	}
	if err := firma.Verifica(repo, tag, contenuto, string(sig), chiavi); err != nil {
		return err
	}
	fmt.Println("firma valida per", repo, tag)
	return nil
}
