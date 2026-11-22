#!/bin/bash
# SPDX-License-Identifier: MIT
# Compila CentralHub per il sistema su cui gira. Lo usano sia le prove
# locali (scripts/ci-locale.sh) sia GitHub (.github/workflows/build.yml): un
# solo modo di compilare, così un attestato locale verde vale anche per GitHub.
#
#   macOS    ../build/CentralHub-AppleSilicon.app e CentralHub-Intel.app
#            (due app ad architettura pura, bundle id e nome distinti, rifirmate)
#   Linux    ../build/CentralHub-Linux-amd64/CentralHub
#   Windows  ../build/CentralHub-Windows/CentralHub.exe
#
# Le licenze stanno DENTRO il programma (internal/licenze, clic sul numero di versione): nessuna cartella di licenze accanto.
#
# Il pacchetto da pubblicare (zip, AppImage) lo fa scripts/pacchetto.sh.
#
# Schema del workspace: sourcecode/ e build/ restano separati. Wails pretende
# una build/ nella root del progetto: la si crea al volo da assets/ e la si
# cancella sempre a fine script (trap). frontend/wailsjs invece resta,
# perché go vet e go test ne hanno bisogno (binding):
# li toglie chi chiama, a fine giro. L'interfaccia di CentralHub (frontend/ui) è
# HTML/CSS/JS statico, senza npm.
set -euo pipefail
cd "$(dirname "$0")/.."

export PATH="$HOME/.local/go/bin:${GOPATH:-$HOME/go}/bin:$PATH"

WAILS_VERSION="v2.13.0"
if ! command -v wails >/dev/null; then
  echo "Wails CLI non trovato. Installa con:"
  echo "  go install github.com/wailsapp/wails/v2/cmd/wails@${WAILS_VERSION}"
  exit 1
fi

OUT_DIR="../build"
mkdir -p "${OUT_DIR}"

# Le licenze nell'app (clic sul numero di versione) sono sempre quelle di ciò che finisce nel programma: si raccolgono a ogni build in
# internal/licenze/testi/raccolta (incorporate con go:embed) e si tolgono dopo, MA solo se le ha create questo script (ci-locale.sh le
# prepara una volta per tutto il giro). La build si ferma se una libreria non ha il suo testo (cancello di raccogli-licenze.sh).
LIC_GIA=0; [ -d internal/licenze/testi/raccolta ] && LIC_GIA=1
cleanup() {
  rm -rf build
  [ "$LIC_GIA" = 1 ] || rm -rf internal/licenze/testi/raccolta
}
trap cleanup EXIT
[ "$LIC_GIA" = 1 ] || bash scripts/raccogli-licenze.sh internal/licenze/testi/raccolta
rm -rf build
mkdir -p build
cp -R assets/. build/

# build_mac <platform> <nome .app> <bundle id> <min macOS>
build_mac() {
  local platform="$1" appname="$2" bundleid="$3" minos="$4"
  local dest="${OUT_DIR}/${appname}.app"
  echo; echo "▸ ${appname} (${platform})"
  wails build -platform "${platform}"
  rm -rf "${dest}"
  cp -R build/bin/CentralHub.app "${dest}"
  local plist="${dest}/Contents/Info.plist"
  /usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier ${bundleid}" "${plist}"
  /usr/libexec/PlistBuddy -c "Set :CFBundleName ${appname}" "${plist}"
  /usr/libexec/PlistBuddy -c "Set :LSMinimumSystemVersion ${minos}" "${plist}"
  # Elenco di integrità per l'autoverifica (internal/autoverifica), dentro l'app e PRIMA della firma
  # (sta tra le risorse sigillate). Non può contenere l'eseguibile né _CodeSignature: la firma li cambia.
  # Li elenca l'elenco della release, completo (INTEGRITA-macos-*.txt, scripts/pacchetto.sh).
  bash scripts/integrita-pacchetto.sh genera "${dest}" "${dest}/Contents/Resources/INTEGRITA.txt" \
    "Contents/MacOS/CentralHub" "Contents/_CodeSignature/"
  # Modificare l'Info.plist invalida la firma di Wails: va rifirmata, altrimenti
  # macOS rifiuta di aprire l'app ("è danneggiata").
  codesign --force --deep --sign - "${dest}" 2>/dev/null
  codesign --verify --deep "${dest}" && echo "  firma:        ok"
  echo "  architettura: $(lipo -archs "${dest}/Contents/MacOS/CentralHub")"
  echo "  bundle id:    $(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "${plist}")"
  echo "  → ${dest}"
}

# Linux: Wails usa cgo e WebKitGTK. Servono libgtk-3-dev e
# libwebkit2gtk-4.1-dev; il tag webkit2_41 evita che pkg-config cerchi la 4.0,
# che non esiste più (PROBLEMI-NOTI.md §1).
build_linux() {
  local dest="${OUT_DIR}/CentralHub-Linux-amd64"
  echo; echo "▸ Linux (linux/amd64)"
  wails build -platform linux/amd64 -tags webkit2_41
  rm -rf "${dest}"; mkdir -p "${dest}"
  cp build/bin/CentralHub "${dest}/CentralHub"
  echo "  → ${dest}/CentralHub"
}

# Windows: exe portable senza cgo (go-webview2 non ne ha bisogno, niente gcc
# sul runner).
build_windows() {
  local dest="${OUT_DIR}/CentralHub-Windows"
  echo; echo "▸ Windows (windows/amd64)"
  CGO_ENABLED=0 wails build -platform windows/amd64
  rm -rf "${dest}"; mkdir -p "${dest}"
  cp build/bin/CentralHub.exe "${dest}/CentralHub.exe"
  echo "  → ${dest}/CentralHub.exe"
}

case "$(uname -s)" in
  Darwin)
    build_mac "darwin/arm64" "CentralHub-AppleSilicon" "com.wails.centralhub.arm64" "11.0"
    build_mac "darwin/amd64" "CentralHub-Intel"        "com.wails.centralhub.intel" "10.13.0"
    ;;
  Linux) build_linux ;;
  MINGW*|MSYS*|CYGWIN*) build_windows ;;
  *) echo "Piattaforma non riconosciuta"; exit 1 ;;
esac
echo; echo "Fatto. Output in ${OUT_DIR}/"
