#!/usr/bin/env bash
set -euo pipefail

task_repo=$(cd "$(dirname "$0")/.." && pwd)
source "$task_repo/scripts/package-release.sh"
task_tmp=$(mktemp -d)
trap 'rm -rf "$task_tmp"' EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
mkdir -p "$task_tmp/tools"
export ST_TEST_GO_LOG="$task_tmp/go.log"
cat > "$task_tmp/tools/go" <<'GO'
#!/usr/bin/env bash
set -eu
[ "$CGO_ENABLED" = 0 ]
printf '%s/%s %s\n' "$GOOS" "$GOARCH" "$*" >> "$ST_TEST_GO_LOG"
output=''
while [ "$#" -gt 0 ]; do
  case $1 in -o) output=$2; shift 2;; *) shift;; esac
done
if [ "${ST_TEST_FAIL_BUILD:-}" = "$GOOS/$GOARCH" ]; then exit 1; fi
printf 'fake executable %s/%s\n' "$GOOS" "$GOARCH" > "$output"
chmod +x "$output"
GO
chmod +x "$task_tmp/tools/go"
export PATH="$task_tmp/tools:$PATH"

package_release "$task_repo" "$task_tmp/assets with spaces" v1.2.3 abc123 > "$task_tmp/package.out"
[ "$(wc -l < "$ST_TEST_GO_LOG" | tr -d ' ')" = 4 ] || fail 'wrong build count'
for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
  [ -x "$task_tmp/assets with spaces/st-next-$target" ] || fail "missing $target"
  grep -q "^${target%-*}/${target#*-} " "$ST_TEST_GO_LOG" || fail "missing platform $target"
done
grep -q -- '-mod=readonly -trimpath -buildvcs=false' "$ST_TEST_GO_LOG" || fail 'release build is not pinned to the source modules'
grep -q -- '-X main.version=v1.2.3 -X main.commit=abc123' "$ST_TEST_GO_LOG" || fail 'build metadata not stamped'
(cd "$task_tmp/assets with spaces" && shasum -a 256 -c SHA256SUMS > "$task_tmp/check.out")
[ "$(wc -l < "$task_tmp/check.out" | tr -d ' ')" = 8 ] || fail 'not every release asset was checksummed'
if tar -tzf "$task_tmp/assets with spaces/st.tar.gz" | grep -q st-next; then fail 'shell bundle contains a platform binary'; fi

# Historical commits still produce their four shell assets without a compiler.
mkdir -p "$task_tmp/legacy/bin" "$task_tmp/legacy/scripts" "$task_tmp/legacy/tmux"
cp "$task_repo/bin/st" "$task_tmp/legacy/bin/st"
cp -R "$task_repo/bin/st-lib" "$task_tmp/legacy/bin/st-lib"
cp "$task_repo/scripts/legacy-upgrade.sh" "$task_tmp/legacy/scripts/legacy-upgrade.sh"
cp "$task_repo/install.sh" "$task_tmp/legacy/install.sh"
cp "$task_repo/tmux/supertree.conf" "$task_tmp/legacy/tmux/supertree.conf"
package_release "$task_tmp/legacy" "$task_tmp/old-assets" v1.0.0 abc123
[ "$(wc -l < "$ST_TEST_GO_LOG" | tr -d ' ')" = 4 ] || fail 'legacy source required Go'
[ "$(wc -l < "$task_tmp/old-assets/SHA256SUMS" | tr -d ' ')" = 4 ] || fail 'legacy release manifest changed'

if ST_TEST_FAIL_BUILD=linux/arm64 package_release "$task_repo" "$task_tmp/failed" v1.2.3 abc123 > /dev/null; then
  fail 'build failure accepted'
fi
[ ! -e "$task_tmp/failed/SHA256SUMS" ] || fail 'failed release has a checksum manifest'
if package_release "$task_repo" "$task_tmp/invalid" 'v1;bad' abc123 > /dev/null 2>&1; then fail 'invalid version accepted'; fi
if package_release "$task_repo" "$task_tmp/invalid" v1.0.0 'bad commit' > /dev/null 2>&1; then fail 'invalid commit accepted'; fi
printf 'ok: release platform matrix, metadata, shell compatibility, checksums and build failures\n'
