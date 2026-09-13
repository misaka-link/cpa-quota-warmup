#!/usr/bin/env bash
# Build the plugin shared library.
#
# CPA derives a plugin's id from the artifact file name, so the output is named
# "<plugin id>-v<version>.so". Set ARTIFACT_STYLE=platform for the older
# "<plugin id>-<goos>-<goarch>.so" naming used by the release workflow.
set -euo pipefail

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${SOURCE_DIR}"

PLUGIN_ID="cpa-quota-warmup"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
OUT_DIR="${OUT_DIR:-dist}"
SKIP_TESTS="${SKIP_TESTS:-0}"
ARTIFACT_STYLE="${ARTIFACT_STYLE:-id}"

case "$GOOS" in
  windows) EXT=".dll" ;;
  darwin) EXT=".dylib" ;;
  *) EXT=".so" ;;
esac

VERSION="${VERSION:-$(sed -n 's/.*pluginVersion *= *"\(.*\)".*/\1/p' main.go | head -n1)}"
[[ -n "${VERSION}" ]] || { echo "could not determine the plugin version from main.go" >&2; exit 1; }

if [[ "${ARTIFACT_STYLE}" == "platform" ]]; then
  ARTIFACT="${PLUGIN_ID}-${GOOS}-${GOARCH}${EXT}"
else
  ARTIFACT="${PLUGIN_ID}-v${VERSION}${EXT}"
fi

mkdir -p "$OUT_DIR"

if [[ "$SKIP_TESTS" != "1" ]]; then
  CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go vet ./...
  CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go test ./...
fi

# -buildvcs=false: without it, `go build` embeds the working tree's VCS
# status (e.g. a "-dirty" marker or differing revision) in the binary,
# which makes two otherwise-identical builds from the same commit produce
# different bytes/hashes depending on what else happens to be lying around
# in the working tree at build time.
CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags="-s -w" -o "$OUT_DIR/$ARTIFACT" .

# CPA loads the library by file name; the generated C header is build-only.
rm -f "${OUT_DIR}/${ARTIFACT%.*}.h"

( cd "$OUT_DIR" && sha256sum "$ARTIFACT" > "${ARTIFACT}.sha256" )

echo "Built $OUT_DIR/$ARTIFACT"
echo "       $(cat "$OUT_DIR/${ARTIFACT}.sha256")"
