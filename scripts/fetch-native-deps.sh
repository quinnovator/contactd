#!/usr/bin/env bash
# Fetch the two native artifacts the ORT build needs into third_party/lib:
#
#   libonnxruntime.so|.dylib  dlopen'ed at runtime by yalue/onnxruntime_go
#   libtokenizers.a           linked statically by daulet/tokenizers (cgo)
#
# Both are pinned to the versions the Go modules in go.mod were built against
# and verified by sha256, so local builds, CI and the image link the same
# bytes. Re-running is a no-op when the installed files already match.
#
# Needs only bash, curl, tar and sha256sum (or shasum).
set -euo pipefail

# onnxruntime_go v1.27.0 vendors the ORT 1.24.1 C API headers (ORT_API_VERSION
# 24); an older library refuses to hand out that API version at init.
ORT_VERSION="1.24.1"
# daulet/tokenizers checks this at link time: the Go package references a
# symbol named after its own version, so any other libtokenizers.a fails to link.
TOKENIZERS_VERSION="1.26.0"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="${NATIVE_LIB_DIR:-$root/third_party/lib}"

# TARGETOS/TARGETARCH are what `docker buildx` sets; otherwise use the host.
os="${TARGETOS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
arch="${TARGETARCH:-$(uname -m)}"
case "$arch" in
  x86_64) arch="amd64" ;;
  aarch64) arch="arm64" ;;
esac

# Per platform: archive name, archive sha256, path of the library inside the
# archive, installed file name, sha256 of the installed file.
case "$os/$arch" in
  linux/amd64)
    ort_archive="onnxruntime-linux-x64-${ORT_VERSION}.tgz"
    ort_archive_sha="9142552248b735920f9390027e4512a2cacf8946a1ffcbe9071a5c210531026f"
    ort_member="onnxruntime-linux-x64-${ORT_VERSION}/lib/libonnxruntime.so.${ORT_VERSION}"
    ort_file="libonnxruntime.so"
    ort_file_sha="e5a7e3646718d8f1f8f52c8fcb770fe229ab44305caf3ea702d558e6e426c9aa"
    tok_archive="libtokenizers.linux-amd64.tar.gz"
    tok_archive_sha="0713ac926d8473440572f59369a1bf2d3aa3fa5c6446eab45191f6e5f04ffeb0"
    tok_file_sha="0201090069c3588f8422632ce1af07c70a19dfe16dd9d114da5a0604fda7ac42"
    ;;
  linux/arm64)
    ort_archive="onnxruntime-linux-aarch64-${ORT_VERSION}.tgz"
    ort_archive_sha="0f56edd68f7602df790b68b874a46b115add037e88385c6c842bb763b39b9f89"
    ort_member="onnxruntime-linux-aarch64-${ORT_VERSION}/lib/libonnxruntime.so.${ORT_VERSION}"
    ort_file="libonnxruntime.so"
    ort_file_sha="7954e8bdedb497f830c6a679e818d98399b7f4d81ade1126c3e0be74d28111ab"
    tok_archive="libtokenizers.linux-arm64.tar.gz"
    tok_archive_sha="e8a19c39bbad5b86044d98519b131806cad6b4a3e1408c11ad555b630051e49b"
    tok_file_sha="1716954597885577140569e156ef4700ecb759e8d3868f38eb590eafb762202a"
    ;;
  darwin/arm64)
    ort_archive="onnxruntime-osx-arm64-${ORT_VERSION}.tgz"
    ort_archive_sha="c2969315cd9ce0f5fa04f6b53ff72cb92f87f7dcf38e88cacfa40c8f983fbba9"
    ort_member="onnxruntime-osx-arm64-${ORT_VERSION}/lib/libonnxruntime.${ORT_VERSION}.dylib"
    ort_file="libonnxruntime.dylib"
    ort_file_sha="9626bbdd201fbed7e3addcbd9fc7e97ec4954c03afcf40a9883af50498bffb06"
    tok_archive="libtokenizers.darwin-arm64.tar.gz"
    tok_archive_sha="2015c7b9fe29724b98485e05876c0492d6058e02dce2c314470bfbd27bd8a551"
    tok_file_sha="c4a298022a230cde672124e2949c49ab6ad60225f38778ffa755a49ebd84e3df"
    ;;
  *)
    echo "fetch-native-deps: unsupported platform $os/$arch (want linux/amd64, linux/arm64 or darwin/arm64)" >&2
    exit 1
    ;;
esac

ort_url="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${ort_archive}"
tok_url="https://github.com/daulet/tokenizers/releases/download/v${TOKENIZERS_VERSION}/${tok_archive}"

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
    echo "fetch-native-deps: checksum mismatch for $(basename "$1")" >&2
    echo "  want $2" >&2
    echo "  got  $got" >&2
    exit 1
  fi
}

mkdir -p "$dest"
# Download and unpack next to the destination so the final mv is a rename on
# the same filesystem: a build never sees a half-written library.
tmp="$(mktemp -d "$dest/.fetch.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

# install URL ARCHIVE_SHA MEMBER FILE FILE_SHA
install() {
  local url="$1" archive_sha="$2" member="$3" file="$4" file_sha="$5"
  if matches "$dest/$file" "$file_sha"; then
    echo "fetch-native-deps: $file up to date"
    return
  fi
  local archive="$tmp/${url##*/}"
  echo "fetch-native-deps: downloading $url"
  curl --fail --silent --show-error --location --retry 3 --output "$archive" "$url"
  verify "$archive" "$archive_sha"
  mkdir -p "$tmp/x"
  tar -xzf "$archive" -C "$tmp/x" "$member"
  verify "$tmp/x/$member" "$file_sha"
  chmod 0644 "$tmp/x/$member"
  mv -f "$tmp/x/$member" "$dest/$file"
  echo "fetch-native-deps: installed $dest/$file"
}

install "$ort_url" "$ort_archive_sha" "$ort_member" "$ort_file" "$ort_file_sha"
install "$tok_url" "$tok_archive_sha" "libtokenizers.a" "libtokenizers.a" "$tok_file_sha"
