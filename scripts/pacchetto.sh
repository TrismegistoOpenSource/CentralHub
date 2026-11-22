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
#   Windows  ../build/CentralHub-Windows.zip (cartella con l'exe)
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

# scarica_fisso <file> <url> <sha256>: scarica una volta e controlla lo SHA-256
# (quello pubblicato nella release su GitHub); un file diverso non si usa mai.
scarica_fisso() {
  local f="$1" url="$2" sha="$3"
  [ -f "$f" ] && [ "$(sha256sum "$f" | cut -d' ' -f1)" = "$sha" ] && return 0
  mkdir -p "$(dirname "$f")"
  curl -fsSL -o "$f.part" "$url"
  if [ "$(sha256sum "$f.part" | cut -d' ' -f1)" != "$sha" ]; then
    rm -f "$f.part"
    echo "!!! $(basename "$f") scaricato con SHA-256 diverso da $sha" >&2
    return 1
  fi
  chmod +x "$f.part"
  mv "$f.part" "$f"
}

pacchetto_linux() {
  # appimagetool e runtime dell'AppImage a versione fissa. Si aggiornano quando
  # loro sono avanti di due versioni minor (STRUMENTI-CLAUDE/controlla-appimage.sh):
  # versione, SHA-256 e licenses/appimage-runtime.txt. Si tengono in
  # ~/.local/bin, scaricati la prima volta (sui runner ogni volta).
  local tool="$HOME/.local/bin/appimagetool-1.9.1-x86_64.AppImage"
  local runtime="$HOME/.local/bin/appimage-runtime-20251108-x86_64"
  scarica_fisso "$tool" \
    https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage \
    ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0
  scarica_fisso "$runtime" \
    https://github.com/AppImage/type2-runtime/releases/download/20251108/runtime-x86_64 \
    2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d
  local lavoro A
  lavoro="$(mktemp -d "${TMPDIR:-/tmp}/ch-appimage-XXXXXX")"
  A="$lavoro/CentralHub.AppDir"
  mkdir -p "$A/usr/bin" "$A/usr/share/icons/hicolor/256x256/apps"
  cp "$B/CentralHub-Linux-amd64/CentralHub" "$A/usr/bin/"
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
  ARCH=x86_64 "$tool" --appimage-extract-and-run --runtime-file "$runtime" "$A" "$B/CentralHub-Linux-x86_64.AppImage"
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
