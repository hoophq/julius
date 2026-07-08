#!/bin/sh
# julius installer — downloads the latest release binary to ~/.local/bin.
#   curl -fsSL https://raw.githubusercontent.com/hoophq/julius/main/install.sh | sh
set -eu

REPO="hoophq/julius"
INSTALL_DIR="${JULIUS_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *) echo "julius: unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin | linux) ;;
  *) echo "julius: unsupported OS: $os (Windows: download from GitHub releases)" >&2; exit 1 ;;
esac

tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
  grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$tag" ]; then
  echo "julius: could not determine the latest release" >&2
  exit 1
fi

version=${tag#v}
url="https://github.com/$REPO/releases/download/$tag/julius_${version}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading julius $tag ($os/$arch)..."
curl -fsSL "$url" -o "$tmp/julius.tar.gz"
tar -xzf "$tmp/julius.tar.gz" -C "$tmp"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/julius" "$INSTALL_DIR/julius"

echo "installed: $INSTALL_DIR/julius"
"$INSTALL_DIR/julius" --version

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: add $INSTALL_DIR to your PATH" ;;
esac

echo
echo "next steps:"
echo "  julius init -g     # register the Claude Code hooks"
echo "  julius doctor      # verify the installation"
