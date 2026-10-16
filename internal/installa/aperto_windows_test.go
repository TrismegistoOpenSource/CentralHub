// SPDX-License-Identifier: MIT

package installa

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApertoWindows(t *testing.T) {
	io, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !apertoWindows(filepath.Dir(io)) {
		t.Error("la prova stessa gira da questa cartella: doveva risultare aperta")
	}
	if apertoWindows(t.TempDir()) {
		t.Error("da una cartella vuota non gira nessun programma")
	}
}
