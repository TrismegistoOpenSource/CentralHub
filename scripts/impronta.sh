#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# IDENTICO IN OGNI PROGETTO (procedura di test del workspace,).
#
# L'impronta è l'identità del sorgente provato: un SHA-256 calcolato su nome e
# contenuto di ogni file di sourcecode/ che va nel commit. Le tre macchine la
# calcolano sullo stesso workspace (MEGAsync copia i file byte per byte) e la
# scrivono nell'attestato; il Mac committa solo se le tre impronte coincidono
# con quella del workspace e con i file del commit (scripts/pubblica-release.sh).
#
# Uso, da qualunque cartella:
#   bash scripts/impronta.sh            stampa l'impronta
#   bash scripts/impronta.sh --elenco   stampa i file che la compongono
#
# Fuori dall'impronta, perché non vanno nel commit o non viaggiano:
#   - file e cartelle che iniziano con il punto (.git, .gitignore, .DS_Store:
#     MEGAsync non li porta), TRANNE .github/, che viaggia ed è nel commit;
#   - ciò che generano le build: node_modules, frontend/dist,
#     frontend/wailsjs, frontend/package.json.md5, build/, dist/, *.AppDir.
# Il Mac controlla che i file del commit siano esattamente questo elenco: un
# file dimenticato o in più ferma la pubblicazione.

set -euo pipefail
cd "$(dirname "$0")/.."
export LC_ALL=C

elenco() {
  find . \( -name node_modules -o -path ./frontend/dist -o -path ./frontend/wailsjs \
            -o -path ./build -o -path ./dist -o -name '*.AppDir' \
            -o \( -name '.*' ! -name . ! -name .github \) \) -prune \
       -o -type f ! -path ./frontend/package.json.md5 -print \
    | sed 's|^\./||' | sort
}

sha() {
  if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1
  else shasum -a 256 | cut -d' ' -f1; fi
}

case "${1:-}" in
  --elenco) elenco ;;
  "")
    elenco | while IFS= read -r f; do
      printf '%s  %s\n' "$(sha < "$f")" "$f"
    done | sha
    ;;
  *) echo "uso: $0 [--elenco]" >&2; exit 2 ;;
esac
