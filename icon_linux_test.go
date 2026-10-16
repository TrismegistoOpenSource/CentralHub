// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"bytes"
	"image/png"
	"testing"
)

// L'icona passata a GTK deve stare sotto il limite di _NET_WM_ICON: con la
// 1024×1024 originale la finestra restava con l'icona generica.
func TestWindowIconRidotta(t *testing.T) {
	data := windowIcon(appIcon)
	if data == nil {
		t.Fatal("icona non decodificata")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 256 || b.Dy() != 256 {
		t.Errorf("icona %dx%d, attesa 256x256", b.Dx(), b.Dy())
	}
	if ARGB := b.Dx() * b.Dy() * 4; ARGB > 1<<20 {
		t.Errorf("%d byte in _NET_WM_ICON: troppi", ARGB)
	}
	// il centro dell'icona non è trasparente (la media non deve azzerarla)
	if _, _, _, a := img.At(128, 128).RGBA(); a == 0 {
		t.Error("centro trasparente: riduzione sbagliata")
	}
}
