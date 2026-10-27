#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Elenco di integrità di un pacchetto, per l'autoverifica all'avvio
# (internal/autoverifica). Identico in ogni progetto.
#
#   bash scripts/integrita-pacchetto.sh genera <radice> <uscita> [<esclusione>...]
#       scrive in <uscita> «<sha256>  <percorso>» per ogni file sotto <radice>
#       (percorsi relativi alla radice, con la barra, in ordine). Esclude sempre
#       <radice>/INTEGRITA.txt e .DS_Store; <esclusione> è un percorso relativo
#       (un file) o, se finisce con «/», una cartella.
#   bash scripts/integrita-pacchetto.sh controlla <radice> <elenco>
#       esce con errore se un file elencato manca o è diverso.
#
# La radice è ciò che il programma vede come «la sua cartella»: il bundle .app su
# macOS, la AppDir dell'AppImage su Linux, la cartella dell'eseguibile su Windows.
set -euo pipefail
export LC_ALL=C

sha_cmd() {
  if command -v sha256sum >/dev/null; then echo "sha256sum"; else echo "shasum -a 256"; fi
}

uso() { echo "uso: $0 genera <radice> <uscita> [esclusione...] | controlla <radice> <elenco>" >&2; exit 2; }

genera() {
  local radice="$1" uscita="$2" tmp lista e f skip n
  shift 2
  [ -d "$radice" ] || { echo "manca la cartella $radice" >&2; exit 1; }
  tmp="$(mktemp -d "${TMPDIR:-${TEMP:-/tmp}}/integrita-XXXXXX")"
  # tra apici doppi: $tmp è locale e alla chiusura non esisterebbe più
  trap "rm -rf '$tmp'" EXIT
  lista="$tmp/lista"
  (cd "$radice" && find . -type f ! -name .DS_Store -print | sed 's|^\./||' | sort) > "$lista"
  : > "$tmp/scelti"
  while IFS= read -r f; do
    skip=0
    [ "$f" = "INTEGRITA.txt" ] && skip=1
    for e in "$@"; do
      case "$e" in
        */) [[ "$f" == "$e"* ]] && skip=1 ;;
        *) [ "$f" = "$e" ] && skip=1 ;;
      esac
    done
    [ "$skip" = 0 ] && printf '%s\n' "$f" >> "$tmp/scelti"
  done < "$lista"
  n="$(wc -l < "$tmp/scelti" | tr -d ' ')"
  [ "$n" -gt 0 ] || { echo "nessun file da elencare sotto $radice" >&2; exit 1; }
  # il lettore dell'app rifiuta barre rovesciate e due punti: meglio fermarsi qui
  if grep -q '[\\:]' "$tmp/scelti"; then
    echo "percorsi con «\\» o «:» non ammessi nell'elenco:" >&2; grep '[\\:]' "$tmp/scelti" >&2; exit 1
  fi
  # shellcheck disable=SC2046
  # il sha256sum di Git Bash su Windows scrive «<sha> *<file>» (modo binario): si riporta alla forma a due spazi
  (cd "$radice" && tr '\n' '\0' < "$tmp/scelti" | xargs -0 $(sha_cmd) | sed 's/^\([0-9a-f]\{64\}\) \*/\1  /') > "$tmp/elenco"
  [ "$(wc -l < "$tmp/elenco" | tr -d ' ')" = "$n" ] || { echo "il calcolo delle somme ha perso dei file" >&2; exit 1; }
  mv "$tmp/elenco" "$uscita"
  echo "→ $uscita ($n file)"
}

controlla() {
  local radice="$1" elenco="$2"
  [ -d "$radice" ] || { echo "manca la cartella $radice" >&2; exit 1; }
  [ -s "$elenco" ] || { echo "manca l'elenco $elenco" >&2; exit 1; }
  elenco="$(cd "$(dirname "$elenco")" && pwd)/$(basename "$elenco")"
  # shellcheck disable=SC2046
  (cd "$radice" && $(sha_cmd) -c "$elenco")
}

case "${1:-}" in
  genera) [ $# -ge 3 ] || uso; shift; genera "$@" ;;
  controlla) [ $# -eq 3 ] || uso; controlla "$2" "$3" ;;
  *) uso ;;
esac
