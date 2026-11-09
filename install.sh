#!/bin/sh
# Installs (or upgrades) the `lane` binary from Laneway's GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/lanewayhq/laneway/main/install.sh | sh
#
# Settings, from the environment:
#   LANEWAY_VERSION      a release tag such as v0.1.0 (default: the latest release)
#   LANEWAY_INSTALL_DIR  where to put `lane` (default: ~/.local/bin)
#
# It checks the download against the release's SHA256SUMS before installing.
# On an upgrade it restarts Laneway's service if it is running. It never uses
# sudo: run `lane install` afterwards to set up the service, ports and CA.
set -eu

repo=lanewayhq/laneway
version=${LANEWAY_VERSION:-}
dir=${LANEWAY_INSTALL_DIR:-$HOME/.local/bin}

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

case "$(uname -s)" in
Linux) ;;
*) fail "Laneway runs on Linux and WSL2; this is $(uname -s)." ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "there is no Laneway build for $(uname -m) (only x86_64 and arm64)." ;;
esac
command -v curl >/dev/null 2>&1 || fail "curl is required."
command -v tar >/dev/null 2>&1 || fail "tar is required."
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required."

if [ -n "${LANEWAY_BASE_URL:-}" ]; then
	base=$LANEWAY_BASE_URL # for testing this script against a local server
elif [ -n "$version" ]; then
	base="https://github.com/$repo/releases/download/$version"
else
	base="https://github.com/$repo/releases/latest/download"
fi
asset="lane_linux_$arch.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset${version:+ ($version)}…"
curl -fsSL --retry 3 -o "$tmp/$asset" "$base/$asset" || fail "couldn't download $base/$asset"
curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || fail "couldn't download $base/SHA256SUMS"
(cd "$tmp" && grep " $asset\$" SHA256SUMS | sha256sum -c --quiet -) ||
	fail "$asset doesn't match the release's SHA256SUMS; not installing it."

tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$dir"
# `lane install` may have let the old binary bind ports 80 and 443 itself
# (setcap); a new file doesn't keep that.
had_cap=
if command -v getcap >/dev/null 2>&1 && [ -f "$dir/lane" ] && getcap "$dir/lane" 2>/dev/null | grep -q cap_net_bind_service; then
	had_cap=1
fi
# Replace the file rather than write into it, so a running daemon keeps its
# old binary until it restarts.
install -m 0755 "$tmp/lane_linux_$arch/lane" "$dir/lane.new"
mv -f "$dir/lane.new" "$dir/lane"
echo "Installed $("$dir/lane" version) to $dir/lane"
if [ -n "$had_cap" ]; then
	echo "The old lane could bind ports 80 and 443 itself; run \`lane install\` again to give the new one that permission."
fi

if command -v systemctl >/dev/null 2>&1 && systemctl --user is-active --quiet laneway.service 2>/dev/null &&
	systemctl --user show -p ExecStart laneway.service 2>/dev/null | grep -q "path=$dir/lane "; then
	systemctl --user restart laneway.service && echo "Restarted laneway.service on the new version."
	echo "Run \`lane doctor\` in each Linked Project to fetch any Plugins it now needs from the Catalog."
else
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "Add $dir to your PATH, then:" ;;
	esac
	echo "Next: run \`lane install\` to set up Laneway's service, ports 80/443 and local CA, then \`lane doctor\`."
fi
