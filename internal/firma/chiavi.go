// SPDX-License-Identifier: MIT

package firma

import "crypto/ed25519"

// ChiaviPubbliche sono le chiavi di cui CentralHub si fida per le firme dei
// programmi di Trismegisto Open Source, in base64 (una per voce). Più di una
// permette di cambiare chiave senza lasciare indietro nessuno: si aggiunge la
// nuova, si pubblica una versione, e solo dopo si toglie la vecchia.
//
// La chiave privata corrispondente non sta mai nel workspace (è sincronizzato
// da MEGAsync): vedi `go run ./cmd/firma genera`.
var ChiaviPubbliche = []string{
	// chiave di Trismegisto Open Source, creata il 21/10/2026
	"1xFAv496jF2yPanmC9U1ekoZhm6+Eb/sTez19YS3mlY=",
}

// Fidate restituisce le chiavi incorporate, scartando quelle illeggibili.
func Fidate() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, s := range ChiaviPubbliche {
		if k, err := DecodificaPubblica(s); err == nil {
			out = append(out, k)
		}
	}
	return out
}
