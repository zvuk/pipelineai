#!/usr/bin/env bash
set -euo pipefail

version="${TOKENIZERS_LIB_VERSION:-1.26.0}"
root="${TOKENIZERS_LIB_ROOT:-$(pwd)/.cache/tokenizers}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "${os}" in
  linux) ;;
  darwin) ;;
  *)
    echo "unsupported OS: ${os}" >&2
    exit 1
    ;;
esac

case "${arch}" in
  x86_64|amd64)
    arch="amd64"
    ;;
  aarch64|arm64)
    arch="arm64"
    ;;
  *)
    echo "unsupported arch: ${arch}" >&2
    exit 1
    ;;
esac

archive="libtokenizers.${os}-${arch}.tar.gz"
url="https://github.com/daulet/tokenizers/releases/download/v${version}/${archive}"
target_dir="${root}/${os}-${arch}"
target_lib="${target_dir}/libtokenizers.a"

if [ -f "${target_lib}" ]; then
  echo "${target_dir}"
  exit 0
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

mkdir -p "${target_dir}"
curl -fsSL "${url}" -o "${tmp_dir}/${archive}"
tar -xzf "${tmp_dir}/${archive}" -C "${tmp_dir}"

if [ ! -f "${tmp_dir}/libtokenizers.a" ]; then
  echo "downloaded archive does not contain libtokenizers.a" >&2
  exit 1
fi

mv "${tmp_dir}/libtokenizers.a" "${target_lib}"
echo "${target_dir}"
