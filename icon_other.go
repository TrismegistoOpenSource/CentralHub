// SPDX-License-Identifier: MIT

//go:build !linux

package main

// appIcon non serve fuori da Linux: niente 330 KB in più nel binario.
var appIcon []byte

// windowIcon serve solo su Linux: macOS e Windows prendono l'icona dal
// pacchetto (bundle .app, risorse dell'exe).
func windowIcon(_ []byte) []byte { return nil }
