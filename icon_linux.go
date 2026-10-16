// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
)

// appIcon è l'icona di assets/ (la stessa di macOS e Windows). Su Linux va
// data a GTK per la finestra: un eseguibile, o un'AppImage non integrata nel
// desktop, non ha un file .desktop installato da cui prenderla, e la barra
// delle applicazioni mostrerebbe l'icona generica.
//
//go:embed assets/appicon.png
var appIcon []byte

// windowIcon restituisce l'icona della finestra per GTK, ridotta a 256 px.
//
// Un'icona grande (1024×1024) GTK la scrive nella
// proprietà X11 _NET_WM_ICON come pixel ARGB a 32 bit, 4 MB, oltre il limite
// di una richiesta X, e la proprietà non viene scritta affatto (finestra con
// l'icona generica nella barra; visto su una VM Linux il 07/10/2026,
// PROBLEMI-NOTI.md §29). Si riduce in memoria,
// senza un secondo file d'icona da tenere allineato.
func windowIcon(src []byte) []byte {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil
	}
	small := shrink(img, 256)
	var buf bytes.Buffer
	if err := png.Encode(&buf, small); err != nil {
		return nil
	}
	return buf.Bytes()
}

// shrink riduce l'immagine facendo la media dei pixel di ogni blocco: per
// un'icona basta, e non serve una dipendenza in più.
func shrink(img image.Image, size int) image.Image {
	b := img.Bounds()
	if b.Dx() <= size && b.Dy() <= size {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/size, b.Min.Y+(y+1)*b.Dy()/size
		for x := 0; x < size; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/size, b.Min.X+(x+1)*b.Dx()/size
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					// RGBA() è premoltiplicato: la media è corretta anche
					// sui bordi trasparenti.
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			if n == 0 {
				continue
			}
			out.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), uint16(a / n)})
		}
	}
	return out
}
