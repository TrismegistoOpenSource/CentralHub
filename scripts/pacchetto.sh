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
set -euo pipefail
cd "$(dirname "$0")/.."
B=../build

pacchetto_macos() {
  for nome in CentralHub-AppleSilicon CentralHub-Intel; do
    rm -f "$B/$nome.zip"
    # --keepParent obbligatorio: senza, lo zip contiene Contents/ e non l'app
    ditto -c -k --sequesterRsrc --keepParent "$B/$nome.app" "$B/$nome.zip"
    echo "→ $B/$nome.zip"
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
