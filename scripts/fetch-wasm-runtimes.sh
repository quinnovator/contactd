#!/usr/bin/env bash
# Fetch the WASI language runtimes tier 0 runs (internal/env/wasm) into
# third_party/wasm, laid out the way wasm.DefaultBases expects:
#
#   python-3.12/python.wasm   CPython, base "python-3.12"
#   python-3.12/lib/          its standard library, mounted read-only at /lib
#   quickjs/qjs.wasm          QuickJS-ng, base "quickjs"
#
# Both are pinned to a release and verified by sha256, so local runs, CI and
# the image execute the same bytes. Re-running is a no-op when the installed
# files already match.
#
# Needs only bash, curl, unzip and sha256sum (or shasum).
set -euo pipefail

# CPython's own WASI builds, published by a core developer for every CPython
# release. 3.12.12 is the newest 3.12; this build keeps its standard library
# on disk rather than inside the module.
PYTHON_VERSION="3.12.12"
PYTHON_ARCHIVE="python-${PYTHON_VERSION}-wasi_sdk-20.zip"
PYTHON_ARCHIVE_SHA="e40dac3ae68c988b9dcbf2ff6a1fb1b84435aa05b20defcd155801339f35feb2"
PYTHON_WASM_SHA="62392f07fee032c22e3aa84be033c07105cd42424e5149058b9f5449a8deb272"
PYTHON_URL="https://github.com/brettcannon/cpython-wasi-build/releases/download/v${PYTHON_VERSION}/${PYTHON_ARCHIVE}"

# QuickJS-ng ships a WASI command build of its qjs CLI with every release.
QUICKJS_VERSION="0.17.0"
QUICKJS_WASM_SHA="42a732a676ec2d93488c19411e0fad283bf72658fdad746f089914b523c783b1"
QUICKJS_URL="https://github.com/quickjs-ng/quickjs/releases/download/v${QUICKJS_VERSION}/qjs-wasi.wasm"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="${WASM_RUNTIME_DIR:-$root/third_party/wasm}"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# matches FILE SHA: true when FILE exists and hashes to SHA.
matches() {
  [[ -f "$1" ]] && [[ "$(sha256_of "$1")" == "$2" ]]
}

# verify FILE SHA: fail loudly on a mismatch.
verify() {
  local got
  got="$(sha256_of "$1")"
  if [[ "$got" != "$2" ]]; then
    echo "fetch-wasm-runtimes: checksum mismatch for $(basename "$1")" >&2
    echo "  want $2" >&2
    echo "  got  $got" >&2
    exit 1
  fi
}

fetch() {
  echo "fetch-wasm-runtimes: downloading $1"
  curl --fail --silent --show-error --location --retry 3 --output "$2" "$1"
}

mkdir -p "$dest"
# Download and unpack next to the destination so the final mv is a rename on
# the same filesystem: a provider never sees a half-written runtime.
tmp="$(mktemp -d "$dest/.fetch.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

install_python() {
  local dir="$dest/python-3.12"
  # The stamp is written last and names the archive the tree came from, so an
  # interrupted or older install is redone.
  if matches "$dir/python.wasm" "$PYTHON_WASM_SHA" &&
    [[ "$(cat "$dir/.archive-sha256" 2>/dev/null)" == "$PYTHON_ARCHIVE_SHA" ]]; then
    echo "fetch-wasm-runtimes: python-3.12 up to date (CPython $PYTHON_VERSION)"
    return
  fi
  fetch "$PYTHON_URL" "$tmp/$PYTHON_ARCHIVE"
  verify "$tmp/$PYTHON_ARCHIVE" "$PYTHON_ARCHIVE_SHA"
  mkdir "$tmp/python"
  unzip -q "$tmp/$PYTHON_ARCHIVE" -d "$tmp/python"
  verify "$tmp/python/python.wasm" "$PYTHON_WASM_SHA"
  [[ -f "$tmp/python/lib/python3.12/os.py" ]] || {
    echo "fetch-wasm-runtimes: $PYTHON_ARCHIVE has no lib/python3.12" >&2
    exit 1
  }
  echo "$PYTHON_ARCHIVE_SHA" >"$tmp/python/.archive-sha256"
  rm -rf "$dir"
  mv "$tmp/python" "$dir"
  echo "fetch-wasm-runtimes: installed $dir (CPython $PYTHON_VERSION)"
}

install_quickjs() {
  local file="$dest/quickjs/qjs.wasm"
  if matches "$file" "$QUICKJS_WASM_SHA"; then
    echo "fetch-wasm-runtimes: quickjs up to date (QuickJS-ng $QUICKJS_VERSION)"
    return
  fi
  fetch "$QUICKJS_URL" "$tmp/qjs.wasm"
  verify "$tmp/qjs.wasm" "$QUICKJS_WASM_SHA"
  chmod 0644 "$tmp/qjs.wasm"
  mkdir -p "$dest/quickjs"
  mv -f "$tmp/qjs.wasm" "$file"
  echo "fetch-wasm-runtimes: installed $file (QuickJS-ng $QUICKJS_VERSION)"
}

install_python
install_quickjs
