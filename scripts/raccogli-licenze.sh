#!/bin/bash
# SPDX-License-Identifier: MIT
# Raccoglie in una cartella i testi di licenza che devono viaggiare CON l'app.
#
# Uso: scripts/raccogli-licenze.sh <cartella-di-destinazione>
# (dalla root del sorgente; lo chiamano scripts/build.sh e la CI)
#
# Perché uno script e non un elenco a mano: MIT, BSD, ISC e Apache chiedono
# che il loro testo accompagni il programma distribuito, e il binario Go
# incorpora non solo le dipendenze dirette ma anche quelle che si porta
# dietro Wails (echo, gorilla/websocket, uniseg...). Un elenco scritto a mano
# era incompleto in tutti i progetti del workspace, e fino al 25/09/2026
# nessuna build copiava nemmeno quello (i README dicevano il contrario).
#
# Cosa finisce nella cartella:
#   LICENSE, THIRD-PARTY-*.md      licenza dell'app ed elenco ragionato
#   licenses/                      testi scritti a mano (componenti non Go,
#                                  es. ffmpeg e le sue librerie)
#   moduli-go/<modulo>/            LICENSE/COPYING/NOTICE di ogni modulo Go
#                                  che finisce nel binario, per macOS, Linux
#                                  e Windows (l'unione: un testo in più non
#                                  fa danni, uno in meno sì)
#   moduli-go/<modulo>/annidate/   licenze che stanno in una SOTTOCARTELLA del
#                                  modulo, di codice di altri autori compilato
#                                  nel binario (Wails: winc, go-common-file-
#                                  dialog, typescriptify; go-webview2:
#                                  webviewloader). PROBLEMI-NOTI.md §23
#   moduli-go/ELENCO.txt           modulo@versione per ciascuno
#   moduli-js/                     licenze dei pacchetti npm di produzione
#                                  compilati nel frontend (se ce ne sono)
#
# È un CANCELLO: se un modulo non ha nessun file di licenza la build fallisce,
# invece di distribuire un'app con un obbligo non rispettato.
set -euo pipefail
cd "$(dirname "$0")/.."

DEST="${1:?uso: $0 <cartella-di-destinazione>}"
rm -rf "$DEST"
mkdir -p "$DEST/moduli-go"

cp LICENSE "$DEST/"
for f in THIRD-PARTY-*.md; do
  [ -f "$f" ] && cp "$f" "$DEST/"
done
if [ -d licenses ]; then
  mkdir -p "$DEST/licenses"
  cp licenses/* "$DEST/licenses/"
fi

# Senza i sorgenti dei moduli in cache non ci sono i file da copiare.
go mod download

# Libreria standard e runtime di Go: stanno nel binario anche loro.
mkdir -p "$DEST/moduli-go/go"
cp "$(go env GOROOT)/LICENSE" "$DEST/moduli-go/go/LICENSE"
[ -f "$(go env GOROOT)/PATENTS" ] && cp "$(go env GOROOT)/PATENTS" "$DEST/moduli-go/go/PATENTS"

# I moduli cambiano per sistema (go-webview2 solo su Windows, godbus solo su
# Linux...): si prende l'unione. go list non compila nulla, quindi funziona
# per qualunque GOOS da qualunque macchina. desktop e production sono i tag
# che `wails build` passa sempre (senza, go list vede un altro insieme di
# pacchetti); webkit2_41 è quello della build Linux.
MODULI=$(for os in darwin linux windows; do
  CGO_ENABLED=1 GOOS=$os GOARCH=amd64 go list -deps -tags desktop,production,webkit2_41 \
    -f '{{with .Module}}{{if not .Main}}{{with .Replace}}{{.Path}}@{{.Version}}|{{.Dir}}{{else}}{{$.Module.Path}}@{{$.Module.Version}}|{{$.Module.Dir}}{{end}}{{end}}{{end}}' .
done | sort -u)

SENZA_LICENZA=""
: > "$DEST/moduli-go/ELENCO.txt"
while IFS='|' read -r mod dir; do
  [ -n "$mod" ] || continue
  echo "$mod" >> "$DEST/moduli-go/ELENCO.txt"
  nome="${mod%@*}"
  out="$DEST/moduli-go/$(echo "$nome" | tr '/' '_')"
  mkdir -p "$out"
  trovati=0
  for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE* "$dir"/UNLICENSE* "$dir"/PATENTS*; do
    if [ -f "$f" ]; then
      cp "$f" "$out/"
      trovati=1
    fi
  done
  [ "$trovati" = 1 ] || SENZA_LICENZA="$SENZA_LICENZA $mod"
done <<< "$MODULI"

# Licenze ANNIDATE: alcuni moduli contengono, in una sottocartella, codice di
# un altro autore con la sua licenza. Se quel pacchetto finisce nel binario, il
# testo deve viaggiare come gli altri (MIT, ISC e Apache lo chiedono anche per
# le copie in forma binaria). Si guardano solo le cartelle dei pacchetti davvero
# compilati, dalla loro fino alla radice del modulo (esclusa: quella l'ha già
# copiata il ciclo qui sopra). Trovato il 17/10/2026: il solo giro sulla radice
# lasciava fuori quattro licenze dai pacchetti Windows di tutte le app Wails.
for os in darwin linux windows; do
  CGO_ENABLED=1 GOOS=$os GOARCH=amd64 go list -e -deps -tags desktop,production,webkit2_41 \
    -f '{{if .Module}}{{if not .Module.Main}}{{.Dir}}|{{.Module.Dir}}|{{.Module.Path}}{{end}}{{end}}' .
done | sort -u | while IFS='|' read -r pdir mdir mpath; do
  # su Windows go list stampa percorsi con la barra rovesciata (C:\Users\...):
  # senza normalizzarli il taglio del prefisso non riesce e nasce una cartella "C:".
  pdir="${pdir//\\//}"; mdir="${mdir//\\//}"
  d="$pdir"
  while [ "${#d}" -gt "${#mdir}" ]; do
    for f in "$d"/LICENSE* "$d"/LICENCE* "$d"/COPYING* "$d"/NOTICE*; do
      [ -f "$f" ] || continue
      out="$DEST/moduli-go/$(echo "$mpath" | tr '/' '_')/annidate/$(echo "${d#"$mdir"/}" | tr '/' '_')"
      mkdir -p "$out"
      # più pacchetti (e più sistemi) risalgono alla stessa cartella; i file
      # della cache dei moduli sono in sola lettura, un secondo cp fallirebbe
      [ -e "$out/$(basename "$f")" ] || cp "$f" "$out/"
    done
    d=$(dirname "$d")
  done
done

if [ -n "$SENZA_LICENZA" ]; then
  echo "FALLITO: moduli Go senza file di licenza:$SENZA_LICENZA" >&2
  echo "Va aggiunto a mano il testo in licenses/ e il modulo escluso qui, dopo averne verificato la licenza." >&2
  exit 1
fi
echo "Licenze raccolte in $DEST: $(wc -l < "$DEST/moduli-go/ELENCO.txt" | tr -d ' ') moduli Go più la libreria standard"

# Pacchetti npm di PRODUZIONE: Vite li compila nel frontend, che finisce
# incorporato nel binario (React, pdf.js... in SmartView). I devDependencies
# (Vite, TypeScript) restano fuori: girano solo durante la build.
if [ -f frontend/package.json ] && node -e 'const p=require("./frontend/package.json"); process.exit(Object.keys(p.dependencies||{}).length ? 0 : 1)'; then
  if [ ! -d frontend/node_modules ]; then
    echo "FALLITO: frontend/node_modules assente, impossibile raccogliere le licenze npm (eseguire prima npm ci o wails build)" >&2
    exit 1
  fi
  mkdir -p "$DEST/moduli-js"
  : > "$DEST/moduli-js/ELENCO.txt"
  SENZA_LICENZA=""
  while read -r dir; do
    [ -f "$dir/package.json" ] || continue
    info=$(node -e 'const p=require(process.argv[1]); const l=p.license||(p.licenses&&p.licenses.map(x=>x.type||x).join(" OR "))||""; process.stdout.write(p.name+"@"+p.version+"|"+(typeof l==="string"?l:(l.type||"")))' "$dir/package.json")
    mod="${info%%|*}"; lic="${info#*|}"
    echo "$mod ($lic)" >> "$DEST/moduli-js/ELENCO.txt"
    out="$DEST/moduli-js/$(echo "${mod%@*}" | tr '/@' '__')"
    mkdir -p "$out"
    trovati=0
    for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/license* "$dir"/COPYING* "$dir"/NOTICE*; do
      if [ -f "$f" ]; then
        cp "$f" "$out/"
        trovati=1
      fi
    done
    if [ "$trovati" = 0 ]; then
      # Alcuni pacchetti dichiarano la licenza solo nel package.json.
      if [ -n "$lic" ]; then
        echo "$mod dichiara la licenza \"$lic\" nel package.json, senza un file di testo a parte." > "$out/LICENZA-DICHIARATA.txt"
      else
        SENZA_LICENZA="$SENZA_LICENZA $mod"
      fi
    fi
  done < <(cd frontend && npm ls --omit=dev --all --parseable 2>/dev/null | tail -n +2 | sort -u)
  if [ -n "$SENZA_LICENZA" ]; then
    echo "FALLITO: pacchetti npm senza alcuna licenza dichiarata:$SENZA_LICENZA" >&2
    exit 1
  fi
  sort -u -o "$DEST/moduli-js/ELENCO.txt" "$DEST/moduli-js/ELENCO.txt"
  echo "  più $(wc -l < "$DEST/moduli-js/ELENCO.txt" | tr -d ' ') pacchetti npm del frontend"
fi
