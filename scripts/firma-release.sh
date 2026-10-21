#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Firma il SHA256SUMS.txt di una release GIÀ PUBBLICATA e ne carica la firma
# (SHA256SUMS.txt.sig). Si lancia sul computer di chi pubblica, dopo che il
# run di GitHub Actions della release è finito: la chiave privata sta in
# ~/.centralhub-firma (la crea `go run ./cmd/firma genera`) e non lascia mai
# questo computer, nemmeno su GitHub.
#
# Uso:  bash scripts/firma-release.sh <proprietario/repo> <tag>
#       bash scripts/firma-release.sh trismegistoopensource/centralhub v0.2.2
#
# Carica un file sulla release: è un atto di pubblicazione, lo lancia l'utente.
set -euo pipefail

repo="${1:-}"
tag="${2:-}"
if [ -z "$repo" ] || [ -z "$tag" ]; then
  echo "uso: bash scripts/firma-release.sh <proprietario/repo> <tag>" >&2
  exit 2
fi
command -v gh >/dev/null || { echo "serve la GitHub CLI (gh), già autenticata" >&2; exit 1; }
command -v go >/dev/null || { echo "serve Go" >&2; exit 1; }

qui="$(cd "$(dirname "$0")/.." && pwd)"
# lo strumento di firma sta in CentralHub; un altro progetto usa l'ultima copia
if [ -d "$qui/cmd/firma" ]; then
  strumento="$qui"
else
  strumento="$(ls -d "$qui"/../../../CentralHub/CentralHub_*/sourcecode 2>/dev/null | sort -V | tail -1 || true)"
fi
[ -n "${strumento:-}" ] && [ -d "$strumento/cmd/firma" ] || { echo "non trovo cmd/firma di CentralHub" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "== scarico SHA256SUMS.txt di $repo $tag"
gh release download "$tag" -R "$repo" -p SHA256SUMS.txt -D "$tmp"
[ -s "$tmp/SHA256SUMS.txt" ] || { echo "la release non ha SHA256SUMS.txt" >&2; exit 1; }
cat "$tmp/SHA256SUMS.txt"

echo "== firmo con la chiave privata"
(cd "$strumento" && go run ./cmd/firma firma "$tmp/SHA256SUMS.txt" "$repo" "$tag")
echo "== controllo la firma con la chiave pubblica incorporata in CentralHub"
(cd "$strumento" && go run ./cmd/firma verifica "$tmp/SHA256SUMS.txt" "$tmp/SHA256SUMS.txt.sig" "$repo" "$tag")

echo "== carico SHA256SUMS.txt.sig su $repo $tag"
gh release upload "$tag" "$tmp/SHA256SUMS.txt.sig" -R "$repo" --clobber
echo "fatto"
