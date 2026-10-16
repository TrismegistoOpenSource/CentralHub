#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# IDENTICO IN OGNI PROGETTO (procedura di test del workspace,
# "Test e build"). Gira SOLO sul Mac, nella cartella sourcecode del workspace
# (che lì è il repository git).
#
#   bash scripts/pubblica-release.sh            controlla e basta
#   bash scripts/pubblica-release.sh --commit   controlla, poi fa il commit della
#                                               versione e il tag v<versione>, in locale
#   bash scripts/pubblica-release.sh --push     controlla il commit fatto, poi push
#                                               del ramo e del tag: LO LANCIA L'UTENTE
#
# Il cancello: i tre attestati ../build/CI-macos.txt, CI-linux.txt e
# CI-windows.txt (scritti da scripts/ci-locale.sh solo se tutto è verde)
# devono avere l'impronta del sorgente di ADESSO, la versione di wails.json e
# artefatti con gli SHA-256 dichiarati (questo dice anche che MEGAsync ha
# finito). Il commit deve contenere esattamente i file dell'impronta: niente
# dimenticato, niente in più.
#
# Una versione = un solo commit sopra quello già pubblicato. Se il commit
# della versione c'è già ma non è pubblicato (una prova rifatta dopo una
# correzione), --commit lo rifà con --amend.
#
# Prima di --commit il Mac ricontrolla le questioni aperte (regole del workspace, "Test
# e build"). Il push non lo fa il Mac: --commit stampa il comando, il Mac lo
# dà all'utente in chat e l'utente lo lancia (regole del workspace, "Git: mai pubblicare
# di iniziativa"). Il push del tag avvia .github/workflows/build.yml: build
# sui runner e release, un solo run.

set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source-path=SCRIPTDIR source=progetto.conf
source scripts/progetto.conf
MODO="${1:-}"
case "$MODO" in ""|--commit|--push) ;; *) echo "uso: $0 [--commit|--push]"; exit 2 ;; esac

VERSIONE="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json)"
TAG="v$VERSIONE"
B=../build
AUTORE_NOME="TrismegistoOpenSource"
AUTORE_MAIL="TrismegistoOpenSource@users.noreply.github.com"
errori=0
no() { echo "NO     $*"; errori=1; }
sha() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

IMPRONTA="$(bash scripts/impronta.sh)"
echo "$NOME $VERSIONE, impronta del sorgente $IMPRONTA"

# --- i tre attestati -------------------------------------------------------
for sistema in macos linux windows; do
  att="$B/CI-$sistema.txt"
  if [ ! -s "$att" ]; then no "manca l'attestato di $sistema ($att)"; continue; fi
  campo() { sed -n "s/^$1: *//p" "$att"; }
  [ "$(campo impronta)" = "$IMPRONTA" ] || { no "$sistema ha provato un altro sorgente ($(campo impronta)): va rifatta la prova"; continue; }
  [ "$(campo esito)" = "TUTTO OK" ] || { no "$sistema: esito '$(campo esito)'"; continue; }
  [ "$(campo versione)" = "$VERSIONE" ] || { no "$sistema: versione '$(campo versione)'"; continue; }
  var="ARTEFATTI_$sistema"
  for a in ${!var}; do
    h="$(grep -E "^[0-9a-f]{64}  $a\$" "$att" | cut -d' ' -f1 || true)"
    if [ -z "$h" ]; then no "$sistema: $a non è nell'attestato"
    elif [ ! -f "$B/$a" ]; then no "$sistema: $a non è in $B"
    elif [ "$(sha "$B/$a")" != "$h" ]; then no "$sistema: $a diverso da quello attestato (MEGAsync non ha finito?)"
    fi
  done
  echo "ok     $sistema: $(campo data), $(campo go)"
done

# --- git ---------------------------------------------------------------------
# I file del commit, con la stessa regola dell'impronta: fuori i file e le
# cartelle con il punto, tranne .github/.
file_del_commit() {
  git ls-files "$@" | LC_ALL=C awk -F/ '{ for (i = 1; i <= NF; i++) if ($i ~ /^\./ && $i != ".github") next; print }' | LC_ALL=C sort
}

RAMO="$(git branch --show-current)"
[ -n "$RAMO" ] || no "nessun ramo (HEAD staccato)"
git remote get-url origin >/dev/null 2>&1 || no "manca il remoto origin"
REMOTO="$(git ls-remote origin "refs/heads/$RAMO" | cut -f1)"
[ -z "$(git ls-remote --tags origin "refs/tags/$TAG")" ] || no "il tag $TAG è già pubblicato: serve una versione nuova"
HEAD_SHA="$(git rev-parse -q --verify HEAD || true)"

# Da dove parte il commit della versione: AMEND se HEAD è già un commit della
# versione non pubblicato, altrimenti un commit nuovo sopra quello pubblicato.
BASE=nuovo
if [ -n "$REMOTO" ]; then
  if [ "$HEAD_SHA" = "$REMOTO" ]; then BASE=nuovo
  elif [ -n "$HEAD_SHA" ] && [ "$(git rev-parse -q --verify HEAD^ || true)" = "$REMOTO" ]; then BASE=amend
  else no "HEAD non è né origin/$RAMO né un commit sopra: sistemare a mano (con backup: git bundle create … --all)"
  fi
else
  # primo push: la storia pubblicata deve essere un commit solo
  if [ -z "$HEAD_SHA" ]; then BASE=nuovo
  elif [ "$(git rev-list --count HEAD)" = 1 ]; then BASE=amend
  else no "primo push con $(git rev-list --count HEAD) commit: la storia deve essere uno solo"
  fi
fi

if [ "$MODO" = --push ]; then
  [ -z "$(git status --porcelain)" ] || no "ci sono modifiche non committate: prima --commit"
  [ "$(git rev-parse -q --verify "refs/tags/$TAG^{commit}" || true)" = "$HEAD_SHA" ] || no "il tag $TAG non punta a HEAD: prima --commit"
  [ "$BASE" = amend ] || no "HEAD non è il commit della versione: prima --commit"
  if ! diff <(file_del_commit) <(bash scripts/impronta.sh --elenco) >/dev/null; then
    no "i file del commit non sono quelli provati"
  fi
fi

[ "$errori" = 0 ] || { echo; echo "NON pronto."; exit 1; }

case "$MODO" in
  "")
    echo; echo "Pronto: tre attestati verdi sul sorgente di adesso."
    echo "Prossimo passo:  bash scripts/pubblica-release.sh --commit"
    ;;
  --commit)
    git add -A
    if ! diff <(file_del_commit --cached) <(bash scripts/impronta.sh --elenco); then
      echo "NO     i file del commit (<) non sono quelli provati (>): .gitignore o file dimenticati"
      git reset -q
      exit 1
    fi
    if [ "$BASE" = amend ]; then
      git -c user.name="$AUTORE_NOME" -c user.email="$AUTORE_MAIL" \
        commit -q --amend --reset-author -m "$NOME $VERSIONE"
    else
      git -c user.name="$AUTORE_NOME" -c user.email="$AUTORE_MAIL" \
        commit -q -m "$NOME $VERSIONE"
    fi
    git tag -f "$TAG" >/dev/null
    [ -z "$(git status --porcelain)" ] || { echo "NO     dopo il commit restano modifiche"; exit 1; }
    echo "commit $(git rev-parse --short HEAD) «$NOME $VERSIONE» e tag $TAG, in locale."
    echo "Comando di push da dare all'utente (lo lancia lui):"
    echo "  cd '$(pwd)' && bash scripts/pubblica-release.sh --push"
    ;;
  --push)
    git push origin "$RAMO"
    git push origin "$TAG"
    echo "push fatto: il workflow del tag $TAG builda e pubblica la release"
    echo "controllo: gh run list --limit 3"
    ;;
esac
