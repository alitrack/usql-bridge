#!/usr/bin/env bash
# Cross-compile the usql-bridge c-shared lib for the platforms the DuckDB
# luajit extension ships. Output: dist/usqlbridge-<os>-<arch>.{so,dll,dylib}
#
# Requirements: Go >= 1.26 (usql's go.mod floor). CGO for the C host ABI.
# Set USQL_VERSION to pin the upstream xo/usql version (default: latest v0.21.x).
set -euo pipefail
cd "$(dirname "$0")"

USQL_VERSION="${USQL_VERSION:-v0.21.4}"
export GOTOOLCHAIN=go1.26.1+auto
mkdir -p dist

# Make go.mod point at the published version (strip any local replace).
if grep -q '=> /' go.mod 2>/dev/null; then
  echo "go.mod has a local replace; run from a clean checkout to release." >&2
  exit 1
fi

build_one() {
  local goos="$1" goarch="$2" cgo="$3" ext="$4"
  echo "==> building ${goos}/${goarch}"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="$cgo" \
    go build -buildmode=c-shared -o "dist/usqlbridge-${goos}-${goarch}.${ext}" .
}

# Linux: amd64 + arm64. moderncsqlite is pure Go so CGO_ENABLED=0 works and
# keeps the artifact portable (no glibc version pin beyond what Go needs).
build_one linux amd64 0 so
build_one linux arm64 0 so

# macOS: arm64 (+ amd64 if a cross cgo toolchain is present; arm64 native is the default target)
if [[ "$HOST_OS" == "darwin" || "$(uname -s)" == "Darwin" ]]; then
  build_one darwin arm64 0 dylib
  build_one darwin amd64 0 dylib || echo "skip darwin/amd64 (no cross cgo)"
fi

# Windows: needs mingw cross-compiler for c-shared. Best done on a Windows host.
if [[ "${BUILD_WINDOWS:-0}" == "1" ]]; then
  build_one windows amd64 1 dll
fi

ls -la dist/
echo "done"
