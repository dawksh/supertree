#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d)
trap '[ -z "${ST_ISO_SOCKET:-}" ] || "$ST_REAL_TMUX" -L "$ST_ISO_SOCKET" kill-server >/dev/null 2>&1 || true; rm -rf "$TMP" "${ST_ISO_TMPDIR:-$TMP}"' EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

mkdir -p "$TMP/home" "$TMP/fakebin" "$TMP/install" "$TMP/config"

cat > "$TMP/fakebin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail

out=''
url=''
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift 2;;
    -w) shift 2;;
    -*) shift;;
    *) url=$1; shift;;
  esac
done

case $url in
  */releases/latest)
    [ "${ST_TEST_CURL_TIMEOUT:-0}" != 1 ] || exit 28
    printf '%s/releases/tag/%s' "${ST_RELEASE_ROOT:?}" "${ST_TEST_LATEST:?}"
    ;;
  */releases/download/*/st.tar.gz)
    cp "${ST_TEST_ASSET:?}" "$out"
    ;;
  */releases/download/*/install.sh)
    cp "${ST_TEST_INSTALLER:?}" "$out"
    ;;
  */releases/download/*/supertree.conf)
    cp "${ST_TEST_BINDINGS:?}" "$out"
    ;;
  */releases/download/*/SHA256SUMS)
    sum=${ST_TEST_SUM:-$(shasum -a 256 "${ST_TEST_ASSET:?}" | awk '{ print $1 }')}
    printf '%s  st.tar.gz\n' "$sum" > "$out"
    installer_sum=${ST_TEST_INSTALLER_SUM:-$(shasum -a 256 "${ST_TEST_INSTALLER:?}" | awk '{ print $1 }')}
    printf '%s  install.sh\n' "$installer_sum" >> "$out"
    shasum -a 256 "${ST_TEST_BINDINGS:?}" | awk '{ print $1 "  supertree.conf" }' >> "$out"
    ;;
  *)
    exit 22
    ;;
esac
CURL
chmod +x "$TMP/fakebin/curl"

stamp() {
  sed "s|^ST_VERSION='dev'$|ST_VERSION='$2'|" "$1" > "$3"
  chmod +x "$3"
}

installed="$TMP/install/st"
stamp "$ROOT/bin/st" v1.0.0 "$installed"
cp -R "$ROOT/bin/st-lib" "$TMP/install/st-lib-v1.0.0"
tar -czf "$TMP/release.tar.gz" -C "$ROOT/bin" st st-lib
printf '%s\n%s\n' "$TMP/config" "$TMP/home/.tmux.conf" > "$installed.install"
printf '%s\n' 'ST_HARNESS=codex' "ST_WINDOWS='agent shell'" > "$TMP/config/config"
printf 'old bindings\n' > "$TMP/config/supertree.conf"
printf 'source-file %s/supertree.conf\n' "$TMP/config" > "$TMP/home/.tmux.conf"

# Keep installer reloads away from any tmux server used by the developer
# running this test.
real_tmux=$(command -v tmux || true)
if [ -n "$real_tmux" ]; then
  export ST_REAL_TMUX="$real_tmux" ST_ISO_SOCKET="st-repair-test-$$"
  ST_ISO_TMPDIR=$(mktemp -d /tmp/st-repair-sock.XXXXXX)
  export ST_ISO_TMPDIR TMUX_TMPDIR="$ST_ISO_TMPDIR"
  mkdir -p "$TMP/home/.local/bin"
  cat > "$TMP/home/.local/bin/tmux" <<'TMUX_WRAPPER'
#!/bin/sh
case ${1:-} in
  -L) exec "$ST_REAL_TMUX" "$@";;
  *) exec "$ST_REAL_TMUX" -L "$ST_ISO_SOCKET" -f /dev/null "$@";;
esac
TMUX_WRAPPER
  chmod +x "$TMP/home/.local/bin/tmux"
  "$real_tmux" -L "$ST_ISO_SOCKET" -f /dev/null new-session -d -s audit 'sleep 120'
fi

export ST_TEST_INSTALLER="$ROOT/install.sh"
export ST_TEST_BINDINGS="$ROOT/tmux/supertree.conf"
export ST_TEST_ASSET="$TMP/release.tar.gz"
export ST_RELEASE_ROOT=https://example.invalid/supertree
export ST_TEST_LATEST=v1.1.0

output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  "$installed" update 2>"$TMP/update-stderr")
grep -q '^st: checking for updates\.\.\.$' <<<"$output" || fail 'update did not report its release check'
grep -q '^st: downloading v1\.1\.0\.\.\.$' <<<"$output" || fail 'update did not report its download'
grep -q 'st: updated v1.0.0 -> v1.1.0' <<<"$output" || fail 'update did not report success on stdout'
[ ! -s "$TMP/update-stderr" ] || fail 'successful update wrote to stderr'
[ "$("$installed" --version)" = 'st v1.1.0' ]
[ -f "$TMP/install/st-lib-v1.1.0/00-core.sh" ]
[ ! -e "$TMP/install/st-lib-v1.0.0" ] || fail 'successful update left the old st-lib-v1.0.0'
grep -q '^bind -n M-w ' "$TMP/config/supertree.conf" || fail 'update did not refresh tmux bindings'
grep -qx 'ST_HARNESS=codex' "$TMP/config/config" || fail 'update changed the chosen harness'
grep -qx "ST_WINDOWS='agent shell'" "$TMP/config/config" || fail 'update changed the window layout'

output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  "$installed" update 2>"$TMP/update-stderr")
grep -q '^st: checking for updates\.\.\.$' <<<"$output" || fail 'current release did not report its release check'
grep -q '^st: v1.1.0 is already the latest version$' <<<"$output" || fail 'current release did not report status on stdout'
[ ! -s "$TMP/update-stderr" ] || fail 'current release wrote to stderr'

# Repair works before modules or user config are loaded, and reinstalls the
# same release without silently upgrading to a newer one.
rm "$TMP/install/st-lib-v1.1.0/00-core.sh"
printf '%s\n' 'ST_WINDOWS="unterminated' > "$TMP/config/config"
printf 'broken bindings\n' > "$TMP/config/supertree.conf"
printf '# old supertree.conf line was removed\n' > "$TMP/home/.tmux.conf"
output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  "$installed" repair 2>"$TMP/repair-stderr") ||
  fail "repair failed: $(cat "$TMP/repair-stderr")"
grep -q 'st: repaired v1.1.0' <<<"$output" || fail 'repair did not report completion'
[ -f "$TMP/install/st-lib-v1.1.0/00-core.sh" ] || fail 'repair did not restore modules'
grep -q '^bind -n M-v .* split"$' "$TMP/config/supertree.conf" || fail 'repair did not restore split binding'
[ "$("$installed" --version)" = 'st v1.1.0' ] || fail 'repair changed release version'
[ "$(find "$TMP/config" -name 'config.broken.*' | wc -l | tr -d ' ')" = 1 ] ||
  fail 'repair did not back up the damaged config'
grep -qx 'ST_WINDOWS="unterminated' "$TMP/config"/config.broken.* ||
  fail 'repair backup did not preserve damaged config'
grep -qxF "source-file $TMP/config/supertree.conf" "$TMP/home/.tmux.conf" ||
  fail 'repair did not reconnect tmux config'

printf '%s\n' 'ST_HARNESS=codex' 'ST_WINDOWS="agent agent"' > "$TMP/config/config"
if [ -n "$real_tmux" ]; then
  printf 'source-file %s/supertree.conf\nbind -n M-v display-message taken\nunbind -n M-B\n' \
    "$TMP/config" > "$TMP/home/.tmux.conf"
  "$TMP/home/.local/bin/tmux" list-sessions >/dev/null ||
    fail 'isolated tmux server is unavailable before repair'
  "$TMP/home/.local/bin/tmux" source-file "$TMP/home/.tmux.conf"
  doctor=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" ST_WINDOWS=shell "$installed" doctor 2>&1 || true)
  grep -q 'WARN  M-v is overridden by:' <<<"$doctor" ||
    fail 'doctor did not identify the conflicting M-v binding'
  grep -q 'WARN  M-B is unbound' <<<"$doctor" ||
    fail 'doctor did not identify the unbound M-B key'
fi
HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" "$installed" repair >"$TMP/repair-windows.out" 2>&1 ||
  fail "repair of invalid windows failed: $(cat "$TMP/repair-windows.out")"
grep -qx "ST_WINDOWS='agent vim shell'" "$TMP/config/config" ||
  fail 'repair did not restore default windows'
if [ -n "$real_tmux" ]; then
  grep -q 'WARN  M-v is overridden by:' "$TMP/repair-windows.out" ||
    fail "repair did not identify the conflicting M-v binding: $(cat "$TMP/repair-windows.out")"
  grep -q 'WARN  M-B is unbound' "$TMP/repair-windows.out" ||
    fail 'repair did not identify the unbound M-B key'
  HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" "$installed" _binding-audit >"$TMP/audit-after.out" ||
    fail "repair did not restore live keys: $(cat "$TMP/audit-after.out")"
fi
printf '%s\n' 'ST_HARNESS=codex' 'ST_WINDOWS="agent shell"' 'ST_KEY_SPLIT="M-v;bad"' > "$TMP/config/config"
HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" "$installed" repair >"$TMP/repair-key.out" 2>&1 ||
  fail "repair of invalid key failed: $(cat "$TMP/repair-key.out")"
grep -q '^bind -n M-v .* split"$' "$TMP/config/supertree.conf" ||
  fail 'repair did not restore default split key'
grep -qx 'ST_KEY_SPLIT="M-v;bad"' "$TMP/config"/config.broken.* ||
  fail 'repair did not back up invalid key config'

printf '%s\n' 'ST_HARNESS=codex' 'ST_WINDOWS="agent shell"' 'ST_KEY_SPLIT=M-w' > "$TMP/config/config"
HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" "$installed" repair >"$TMP/repair-duplicate.out" 2>&1 ||
  fail "repair with duplicate keys failed: $(cat "$TMP/repair-duplicate.out")"
grep -q 'WARN  M-w is assigned more than once' "$TMP/repair-duplicate.out" ||
  fail 'repair did not identify the duplicate Supertree key'

if output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_TEST_CURL_TIMEOUT=1 "$installed" update 2>&1); then
  fail 'update unexpectedly succeeded after a release lookup timeout'
fi
grep -q 'st: checking for updates' <<<"$output" || fail 'timed out update gave no initial feedback'
grep -q 'could not check for updates' <<<"$output" || fail 'timed out update gave no clear error'

stamp "$ROOT/bin/st" v1.0.0 "$installed"
cp -R "$ROOT/bin/st-lib" "$TMP/install/st-lib-v1.0.0"
before=$(cksum "$installed")
if output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_TEST_INSTALLER_SUM=0000000000000000000000000000000000000000000000000000000000000000 \
  "$installed" update 2>&1); then
  fail 'update with a tampered installer unexpectedly succeeded'
fi
grep -q 'installer for st .* does not match its published checksum' <<<"$output" ||
  fail "tampered installer was not explained: $output"
[ "$(cksum "$installed")" = "$before" ] || fail 'tampered installer replaced st'

if output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_TEST_SUM=0000000000000000000000000000000000000000000000000000000000000000 \
  "$installed" update 2>&1); then
  fail 'update with a checksum mismatch unexpectedly succeeded'
fi
grep -q 'does not match its published checksum' <<<"$output" ||
  fail "checksum mismatch did not explain itself: $output"
[ "$(cksum "$installed")" = "$before" ] || fail 'checksum mismatch replaced st'
[ -f "$TMP/install/st-lib-v1.0.0/00-core.sh" ] || fail 'checksum mismatch removed the old modules'
[ "$("$installed" --version)" = 'st v1.0.0' ] || fail 'checksum mismatch changed the installed version'

printf 'not a script\n' > "$TMP/bad-asset"
before=$(cksum "$installed")
if HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_TEST_ASSET="$TMP/bad-asset" \
  "$installed" update >/dev/null 2>&1; then
  printf 'malformed update unexpectedly succeeded\n' >&2
  exit 1
fi
[ "$(cksum "$installed")" = "$before" ]

printf 'update tests passed\n'
