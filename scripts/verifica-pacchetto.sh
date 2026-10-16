#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Verifica i pacchetti di ../build COME LI RICEVE L'UTENTE: li apre in una
# cartella temporanea e controlla contenuto, versione, licenze e (macOS)
# architettura, bundle id e firma. Con --avvio lancia anche l'app: deve
# restare aperta 8 secondi e, chiusa, non lasciare processi. Lo usano le
# prove locali (con --avvio) e GitHub (senza: i runner Linux non hanno un
# display). Esce con errore al primo controllo fallito.
#
#   bash scripts/verifica-pacchetto.sh [--avvio]
set -euo pipefail
cd "$(dirname "$0")/.."
B=../build
AVVIO=0
[ "${1:-}" = "--avvio" ] && AVVIO=1
VERSIONE="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json)"
# Su GitHub il tag deve corrispondere alla versione.
if [[ "${GITHUB_REF_NAME:-}" == v* ]] && [ "$GITHUB_REF_NAME" != "v$VERSIONE" ]; then
  echo "FALLITO: tag $GITHUB_REF_NAME, versione in wails.json $VERSIONE"; exit 1
fi
U="$(mktemp -d "${TMPDIR:-${TEMP:-/tmp}}/ch-verifica-XXXXXX")"
trap 'rm -rf "$U"' EXIT
# L'app avviata scrive la sua configurazione al primo avvio: mai in quella
# vera dell'utente (regole del workspace), anche quando lo script è lanciato a mano e
# non da ci-locale.sh.
CENTRALHUB_CONFIG_DIR="$U/config"
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) CENTRALHUB_CONFIG_DIR="$(cygpath -w "$U/config")" ;; esac
export CENTRALHUB_CONFIG_DIR

no() { echo "FALLITO: $*"; exit 1; }
ok() { echo "ok  $*"; }

# licenze <cartella LICENZE> <eseguibile>: i file principali e una licenza per
# ogni modulo Go che è davvero nel binario (PROBLEMI-NOTI.md §23)
licenze() {
  local L="$1" exe="$2" m
  for f in LICENSE THIRD-PARTY-LICENSES.md moduli-go/ELENCO.txt moduli-go/go/LICENSE; do
    [ -s "$L/$f" ] || no "manca LICENZE/$f"
  done
  for m in $(go version -m "$exe" | awk '$1=="dep"{print $2}'); do
    grep -q "^$m@" "$L/moduli-go/ELENCO.txt" || no "nessuna licenza per $m"
  done
  # Licenze annidate (PROBLEMI-NOTI.md §23): Wails compila codice di altri
  # autori con licenza propria in sottocartelle; il pacchetto deve portarle.
  if grep -q '^github.com/wailsapp/wails/v2@' "$L/moduli-go/ELENCO.txt"; then
    ls -d "$L"/moduli-go/*/annidate >/dev/null 2>&1 || no "nessuna licenza annidata in LICENZE/moduli-go/*/annidate"
  fi
  ok "licenze ($(wc -l < "$L/moduli-go/ELENCO.txt" | tr -d ' ') moduli)"
}

# avvia <comando…>: l'app resta aperta 8 s; poi la si chiude e non deve
# restare nessun processo lanciato da lei.
avvia() {
  "$@" > "$U/avvio.log" 2>&1 &
  local pid=$!
  sleep 8
  kill -0 "$pid" 2>/dev/null || { wait "$pid" || true; no "l'app non resta aperta: $(tail -3 "$U/avvio.log")"; }
  kill "$pid"; wait "$pid" 2>/dev/null || true
  sleep 1
  if pgrep -f "$U" >/dev/null; then pgrep -af "$U"; pkill -f "$U" || true; no "processi rimasti dopo la chiusura"; fi
  ok "avvio: aperta dopo 8 s, nessun processo rimasto"
}

# avvia_linux <comando…>: come avvia, ma l'AppImage lancia l'app vera in un
# processo figlio (da appimage_extracted_*) che non porta $U nella riga di
# comando: chiudere il solo runtime la lascerebbe aperta, e pgrep -f "$U" non
# la vedrebbe. L'app parte quindi in una sessione sua (setsid), che contiene
# runtime, app e processi WebKit; la si chiude con SIGTERM a tutta la sessione
# e dopo al massimo 10 s nella sessione non deve restare niente. La sessione
# la scrive chi la apre: setsid si sdoppia se chi lo lancia è capo di gruppo,
# e allora $! non è la sessione.
avvia_linux() {
  rm -f "$U/sessione"
  setsid bash -c 'echo $$ > "$0/sessione"; exec "$@"' "$U" "$@" > "$U/avvio.log" 2>&1 &
  local pid=$! s n vivi
  sleep 8
  s="$(cat "$U/sessione" 2>/dev/null)" || no "l'app non è partita: $(tail -3 "$U/avvio.log")"
  pgrep -s "$s" >/dev/null || { wait "$pid" || true; no "l'app non resta aperta: $(tail -3 "$U/avvio.log")"; }
  n="$(pgrep -cs "$s")"
  pkill -TERM -s "$s" || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do pgrep -s "$s" >/dev/null || break; sleep 1; done
  if vivi="$(pgrep -as "$s")"; then echo "$vivi"; pkill -KILL -s "$s" || true; no "processi rimasti 10 s dopo la chiusura"; fi
  wait "$pid" 2>/dev/null || true
  ok "avvio: aperta dopo 8 s, chiusa, nessun processo rimasto ($n nella sessione)"
}

verifica_macos() {
  local terna nome arch bid A p got
  for terna in "CentralHub-AppleSilicon:arm64:com.wails.centralhub.arm64" \
               "CentralHub-Intel:x86_64:com.wails.centralhub.intel"; do
    nome="${terna%%:*}"; rest="${terna#*:}"; arch="${rest%%:*}"; bid="${rest##*:}"
    echo "--- $nome.zip"
    ditto -x -k "$B/$nome.zip" "$U/$nome"
    A="$U/$nome/$nome.app"
    [ -d "$A" ] || no "$nome.zip non contiene $nome.app al primo livello"
    p="$A/Contents/Info.plist"
    got="$(lipo -archs "$A/Contents/MacOS/CentralHub")"
    [ "$got" = "$arch" ] || no "$nome è $got, atteso $arch"
    got="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$p")"
    [ "$got" = "$bid" ] || no "bundle id $got, atteso $bid"
    got="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$p")"
    [ "$got" = "$VERSIONE" ] || no "versione $got, attesa $VERSIONE"
    codesign --verify --deep --strict "$A" || no "firma di $nome"
    ok "$arch, $bid, versione $VERSIONE, firma"
    licenze "$A/Contents/Resources/LICENZE" "$A/Contents/MacOS/CentralHub"
    # l'Intel parte con Rosetta
    [ "$AVVIO" = 0 ] || avvia "$A/Contents/MacOS/CentralHub"
  done
}

verifica_linux() {
  local img="$U/CentralHub-Linux-x86_64.AppImage" R
  cp "$B/CentralHub-Linux-x86_64.AppImage" "$img"
  chmod +x "$img"
  (cd "$U" && "$img" --appimage-extract >/dev/null) || no "AppImage non estraibile"
  R="$U/squashfs-root"
  for f in usr/bin/CentralHub centralhub.png CentralHub.desktop \
           usr/share/icons/hicolor/256x256/apps/centralhub.png; do
    [ -s "$R/$f" ] || no "manca $f nell'AppImage"
  done
  ok "eseguibile, icona, .desktop"
  licenze "$R/usr/share/doc/centralhub" "$R/usr/bin/CentralHub"
  if ldd "$R/usr/bin/CentralHub" | grep 'not found'; then no "librerie mancanti"; fi
  ok "librerie"
  [ "$AVVIO" = 0 ] || avvia_linux "$img" --appimage-extract-and-run
}

verifica_windows() {
  local W="$U/CentralHub-Windows" wexe v r
  unzip -q "$B/CentralHub-Windows.zip" -d "$U" || no "zip non leggibile"
  [ -s "$W/CentralHub.exe" ] || no "manca CentralHub.exe nello zip"
  wexe="$(cygpath -w "$W/CentralHub.exe")"
  # Wails scrive le stringhe di versione con lingua 0000: si leggono i campi
  # numerici (PROBLEMI-NOTI.md §28)
  v="$(powershell -NoProfile -Command "\$v=[Diagnostics.FileVersionInfo]::GetVersionInfo('$wexe'); '{0}.{1}.{2}' -f \$v.FileMajorPart,\$v.FileMinorPart,\$v.FileBuildPart" | tr -d '\r')"
  [ "$v" = "$VERSIONE" ] || no "versione dell'exe $v, attesa $VERSIONE"
  ok "exe, versione $v"
  licenze "$W/LICENZE" "$W/CentralHub.exe"
  if [ "$AVVIO" = 1 ]; then
    # Aperta 8 s, poi chiusa come la chiude l'utente (WM_CLOSE alla finestra,
    # così passa da OnShutdown); poi nessun processo del suo albero (figli
    # WebView2 compresi) deve restare vivo.
    cat > "$U/avvio.ps1" <<'PS1'
param([string]$exe)
$p = Start-Process $exe -PassThru
Start-Sleep 8
if ($p.HasExited) { "uscita con codice $($p.ExitCode)"; exit }
$tutti = Get-CimInstance Win32_Process
$albero = @($p.Id); $i = 0
while ($i -lt $albero.Count) {
  $albero += @($tutti | Where-Object { $_.ParentProcessId -eq $albero[$i] } | ForEach-Object { $_.ProcessId })
  $i++
}
$p.Refresh()
if (-not $p.CloseMainWindow()) { Stop-Process -Id $p.Id; "nessuna finestra da chiudere"; exit }
if (-not $p.WaitForExit(10000)) { Stop-Process -Id $p.Id; "non si chiude entro 10 s"; exit }
Start-Sleep 3
$vivi = @($albero | Where-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
if ($vivi.Count) { "processi rimasti: $($vivi -join ' ')"; $vivi | ForEach-Object { Stop-Process -Id $_ -ErrorAction SilentlyContinue }; exit }
"ok $($albero.Count)"
PS1
    r="$(powershell -NoProfile -ExecutionPolicy Bypass -File "$(cygpath -w "$U/avvio.ps1")" "$wexe" | tr -d '\r')"
    [[ "$r" == "ok "* ]] || no "avvio: $r"
    ok "avvio: aperta dopo 8 s, chiusa dalla finestra, nessun processo rimasto (${r#ok } nell'albero)"
  fi
}

case "$(uname -s)" in
  Darwin) verifica_macos ;;
  Linux) verifica_linux ;;
  MINGW*|MSYS*|CYGWIN*) verifica_windows ;;
  *) echo "Piattaforma non riconosciuta"; exit 1 ;;
esac
echo "OK: pacchetti verificati come li riceve l'utente (versione $VERSIONE)"
