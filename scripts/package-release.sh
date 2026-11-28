#!/usr/bin/env bash
# Build release assets locally. This script never tags, uploads or publishes.
set -euo pipefail

package_release() { # source directory, output directory, version, commit
  local src=$1 assets=$2 version=$3 revision=$4 target goos goarch asset
  local -a names=(st st.tar.gz install.sh supertree.conf)
  case $version in ''|*[!A-Za-z0-9._-]*) printf 'invalid package version: %s\n' "$version" >&2; return 1;; esac
  case $revision in ''|*[!a-fA-F0-9]*) printf 'invalid package commit: %s\n' "$revision" >&2; return 1;; esac
  mkdir -p "$assets" || return 1
  assets=$(cd "$assets" && pwd -P) || return 1
  src=$(cd "$src" && pwd -P) || return 1

  # Keep the shell bundle usable by existing installers and legacy upgraders.
  tar -czf "$assets/st.tar.gz" -C "$src/bin" st st-lib || return 1
  cp "$src/scripts/legacy-upgrade.sh" "$assets/st" || return 1
  cp "$src/install.sh" "$assets/install.sh" || return 1
  cp "$src/tmux/supertree.conf" "$assets/supertree.conf" || return 1
  grep -qx "ST_VERSION='dev'" "$src/bin/st" || return 1
  grep -qx "ST_VERSION='dev'" "$assets/st" || return 1

  # Historical commits may predate the Go packaging contract. The release
  # catch-up loop must still be able to package those as shell-only releases.
  if [ -f "$src/scripts/package-release.sh" ] && [ -f "$src/go.mod" ]; then
    command -v go >/dev/null || { printf 'Go is required to package the workspace core\n' >&2; return 1; }
    for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
      goos=${target%-*} goarch=${target#*-}
      asset="st-next-$target"
      printf 'Building %s (%s)\n' "$asset" "$version"
      CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
        go -C "$src" build -mod=readonly -trimpath -buildvcs=false \
          -ldflags "-s -w -X main.version=$version -X main.commit=$revision" \
          -o "$assets/$asset" ./cmd/st-next || return 1
      names+=("$asset")
    done
  fi
  # Write checksums last: an interrupted or failed build cannot leave a new
  # manifest that appears to describe a complete release.
  (
    cd "$assets" || exit 1
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "${names[@]}"
    else shasum -a 256 "${names[@]}"; fi
  ) > "$assets/SHA256SUMS.new" || return 1
  mv "$assets/SHA256SUMS.new" "$assets/SHA256SUMS"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  [ "$#" -ge 1 ] && [ "$#" -le 2 ] || {
    printf 'usage: %s <output-directory> [version]\n' "$0" >&2; exit 1;
  }
  package_root=$(cd "$(dirname "$0")/.." && pwd -P)
  package_commit=$(git -C "$package_root" rev-parse HEAD)
  package_release "$package_root" "$1" "${2:-dev}" "$package_commit"
fi
