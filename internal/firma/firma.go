// SPDX-License-Identifier: MIT

// Package firma firma e verifica con ed25519 il SHA256SUMS.txt delle release.
// Chi pubblica firma con la chiave privata (che non lascia mai il suo
// computer); chi scarica verifica con la chiave pubblica, che sta dentro
// CentralHub. La firma dice che le somme sono quelle di chi ha la chiave
// privata: se qualcuno carica su GitHub pacchetti manomessi con somme false,
// non può rifare la firma.
package firma

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// prefisso viene messo davanti a ciò che si firma: la stessa chiave non firma
// mai per sbaglio qualcosa che non è un elenco di somme di CentralHub.
const prefisso = "centralhub-sums-v2\n"

// messaggio è ciò che si firma davvero: il repository, il tag e il testo
// delle somme. La firma è legata a quella release di quel repository: la firma
// di una release (magari vecchia, magari di un altro programma) non vale per
// un'altra, anche se chi attacca la ricarica insieme alle sue somme.
func messaggio(repo, tag string, contenuto []byte) ([]byte, error) {
	if repo == "" || tag == "" || strings.ContainsAny(repo+tag, "\r\n") {
		return nil, errors.New("repository o tag non validi per la firma")
	}
	return []byte(prefisso + "repo=" + strings.ToLower(repo) + "\ntag=" + tag + "\n---\n" + string(contenuto)), nil
}

// NomeFile è il nome del file di firma accanto a SHA256SUMS.txt.
const NomeFile = "SHA256SUMS.txt.sig"

// Nuova crea una coppia di chiavi.
func Nuova() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// Firma firma il contenuto di un SHA256SUMS.txt della release `tag` del
// repository `repo` e restituisce la firma in base64 (una riga).
func Firma(priv ed25519.PrivateKey, repo, tag string, contenuto []byte) (string, error) {
	msg, err := messaggio(repo, tag, contenuto)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)), nil
}

// Riconosce dice se un testo ha la forma di una firma di CentralHub (base64
// di 64 byte). Una firma in un altro formato (minisign, GPG) non è nostra e
// non va trattata come tale.
func Riconosce(firma string) bool {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(firma))
	return err == nil && len(sig) == ed25519.SignatureSize
}

// ErrNessunaChiave: nel programma non c'è nessuna chiave pubblica fidata.
var ErrNessunaChiave = errors.New("in CentralHub non c'è nessuna chiave pubblica incorporata: non può verificare le firme")

// Verifica controlla la firma contro tutte le chiavi fidate: basta una.
func Verifica(repo, tag string, contenuto []byte, firma string, chiavi []ed25519.PublicKey) error {
	if len(chiavi) == 0 {
		return ErrNessunaChiave
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(firma))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("il file di firma non è una firma valida")
	}
	msg, err := messaggio(repo, tag, contenuto)
	if err != nil {
		return err
	}
	for _, k := range chiavi {
		if ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	return errors.New("la firma non corrisponde a nessuna chiave fidata: le somme di controllo non sono di chi pubblica questo programma")
}

// CodificaPubblica scrive una chiave pubblica in base64 (una riga di 44 caratteri).
func CodificaPubblica(k ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(k) }

// DecodificaPubblica legge una chiave pubblica scritta con CodificaPubblica.
func DecodificaPubblica(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("chiave pubblica non valida")
	}
	return ed25519.PublicKey(b), nil
}

// CodificaPrivata scrive la chiave privata (il seme di 32 byte) in base64.
func CodificaPrivata(k ed25519.PrivateKey) string { return base64.StdEncoding.EncodeToString(k.Seed()) }

// DecodificaPrivata legge una chiave privata scritta con CodificaPrivata.
func DecodificaPrivata(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, fmt.Errorf("chiave privata non valida")
	}
	return ed25519.NewKeyFromSeed(b), nil
}
