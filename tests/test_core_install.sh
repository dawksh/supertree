#!/usr/bin/env bash
set -euo pipefail

task_repo=$(cd "$(dirname "$0")/.." && pwd)
task_tmp=$(mktemp -d)
trap 'rm -rf "$task_tmp"' EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
task_real_uname=$(command -v uname)
task_real_mv=$(command -v mv)
export HOME="$task_tmp/home" ST_TEST_ASSETS="$task_tmp/assets"
export ST_PREFIX="$task_tmp/bin" ST_CONFDIR="$task_tmp/config" ST_STATE="$task_tmp/state"
export ST_TEST_DOWNLOADS="$task_tmp/downloads" ST_TEST_CORE_STOP="$task_tmp/stopped"
export ST_VERSION=v1.2.3 ST_TEST_LATEST=v1.2.3
export ST_WINDOWS=shell ST_HARNESS=codex ST_YES=1 ST_NO_TMUX_CONF=1
export ST_RELEASE_ROOT=https://example.invalid/supertree
unset TMUX TMUX_PANE ST_CONFIG ST_WITH_NEXT ST_NEXT_SOCKET
mkdir -p "$HOME/.local/bin" "$ST_TEST_ASSETS"
for tool in tmux fzf; do
  printf '#!/bin/sh\nprintf "%%s\\n" "%s 9.0"\n' "$tool" > "$HOME/.local/bin/$tool"
  chmod +x "$HOME/.local/bin/$tool"
done
cat > "$HOME/.local/bin/curl" <<'CURL'
#!/bin/sh
output='' url=''
while [ "$#" -gt 0 ]; do
  case $1 in -o) output=$2; shift 2;; -w) shift 2;; -*) shift;; *) url=$1; shift;; esac
done
printf '%s\n' "$url" >> "$ST_TEST_DOWNLOADS"
case $url in
  */releases/latest) printf '%s/releases/tag/%s' "$ST_RELEASE_ROOT" "$ST_TEST_LATEST";;
  */SHA256SUMS) cp "${ST_TEST_SUMS:-$ST_TEST_ASSETS/SHA256SUMS}" "$output";;
  *) cp "$ST_TEST_ASSETS/${url##*/}" "$output";;
esac
CURL
chmod +x "$HOME/.local/bin/curl"
cat > "$HOME/.local/bin/uname" <<'UNAME'
#!/bin/sh
case $1 in -s) printf '%s\n' "$ST_TEST_OS";; -m) printf '%s\n' "$ST_TEST_ARCH";; *) exit 1;; esac
UNAME
chmod +x "$HOME/.local/bin/uname"
export ST_TEST_OS=$($task_real_uname -s) ST_TEST_ARCH=$($task_real_uname -m)
tar -czf "$ST_TEST_ASSETS/st.tar.gz" -C "$task_repo/bin" st st-lib
cp "$task_repo/install.sh" "$ST_TEST_ASSETS/install.sh"
cp "$task_repo/tmux/supertree.conf" "$ST_TEST_ASSETS/supertree.conf"
make_core_assets() {
  local target
  for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
    {
      printf '#!/usr/bin/env bash\n'
      printf 'version=%q\n' "$ST_TEST_LATEST"
      cat <<'CORE'
case ${1:-} in
  --version) printf 'st-next %s (fixture)\n' "$version";;
  daemon) [ "${2:-}" = stop ] || exit 1; printf '%s\n' "$ST_STATE" >> "$ST_TEST_CORE_STOP";;
  *) printf '%s\n' "$*";;
esac
CORE
    } > "$ST_TEST_ASSETS/st-next-$target"
  done
  (cd "$ST_TEST_ASSETS" && shasum -a 256 st.tar.gz install.sh supertree.conf st-next-* > SHA256SUMS)
}
make_core_assets

# Shell-only is the default, including an old release with no Go assets.
"$task_repo/install.sh" > "$task_tmp/shell.out"
cmp -s "$ST_PREFIX/st-lib-$ST_VERSION/editor.lua" "$task_repo/bin/st-lib/editor.lua" || fail 'installer dropped the editor bridge'
cmp -s "$ST_PREFIX/st-lib-$ST_VERSION/program-theme.lua" "$task_repo/bin/st-lib/program-theme.lua" || fail 'installer dropped the program theme'
[ ! -e "$ST_PREFIX/st-lib-$ST_VERSION/st-next" ] || fail 'default install enabled the core'
! grep -q '/st-next-' "$ST_TEST_DOWNLOADS" || fail 'shell-only install fetched the core'
if "$ST_PREFIX/st" next --version > "$task_tmp/missing.out" 2>&1; then fail 'missing installed core was accepted'; fi
grep -q 'ST_WITH_NEXT=1 st repair' "$task_tmp/missing.out" || fail 'missing installed core has no repair hint'

# Platform aliases select exactly one asset. No Go compiler is used to install.
for spec in 'Darwin x86_64 darwin-amd64' 'Darwin arm64 darwin-arm64' 'Linux x86_64 linux-amd64' 'Linux aarch64 linux-arm64'; do
  set -- $spec
  export ST_TEST_OS=$1 ST_TEST_ARCH=$2
  : > "$ST_TEST_DOWNLOADS"
  ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/core.out"
  grep -q "/st-next-$3$" "$ST_TEST_DOWNLOADS" || fail "wrong asset for $1/$2"
  [ "$(grep -c '/st-next-' "$ST_TEST_DOWNLOADS")" = 1 ] || fail 'downloaded extra platforms'
  cmp -s "$ST_PREFIX/st-lib-$ST_VERSION/st-next" "$ST_TEST_ASSETS/st-next-$3" || fail 'installed a different core'
  [ "$("$ST_PREFIX/st" next --version)" = "st-next $ST_VERSION (fixture)" ] || fail 'installed command did not find its versioned core'
done

# Missing, corrupt, incompatible or mis-versioned core assets leave every
# installed release file intact, before staging replacement modules or command.
before_st=$(cksum "$ST_PREFIX/st")
before_core=$(cksum "$ST_PREFIX/st-lib-$ST_VERSION/st-next")
before_conf=$(cksum "$ST_CONFDIR/supertree.conf")
assert_unchanged() {
  [ "$(cksum "$ST_PREFIX/st")" = "$before_st" ] || fail 'failed install replaced st'
  [ "$(cksum "$ST_PREFIX/st-lib-$ST_VERSION/st-next")" = "$before_core" ] || fail 'failed install replaced core'
  [ "$(cksum "$ST_CONFDIR/supertree.conf")" = "$before_conf" ] || fail 'failed install replaced bindings'
}
export ST_TEST_OS=Linux ST_TEST_ARCH=riscv64
if ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/bad.out" 2>&1; then fail 'unsupported architecture accepted'; fi
grep -q 'ST_WITH_NEXT=0' "$task_tmp/bad.out" || fail 'unsupported architecture has no shell-only hint'
assert_unchanged
export ST_TEST_ARCH=aarch64
awk '$2 !~ /^st-next-/' "$ST_TEST_ASSETS/SHA256SUMS" > "$task_tmp/old-sums"
if ST_TEST_SUMS="$task_tmp/old-sums" ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/bad.out" 2>&1; then fail 'missing core asset accepted'; fi
grep -q 'does not provide st-next-linux-arm64' "$task_tmp/bad.out" || fail 'missing asset not explained'
assert_unchanged
awk '$2 == "st-next-linux-arm64" { $1="0000000000000000000000000000000000000000000000000000000000000000" } {print $1 "  " $2}' \
  "$ST_TEST_ASSETS/SHA256SUMS" > "$task_tmp/bad-sums"
if ST_TEST_SUMS="$task_tmp/bad-sums" ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/bad.out" 2>&1; then fail 'tampered core accepted'; fi
grep -q 'does not match its published checksum' "$task_tmp/bad.out" || fail 'tampered core not explained'
assert_unchanged
export ST_TEST_LATEST=v0.0.0
make_core_assets
if ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/bad.out" 2>&1; then fail 'wrong core version accepted'; fi
grep -q 'workspace core version does not match' "$task_tmp/bad.out" || fail 'wrong version not explained'
assert_unchanged
export ST_TEST_LATEST=$ST_VERSION
make_core_assets

# A final command replacement failure rolls back the matching versioned core.
export ST_TEST_REAL_MV=$task_real_mv
cat > "$HOME/.local/bin/mv" <<'MV'
#!/bin/sh
for arg in "$@"; do
  case $arg in */.st.install.*) exit 1;; esac
done
exec "$ST_TEST_REAL_MV" "$@"
MV
chmod +x "$HOME/.local/bin/mv"
if ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/bad.out" 2>&1; then fail 'replacement failure accepted'; fi
assert_unchanged
rm "$HOME/.local/bin/mv"
[ -z "$(find "$ST_PREFIX" -name '.st*')" ] || fail 'failed install left staged files'

# Update inherits opt-in without ST_WITH_NEXT. Repair restores a lost core and
# module directory using the third install-manifest line, before module loading.
export ST_TEST_LATEST=v1.2.4
make_core_assets
"$ST_PREFIX/st" update > "$task_tmp/update.out"
[ -x "$ST_PREFIX/st-lib-v1.2.4/st-next" ] || fail 'update dropped opt-in'
[ ! -e "$ST_PREFIX/st-lib-v1.2.3" ] || fail 'update left old modules/core'
export ST_VERSION=v1.2.4
rm -rf "$ST_PREFIX/st-lib-$ST_VERSION"
"$ST_PREFIX/st" repair > "$task_tmp/repair.out"
[ -x "$ST_PREFIX/st-lib-$ST_VERSION/st-next" ] || fail 'repair lost opt-in when modules were missing'
[ "$("$ST_PREFIX/st" next --version)" = 'st-next v1.2.4 (fixture)' ] || fail 'repair installed wrong core'

# Uninstall stops the selected state before removing its database and binary.
mkdir -p "$ST_STATE/next"
printf 'y\n' | "$ST_PREFIX/st" uninstall > "$task_tmp/uninstall.out" 2>&1
[ -f "$ST_TEST_CORE_STOP" ] || fail 'uninstall did not stop core'
grep -qxF "$ST_STATE" "$ST_TEST_CORE_STOP" || fail 'uninstall stopped a different state'
[ ! -e "$ST_STATE" ] && [ ! -e "$ST_PREFIX/st-lib-$ST_VERSION" ] || fail 'uninstall left state or core'

# Explicit opt-out overrides remembered choice and writes a shell-only manifest.
ST_WITH_NEXT=1 "$task_repo/install.sh" > "$task_tmp/reinstall.out"
ST_WITH_NEXT=0 "$task_repo/install.sh" > "$task_tmp/opt-out.out"
[ ! -e "$ST_PREFIX/st-lib-$ST_VERSION/st-next" ] || fail 'opt-out kept the optional core'
[ "$(sed -n '3p' "$ST_PREFIX/st.install")" = 0 ] || fail 'opt-out did not clear remembered choice'
"$task_repo/install.sh" > "$task_tmp/shell-again.out"
[ ! -e "$ST_PREFIX/st-lib-$ST_VERSION/st-next" ] || fail 'auto install forgot opt-out'
printf 'ok: optional core platforms, checksums, rollback, update, repair, uninstall and opt-out\n'
