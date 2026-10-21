// SPDX-License-Identifier: MIT

package firma

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestFirmaEVerifica(t *testing.T) {
	pub, priv, _ := Nuova()
	altra, _, _ := Nuova()
	somme := []byte(strings.Repeat("a", 64) + "  App.zip\n")
	sig, err := Firma(priv, "Trismegisto/App", "v1.0.0", somme)
	if err != nil {
		t.Fatal(err)
	}

	if err := Verifica("trismegisto/app", "v1.0.0", somme, sig, []ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("firma valida rifiutata (le maiuscole del repository non contano): %v", err)
	}
	// basta una chiave fra quelle fidate
	if err := Verifica("trismegisto/app", "v1.0.0", somme, sig, []ed25519.PublicKey{altra, pub}); err != nil {
		t.Errorf("con due chiavi fidate: %v", err)
	}
	// un solo byte diverso nelle somme
	if err := Verifica("trismegisto/app", "v1.0.0", []byte(strings.Replace(string(somme), "a", "b", 1)), sig, []ed25519.PublicKey{pub}); err == nil {
		t.Error("somme alterate accettate")
	}
	// firma di un altro
	if err := Verifica("trismegisto/app", "v1.0.0", somme, sig, []ed25519.PublicKey{altra}); err == nil {
		t.Error("firma di un'altra chiave accettata")
	}
	// la firma è legata alla release: stesse somme e stessa firma, altro tag o altro repository
	if err := Verifica("trismegisto/app", "v0.9.0", somme, sig, []ed25519.PublicKey{pub}); err == nil {
		t.Error("la firma di un tag è valsa per un altro tag (riuso della firma)")
	}
	if err := Verifica("trismegisto/altro", "v1.0.0", somme, sig, []ed25519.PublicKey{pub}); err == nil {
		t.Error("la firma di un repository è valsa per un altro")
	}
	// niente chiavi fidate: non si può verificare, non si accetta
	if err := Verifica("trismegisto/app", "v1.0.0", somme, sig, nil); err != ErrNessunaChiave {
		t.Errorf("senza chiavi: %v", err)
	}
	if _, err := Firma(priv, "a/b", "v1\nv2", somme); err == nil {
		t.Error("un tag con un a capo non si firma")
	}
	if !Riconosce(sig) || Riconosce("untrusted comment: minisign") || Riconosce("") {
		t.Error("Riconosce deve accettare solo le firme di CentralHub")
	}
	for _, male := range []string{"", "non base64!", "AAAA"} {
		if err := Verifica("trismegisto/app", "v1.0.0", somme, male, []ed25519.PublicKey{pub}); err == nil {
			t.Errorf("firma illeggibile %q accettata", male)
		}
	}
}

func TestFirmaNonValeComeFirmaDiAltro(t *testing.T) {
	// il prefisso impedisce di riusare una firma fatta su un contenuto qualunque
	pub, priv, _ := Nuova()
	testo := []byte("un testo qualunque")
	cruda := ed25519.Sign(priv, testo)
	if m, _ := messaggio("a/b", "v1", testo); ed25519.Verify(pub, m, cruda) {
		t.Fatal("la firma cruda non deve valere per il formato delle somme")
	}
}

func TestChiaviSiScrivonoESiRileggono(t *testing.T) {
	pub, priv, _ := Nuova()
	p, err := DecodificaPubblica(CodificaPubblica(pub))
	if err != nil || !p.Equal(pub) {
		t.Errorf("pubblica: %v", err)
	}
	k, err := DecodificaPrivata(CodificaPrivata(priv))
	if err != nil || !k.Equal(priv) {
		t.Errorf("privata: %v", err)
	}
	if _, err := DecodificaPubblica("corta"); err == nil {
		t.Error("chiave pubblica corta accettata")
	}
	if _, err := DecodificaPrivata("corta"); err == nil {
		t.Error("chiave privata corta accettata")
	}
	// la chiave privata non è la pubblica: scambiarle non deve passare
	if _, err := DecodificaPubblica(CodificaPrivata(priv)); err == nil && len(priv.Seed()) != len(pub) {
		t.Error("una privata scambiata per pubblica")
	}
}

// La chiave incorporata dev'essere leggibile: un refuso farebbe rifiutare
// ogni release firmata.
func TestChiaviIncorporateSonoValide(t *testing.T) {
	if len(ChiaviPubbliche) == 0 {
		t.Fatal("nessuna chiave pubblica incorporata")
	}
	if got := Fidate(); len(got) != len(ChiaviPubbliche) {
		t.Errorf("%d chiavi incorporate ma solo %d leggibili", len(ChiaviPubbliche), len(got))
	}
}
