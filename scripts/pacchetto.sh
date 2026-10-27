#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Confeziona ciò che scripts/build.sh ha prodotto in ../build nel pacchetto
# che riceve l'utente, per il sistema su cui gira. Lo usano le prove locali
# (scripts/ci-locale.sh) e GitHub (.github/workflows/build.yml): stessi
# comandi, stesso risultato.
#
#   macOS    ../build/CentralHub-AppleSilicon.zip, CentralHub-Intel.zip
#   Linux    ../build/CentralHub-Linux-x86_64.AppImage
#   Windows  ../build/CentralHub-Windows.zip (cartella con exe e LICENZE)
#
# Per l'autoverifica (internal/autoverifica) ogni sistema consegna anche
# ../build/INTEGRITA-<sistema>.txt: lo SHA-256 di ogni file che l'utente riceve,
# eseguibile compreso. Va fra gli asset della release, quindi lo elenca il
# SHA256SUMS.txt firmato. Sul pacchetto Linux e Windows lo stesso elenco sta anche
# dentro (INTEGRITA.txt); su macOS l'elenco dentro l'app lo scrive build.sh prima
# della firma del sistema e non contiene l'eseguibile.
set -euo pipefail
cd "$(dirname "$0")/.."
B=../build

pacchetto_macos() {
  local nome sistema
  for nome in CentralHub-AppleSilicon CentralHub-Intel; do
    rm -f "$B/$nome.zip"
    # --keepParent obbligatorio: senza, lo zip contiene Contents/ e non l'app
    ditto -c -k --sequesterRsrc --keepParent "$B/$nome.app" "$B/$nome.zip"
    echo "→ $B/$nome.zip"
    # elenco completo, dell'app già firmata (eseguibile e _CodeSignature compresi)
    case "$nome" in *AppleSilicon) sistema=macos-arm64 ;; *) sistema=macos-amd64 ;; esac
    bash scripts/integrita-pacchetto.sh genera "$B/$nome.app" "$B/INTEGRITA-$sistema.txt"
  done
}

pacchetto_linux() {
  # appimagetool: quello indicato da APPIMAGETOOL, altrimenti si tiene in
  # ~/.local/bin, scaricato la prima volta (sui runner ogni volta).
  local tool="${APPIMAGETOOL:-$HOME/.local/bin/appimagetool-x86_64.AppImage}"
  local url="https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage"
  if [ ! -x "$tool" ]; then
    mkdir -p "$(dirname "$tool")"
    curl -fsSL -o "$tool.part" "$url"
    chmod +x "$tool.part"
    mv "$tool.part" "$tool"
  fi
  local lavoro A
  lavoro="$(mktemp -d "${TMPDIR:-/tmp}/ch-appimage-XXXXXX")"
  A="$lavoro/CentralHub.AppDir"
  mkdir -p "$A/usr/bin" "$A/usr/share/icons/hicolor/256x256/apps"
  cp "$B/CentralHub-Linux-amd64/CentralHub" "$A/usr/bin/"
  mkdir -p "$A/usr/share/doc"
  cp -R "$B/CentralHub-Linux-amd64/LICENZE" "$A/usr/share/doc/centralhub"
  cp assets/appicon.png "$A/centralhub.png"
  cp assets/appicon.png "$A/usr/share/icons/hicolor/256x256/apps/centralhub.png"
  cat > "$A/CentralHub.desktop" <<'DESKTOP'
[Desktop Entry]
Name=CentralHub
Exec=CentralHub
Icon=centralhub
Type=Application
Categories=Utility;
StartupWMClass=CentralHub
DESKTOP
  ln -s usr/bin/CentralHub "$A/AppRun"
  # elenco di integrità (internal/autoverifica): dentro la AppDir e fra gli asset della release
  bash scripts/integrita-pacchetto.sh genera "$A" "$A/INTEGRITA.txt"
  cp "$A/INTEGRITA.txt" "$B/INTEGRITA-linux-amd64.txt"
  # --appimage-extract-and-run: i runner non hanno FUSE.
  rm -f "$B/CentralHub-Linux-x86_64.AppImage"
  ARCH=x86_64 "$tool" --appimage-extract-and-run "$A" "$B/CentralHub-Linux-x86_64.AppImage"
  rm -rf "$lavoro"
  echo "→ $B/CentralHub-Linux-x86_64.AppImage"
}

pacchetto_windows() {
  # Git Bash non ha zip: il tar di Windows (bsdtar) sceglie il formato
  # dall'estensione (PROBLEMI-NOTI.md §28).
  rm -f "$B/CentralHub-Windows.zip"
  # elenco di integrità (internal/autoverifica): dentro la cartella e fra gli asset della release
  bash scripts/integrita-pacchetto.sh genera "$B/CentralHub-Windows" "$B/CentralHub-Windows/INTEGRITA.txt"
  cp "$B/CentralHub-Windows/INTEGRITA.txt" "$B/INTEGRITA-windows-amd64.txt"
  /c/Windows/System32/tar.exe -a -c -f "$(cygpath -w "$PWD/$B/CentralHub-Windows.zip")" \
    -C "$(cygpath -w "$PWD/$B")" CentralHub-Windows
  echo "→ $B/CentralHub-Windows.zip"
}

case "$(uname -s)" in
  Darwin) pacchetto_macos ;;
  Linux) pacchetto_linux ;;
  MINGW*|MSYS*|CYGWIN*) pacchetto_windows ;;
  *) echo "Piattaforma non riconosciuta"; exit 1 ;;
esac
