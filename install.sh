#!/bin/sh
# Installs dv from its GitHub releases:
#
#   curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/dv/main/install.sh | sh
#
# DV_VERSION=v0.2.0 installs that release instead of the latest, and
# DV_INSTALL_DIR puts the binary somewhere other than ~/.local/bin.
set -eu

repo=priyanshu-shubham/dv
dir=${DV_INSTALL_DIR:-$HOME/.local/bin}

fail() {
	echo "dv: $*" >&2
	exit 1
}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "no prebuilt binary for $(uname -s); see https://github.com/$repo#install" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no prebuilt binary for $(uname -m); see https://github.com/$repo#install" ;;
esac

if [ -n "${DV_VERSION:-}" ]; then
	base=https://github.com/$repo/releases/download/$DV_VERSION
else
	base=https://github.com/$repo/releases/latest/download
fi
asset=dv_${os}_${arch}.tar.gz

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "could not download $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "could not download $base/checksums.txt"

want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
	got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "$asset does not match its checksum; not installing"

tar -xzf "$tmp/$asset" -C "$tmp" dv
mkdir -p "$dir"
# A copy then a rename, so a dv that is running keeps its old binary intact.
cp "$tmp/dv" "$dir/.dv.new"
chmod 0755 "$dir/.dv.new"
mv -f "$dir/.dv.new" "$dir/dv"

echo "dv: installed $("$dir/dv" -version | cut -d' ' -f2) to $dir/dv"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "dv: $dir is not on your PATH; add it to run dv from anywhere" ;;
esac
echo "dv: run \`dv claude install\` once to see Claude Code's permission prompts in dv"
