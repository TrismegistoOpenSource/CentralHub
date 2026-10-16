#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# IDENTICO IN OGNI PROGETTO (procedura di test del workspace,
# "Test e build"). Le parti del progetto stanno in scripts/progetto.conf,
# scripts/build.sh, scripts/pacchetto.sh e scripts/verifica-pacchetto.sh.
#
# Ogni macchina prova il PROPRIO sistema sul sorgente del workspace, così
# com'è (senza commit né bundle):
#
#   bash scripts/ci-locale.sh          dalla cartella sourcecode del workspace
#
# Cosa fa:
#   1. calcola l'impronta del sorgente (scripts/impronta.sh) e ne copia i file
#      in una cartella temporanea fuori da MEGAsync: le build non sporcano il
#      workspace e si prova esattamente ciò che l'impronta descrive;
#   2. nella copia: pacchetti di sistema, build (scripts/build.sh), go vet,
#      gofmt, go test -race, pacchetto (scripts/pacchetto.sh) e verifica del
#      pacchetto come lo riceve l'utente, avvio compreso
#      (scripts/verifica-pacchetto.sh --avvio);
#   3. se tutto è verde, consegna gli artefatti in ../build del workspace con
#      l'attestato ../build/CI-<sistema>.txt (impronta, versione, esito,
#      SHA-256 degli artefatti);
#   4. ricalcola l'impronta del workspace: se il sorgente è cambiato durante
#      la prova, l'attestato non vale e non viene consegnato.
#
# Con i tre attestati verdi sulla stessa impronta il Mac fa il commit e la
# pubblicazione (scripts/pubblica-release.sh).

set -u -o pipefail
cd "$(dirname "$0")/.." || exit 1
# shellcheck source-path=SCRIPTDIR source=progetto.conf
source scripts/progetto.conf

case "$(uname -s)" in
  Darwin) SISTEMA=macos ;;
  Linux) SISTEMA=linux ;;
  MINGW*|MSYS*|CYGWIN*) SISTEMA=windows ;;
  *) echo "sistema non riconosciuto"; exit 1 ;;
esac

# --- 1. dal workspace: copia e ripartenza nella copia ----------------------
if [ "${1:-}" != "--nella-copia" ]; then
  WS="$(pwd -P)"
  IMPRONTA="$(bash scripts/impronta.sh)"
  case "$SISTEMA" in
    linux) BASE="${XDG_CACHE_HOME:-$HOME/.cache}/ci-locale" ;;   # /tmp della VM Linux è in RAM
    # %TEMP% arriva come C:\...: il tar di Git Bash legge "C:" come un host
    # remoto, quindi il percorso va in forma /c/...
    windows) BASE="$(cygpath -u "${TEMP:-/tmp}")/ci-locale" ;;
    macos) BASE="${TMPDIR:-/tmp}/ci-locale" ;;
  esac
  # la copia ricalca lo schema del workspace (<versione>/sourcecode e
  # <versione>/build), così i percorsi ../build degli script restano validi
  C="$BASE/$NOME/$(basename "$(dirname "$WS")")"
  rm -rf "$C"
  mkdir -p "$C/sourcecode" "$C/build"
  bash scripts/impronta.sh --elenco | tar -cf - -T - | tar -xf - -C "$C/sourcecode" \
    || { echo "copia del sorgente fallita"; exit 1; }
  echo "impronta $IMPRONTA, copia in $C"
  exec bash "$C/sourcecode/scripts/ci-locale.sh" --nella-copia "$WS" "$IMPRONTA"
fi
WS="$2"
IMPRONTA="$3"

PROVA="$(cd .. && pwd)/prova"
mkdir -p "$PROVA"
LOG="$PROVA/ci-locale-$SISTEMA.log"
exec > >(tee "$LOG") 2>&1
export "$CONFIG_VAR=$PROVA/config"
# sulla VM Linux di prova /tmp è in RAM (1 GB): i t.TempDir() dei test (centinaia di MB in
# RsyncGUI ed Encryptor) e l'AppImage estratta dalla verifica lo riempivano
# fino all'OOM dei test -race. Su Linux i temporanei stanno nella prova, su disco.
if [ "$SISTEMA" = linux ]; then mkdir -p "$PROVA/tmp"; export TMPDIR="$PROVA/tmp"; fi
export PATH="$HOME/.local/go/bin:/c/Program Files/Go/bin:${GOPATH:-$HOME/go}/bin:$HOME/.local/node/bin:$PATH"
if [ "$SISTEMA" = windows ] && ! command -v gcc >/dev/null; then
  for d in "${LOCALAPPDATA:-}"/Microsoft/WinGet/Packages/BrechtSanders.WinLibs*/mingw64/bin; do
    if [ -x "$d/gcc.exe" ]; then PATH="$PATH:$(cygpath -u "$d")"; export PATH; break; fi
  done
fi

esito=0
passo() { echo; echo "=================== $*"; }
fallito() { echo "!!! FALLITO: $*"; esito=1; }
# Le cartelle che le build creano nel sorgente si tolgono sempre: la copia
# resta pulita per un'eventuale seconda prova.
# shellcheck disable=SC2317,SC2329  # chiamata dalla trap (codice per shellcheck 0.10 e 0.11)
pulizia() { rm -rf build frontend/dist frontend/wailsjs frontend/node_modules frontend/package.json.md5; }
trap pulizia EXIT

tag_var="GO_TAGS_$SISTEMA"; TAGS="${!tag_var:-}"
# NOCGO_<sistema>=1 in progetto.conf: vet e test senza cgo, quindi senza
# -race, che cgo lo pretende. Serve quando con cgo un pacchetto vuole header
# che il sistema non ha e che il binario spedito non usa (Encryptor su
# Windows: cgofuse con cgo cerca gli header di WinFsp, l'exe è senza cgo).
nocgo_var="NOCGO_$SISTEMA"; NOCGO="${!nocgo_var:-}"
art_var="ARTEFATTI_$SISTEMA"; ARTEFATTI="${!art_var}"
# ${TAGARG[@]+...}: con set -u il bash 3.2 del Mac rifiuta un array vuoto
TAGARG=()
[ -z "$TAGS" ] || TAGARG=(-tags "$TAGS")

passo "Sistema ($SISTEMA)"
date
uname -a
go version || { fallito "Go non trovato"; exit 1; }
# niente "| head" con pipefail: Wails scrive anche un messaggio dopo la
# versione e, a pipe chiusa, esce per SIGPIPE (PROBLEMI-NOTI.md §29)
if v="$(wails version 2>/dev/null)"; then echo "wails ${v%%$'\n'*}"; else fallito "Wails non trovato"; fi
VERSIONE="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json)"
echo "progetto: $NOME $VERSIONE"
echo "impronta: $IMPRONTA"
echo "workspace: $WS"

passo "Copia fedele al workspace"
if [ "$(bash scripts/impronta.sh)" = "$IMPRONTA" ]; then
  echo "ok: la copia ha la stessa impronta"
else
  fallito "la copia non corrisponde al workspace"
  exit 1
fi

if [ "$SISTEMA" = linux ] && [ -n "${APT_linux:-}" ]; then
  passo "Pacchetti di sviluppo"
  mancano=""
  for p in $APT_linux; do
    dpkg -s "$p" >/dev/null 2>&1 || mancano="$mancano $p"
  done
  if [ -n "$mancano" ]; then
    fallito "mancano:$mancano"
    echo "lanciare a mano:  sudo apt-get install -y$mancano"
    exit 1
  fi
fi
if [ "$SISTEMA" = windows ]; then
  if v="$(gcc --version 2>/dev/null)"; then echo "${v%%$'\n'*}"; else fallito "gcc non trovato: -race non disponibile"; fi
fi

passo "Build (scripts/build.sh)"
bash scripts/build.sh || fallito "build"

passo "go vet"
if [ "$NOCGO" = 1 ]; then
  CGO_ENABLED=0 go vet ${TAGARG[@]+"${TAGARG[@]}"} ./... || fallito "go vet"
else
  go vet ${TAGARG[@]+"${TAGARG[@]}"} ./... || fallito "go vet"
fi

passo "gofmt"
f="$(gofmt -l . 2>&1)"
if [ -n "$f" ]; then echo "$f"; fallito "file non formattati"; fi

if [ "$NOCGO" = 1 ]; then
  passo "go test (senza cgo: NOCGO_$SISTEMA in progetto.conf, quindi senza -race)"
  CGO_ENABLED=0 go test -count=1 ${TAGARG[@]+"${TAGARG[@]}"} ./... || fallito "go test"
else
  passo "go test -race"
  # -race vuole cgo anche su Windows (gcc), dove il binario spedito ne fa a meno
  CGO_ENABLED=1 go test -count=1 -race ${TAGARG[@]+"${TAGARG[@]}"} ./... || fallito "go test"
fi

passo "Pacchetto (scripts/pacchetto.sh)"
bash scripts/pacchetto.sh || fallito "pacchetto"

passo "Verifica del pacchetto come lo riceve l'utente, con l'avvio"
if [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] || [ "$SISTEMA" != linux ]; then
  bash scripts/verifica-pacchetto.sh --avvio || fallito "verifica del pacchetto"
else
  bash scripts/verifica-pacchetto.sh || fallito "verifica del pacchetto"
  fallito "nessun display: l'avvio non si può provare (lanciare dalla sessione grafica)"
fi

passo "Sorgente del workspace invariato durante la prova"
ADESSO="$(bash "$WS/scripts/impronta.sh")"
if [ "$ADESSO" = "$IMPRONTA" ]; then
  echo "ok"
else
  fallito "il sorgente del workspace è cambiato durante la prova ($ADESSO): rifare la prova"
fi

passo "Attestato e consegna"
sha() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
ATT="../build/CI-$SISTEMA.txt"
if [ "$esito" = 0 ]; then
  {
    echo "sistema:  $SISTEMA"
    echo "impronta: $IMPRONTA"
    echo "versione: $VERSIONE"
    echo "data:     $(date '+%d/%m/%Y %H:%M')"
    echo "go:       $(go env GOVERSION)"
    echo "esito:    TUTTO OK"
    # shellcheck disable=SC2086
    (cd ../build && sha $ARTEFATTI | sed 's/ \*/  /')
  } > "$ATT"
  cat "$ATT"
  D="$WS/../build"
  mkdir -p "$D"
  consegnato=1
  for a in $ARTEFATTI; do cp "../build/$a" "$D/" || consegnato=0; done
  cp "$ATT" "$D/" || consegnato=0
  # shellcheck disable=SC2086
  if [ "$consegnato" = 1 ] && (cd "$D" && sha $ARTEFATTI | sed 's/ \*/  /' | diff -q - <(grep -E '^[0-9a-f]{64} ' "CI-$SISTEMA.txt") >/dev/null); then
    echo "consegnati in $(cd "$D" && pwd)"
  else
    fallito "consegna nel workspace"
    rm -f "$D/CI-$SISTEMA.txt"
  fi
else
  rm -f "$ATT" "$WS/../build/CI-$SISTEMA.txt"
  echo "niente consegna: ci sono passi falliti (un attestato vecchio è stato tolto)"
fi

passo "Esito"
if [ "$esito" = 0 ]; then echo "TUTTO OK"; else echo "CI SONO PASSI FALLITI (cerca '!!! FALLITO')"; fi
echo "log completo: $LOG"
exit "$esito"
