#!/usr/bin/env bash
# Build the usql-bridge c-shared library for every platform the DuckDB luajit
# extension ships.
#
# IMPORTANT — cgo is mandatory:
#   main.go has `import "C"` and uses //export, so -buildmode=c-shared needs
#   CGO_ENABLED=1 *and* a C toolchain for the TARGET platform. Building with
#   CGO_ENABLED=0 fails with "build constraints exclude all Go files".
#   darwin targets therefore need a macOS host (Mach-O linker + Apple SDK);
#   there is no usable cross toolchain for darwin on Linux.
#   That is what .github/workflows/release.yml does: one native runner per OS.
#
# Usage:
#   ./build-release.sh                 # every target this host can build
#   ./build-release.sh linux-amd64 ...  # only the named targets
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist

# target -> "goos goarch ext cc" (empty cc = go default)
target_spec() {
  case "$1" in
    linux-amd64)   echo "linux amd64 so gcc" ;;
    linux-arm64)   echo "linux arm64 so aarch64-linux-gnu-gcc" ;;
    darwin-arm64)  echo "darwin arm64 dylib cc" ;;
    darwin-amd64)  echo "darwin amd64 dylib cc" ;;
    windows-amd64) echo "windows amd64 dll x86_64-w64-mingw32-gcc" ;;
    *) return 1 ;;
  esac
}

ALL_TARGETS="linux-amd64 linux-arm64 darwin-arm64 darwin-amd64 windows-amd64"

build_one() {
  local t="$1" spec goos goarch ext cc
  spec="$(target_spec "$t")" || { echo "!!! unknown target: $t" >&2; return 1; }
  set -- $spec
  goos="$1"; goarch="$2"; ext="$3"; cc="${4:-}"

  if [[ -n "$cc" ]] && ! command -v "${cc%% *}" >/dev/null 2>&1; then
    echo "!!! SKIP $t: C toolchain '$cc' not found on this host" >&2
    return 1
  fi

  local out="dist/usqlbridge-${goos}-${goarch}.${ext}"
  echo "==> ${goos}/${goarch}  CC=${cc:-<go default>}"
  # Darwin/amd64 is built from an arm64 macOS runner by pointing clang at the
  # x86_64 slice of the SDK (both arches ship in one SDK; no extra toolchain).
  local extra=()
  if [[ "$goos/$goarch" == "darwin/amd64" && "$(uname -s)" == "Darwin" ]]; then
    extra=(CGO_CFLAGS="${CGO_CFLAGS:--arch x86_64}" CGO_LDFLAGS="${CGO_LDFLAGS:--arch x86_64}")
  fi
  # -s -w strips DWARF/symtab (~30% smaller download); exported C symbols live
  # in .dynsym and survive, so the FFI bridge keeps working.
  env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=1 ${cc:+CC="$cc"} "${extra[@]}" \
    go build -trimpath -ldflags "-s -w" -buildmode=c-shared -o "$out" .
  rm -f "dist/usqlbridge-${goos}-${goarch}.h"   # generated cgo header, not shipped
  ls -l "$out"
}

rc=0
if [[ $# -gt 0 ]]; then
  for t in "$@"; do build_one "$t" || rc=1; done
else
  for t in $ALL_TARGETS; do build_one "$t" || true; done
fi

echo "=== dist/ ==="
ls -l dist/ 2>/dev/null || true
[[ $rc -eq 0 ]] || { echo "one or more requested targets could not be built" >&2; exit $rc; }
echo "done"
