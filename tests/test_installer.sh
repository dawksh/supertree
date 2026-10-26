#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
unset TMUX TMUX_PANE

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

export HOME="$TEST_ROOT/home"
export ST_PREFIX="$TEST_ROOT/bin"
export ST_CONFDIR="$TEST_ROOT/config"
export ST_NO_TMUX_CONF=1
export ST_HARNESS=codex
export ST_YES=1
export ST_TEST_ASSETS="$TEST_ROOT/assets"
export ST_TEST_LATEST=v9.8.7
mkdir -p "$HOME/.local/bin" "$ST_TEST_ASSETS"
tar -czf "$ST_TEST_ASSETS/st.tar.gz" -C "$ROOT/bin" st st-lib
cp "$ROOT/tmux/supertree.conf" "$ST_TEST_ASSETS/supertree.conf"
(cd "$ST_TEST_ASSETS" && shasum -a 256 st.tar.gz supertree.conf > SHA256SUMS)

for command in tmux fzf nvim codex; do
  printf '%s\n' \
    '#!/bin/sh' \
    'printf "%s 1.0.0\n" "$(basename "$0")"' > "$HOME/.local/bin/$command"
  chmod +x "$HOME/.local/bin/$command"
done

printf '%s\n' \
  '#!/bin/sh' \
  'output=""' \
  'url=""' \
  'while [ "$#" -gt 0 ]; do' \
  '  case $1 in' \
  '    -o) output=$2; shift 2;;' \
  '    -w) shift 2;;' \
  '    -*) shift;;' \
  '    *) url=$1; shift;;' \
  '  esac' \
  'done' \
  'case $url in' \
  '  */releases/latest) printf "%s/releases/tag/%s" "$ST_RELEASE_ROOT" "$ST_TEST_LATEST";;' \
  '  */SHA256SUMS) cp "${ST_TEST_SUMS:-$ST_TEST_ASSETS/SHA256SUMS}" "$output";;' \
  '  */st.tar.gz|*/supertree.conf) cp "$ST_TEST_ASSETS/${url##*/}" "$output";;' \
  '  *) exit 1;;' \
  'esac' > "$HOME/.local/bin/curl"
chmod +x "$HOME/.local/bin/curl"

export ST_RELEASE_ROOT=https://example.invalid/supertree
output=$("$ROOT/install.sh")

[ -x "$ST_PREFIX/st" ] || { printf 'FAIL: st was not installed\n' >&2; exit 1; }
[ -f "$ST_PREFIX/st-lib-$ST_TEST_LATEST/00-core.sh" ] || {
  printf 'FAIL: st modules were not installed\n' >&2; exit 1
}
grep -F "supertree $ST_TEST_LATEST" <<<"$output" >/dev/null || {
  printf 'FAIL: installer did not show the resolved version\n' >&2
  exit 1
}
[ "$("$ST_PREFIX/st" --version)" = "st $ST_TEST_LATEST" ] || {
  printf 'FAIL: installer did not stamp the resolved version\n' >&2
  exit 1
}
grep -qx 'ST_HARNESS=codex' "$ST_CONFDIR/config" || {
  printf 'FAIL: installer did not persist the harness choice\n' >&2
  exit 1
}
grep -qx "ST_WINDOWS='agent vim shell'" "$ST_CONFDIR/config" || {
  printf 'FAIL: installer did not persist the default window layout\n' >&2
  exit 1
}
grep -F "run-shell -b \"'$ST_PREFIX/st' window 1\"" "$ST_CONFDIR/supertree.conf" >/dev/null || {
  printf 'FAIL: tmux window binding did not use the install prefix\n' >&2
  exit 1
}

# A layout override updates the existing config and omits disabled dependencies.
# Installing a newer release also drops the previous release's modules.
export ST_WINDOWS='agent shell'
export ST_TEST_LATEST=v9.8.8
rm "$HOME/.local/bin/nvim"
output=$("$ROOT/install.sh")
[ -f "$ST_PREFIX/st-lib-v9.8.8/00-core.sh" ] || fail 'reinstall did not install the new modules'
[ ! -e "$ST_PREFIX/st-lib-v9.8.7" ] || fail 'reinstall left the previous release modules'
grep -qx "ST_WINDOWS='agent shell'" "$ST_CONFDIR/config" || {
  printf 'FAIL: installer did not update the window layout\n' >&2
  exit 1
}
if grep -Eq '^[[:space:]]+.[[:space:]]+nvim[[:space:]]' <<<"$output"; then
  printf 'FAIL: installer checked nvim for a layout without vim\n' >&2
  exit 1
fi

( unset ST_CONFDIR ST_CONFIG TMUX_CONF
  printf 'y\n' | "$ST_PREFIX/st" uninstall ) > "$TEST_ROOT/uninstall.out" 2>&1
[ ! -e "$ST_PREFIX/st" ] || {
  printf 'FAIL: uninstall left the installed command\n' >&2
  exit 1
}
[ ! -e "$ST_PREFIX/st.install" ] || {
  printf 'FAIL: uninstall left install metadata\n' >&2
  exit 1
}
[ ! -e "$ST_PREFIX/st-lib-$ST_TEST_LATEST" ] || {
  printf 'FAIL: uninstall left installed modules\n' >&2
  exit 1
}
[ ! -e "$ST_CONFDIR/config" ] && [ ! -e "$ST_CONFDIR/supertree.conf" ] || {
  printf 'FAIL: uninstall left installed config\n' >&2
  exit 1
}

# Every st binding runs in the background so tmux is not blocked while st works.
for key in M-e M-Tab M-1 M-2 M-3 M-4 M-5 M-6 M-7 M-8 M-9 M-s M-B M-v M-j M-k M-q; do
  grep -Eq "^bind -n $key run-shell -b " "$ROOT/tmux/supertree.conf" ||
    fail "supertree.conf binding $key does not use run-shell -b"
done

# A config whose last line is a false test must not break st.
printf '%s\n' 'ST_HARNESS=codex' '[ -n "${ST_TEST_UNSET:-}" ] && ST_WINDOWS=shell' > "$TEST_ROOT/false-tail.conf"
version=$(ST_CONFIG="$TEST_ROOT/false-tail.conf" "$ROOT/bin/st" --version 2>&1) ||
  fail "st --version failed with a config ending in a false test: $version"
[ "$version" = 'st dev' ] || fail "unexpected st --version output: $version"

ST_CONFIG="$TEST_ROOT/false-tail.conf" "$ROOT/bin/st" help | grep -Eq '^  M-r ' ||
  fail 'st help does not list M-r'

install_into() {
  ST_PREFIX="$1" ST_CONFDIR="$2" "$ROOT/install.sh"
}

# Prefixes with shell and tmux metacharacters are quoted in the rendered bindings.
odd_prefix="$TEST_ROOT/we ird'\$HOME#x"
odd_conf="$TEST_ROOT/odd-config"
install_into "$odd_prefix" "$odd_conf" > "$TEST_ROOT/odd.out" 2>&1 ||
  fail "install into an unusual prefix failed: $(cat "$TEST_ROOT/odd.out")"
expected_word="'$TEST_ROOT/we ird'\\\\''\\\$HOME##x/st'"
grep -qxF "bind -n M-1 run-shell -b \"$expected_word window 1\"" "$odd_conf/supertree.conf" ||
  fail "unusual prefix was not quoted for tmux: $(grep 'M-1' "$odd_conf/supertree.conf")"
grep -qxF "bind -n M-w run-shell -b -c '#{pane_current_path}' \"$expected_word go --popup '#{client_tty}'\"" \
  "$odd_conf/supertree.conf" || fail 'unusual prefix broke the M-w binding'

# Let a real, isolated tmux server parse the rendered command and run it through sh.
real_tmux=$(command -v tmux || true)
if [ -n "$real_tmux" ]; then
  socket="st-installer-test-$$"
  printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$*" > "$ST_TEST_LOG"' > "$odd_prefix/st"
  chmod +x "$odd_prefix/st"
  sed -n 's/^bind -n M-1 run-shell -b /run-shell /p' "$odd_conf/supertree.conf" > "$TEST_ROOT/odd-run.conf"
  ST_TEST_LOG="$TEST_ROOT/odd-run.log" "$real_tmux" -L "$socket" -f /dev/null \
    new-session -d -s probe "sleep 30" \; source-file "$TEST_ROOT/odd-run.conf" >/dev/null 2>&1 || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$TEST_ROOT/odd-run.log" ] && break; sleep 0.2; done
  "$real_tmux" -L "$socket" kill-server >/dev/null 2>&1 || true
  [ "$(cat "$TEST_ROOT/odd-run.log" 2>/dev/null)" = 'window 1' ] ||
    fail 'tmux could not run the binding rendered for an unusual prefix'
fi

# ST_KEY_* overrides rebind keys; swapped keys are rewritten only once.
keys_prefix="$TEST_ROOT/keys-bin"
keys_conf="$TEST_ROOT/keys-config"
mkdir -p "$keys_conf"
printf '%s\n' 'ST_KEY_PICKER=M-o' 'ST_KEY_WINDOW_1=M-2' 'ST_KEY_WINDOW_2=M-1' 'ST_KEY_DETACH=C-x' \
  'ST_KEY_SPLIT=M-V' 'ST_KEY_HELP=F1' 'ST_KEY_PREV_TAB=M-P' > "$keys_conf/config"
install_into "$keys_prefix" "$keys_conf" > "$TEST_ROOT/keys.out" 2>&1 ||
  fail "install with custom keys failed: $(cat "$TEST_ROOT/keys.out")"
rendered="$keys_conf/supertree.conf"
grep -q "^bind -n M-o run-shell -b -c .* go --popup" "$rendered" || fail 'ST_KEY_PICKER was not applied'
grep -q '^bind -n M-w ' "$rendered" && fail 'M-w is still bound after ST_KEY_PICKER moved it'
grep -q "^bind -n M-2 run-shell -b .* window 1\"$" "$rendered" || fail 'ST_KEY_WINDOW_1=M-2 was not applied'
grep -q "^bind -n M-1 run-shell -b .* window 2\"$" "$rendered" || fail 'ST_KEY_WINDOW_2=M-1 was not applied'
grep -qx 'bind -n C-x detach-client' "$rendered" || fail 'ST_KEY_DETACH was not applied'
grep -q '^bind -n M-V run-shell -b .* split"$' "$rendered" || fail 'ST_KEY_SPLIT was not applied'
grep -q '^bind -n F1 display-popup -EE .* keys --wait"$' "$rendered" || fail 'ST_KEY_HELP was not applied'
grep -qx 'bind -n M-P previous-window' "$rendered" || fail 'ST_KEY_PREV_TAB was not applied'
grep -Eq '^bind -n (M-v|M-/|M-p) ' "$rendered" && fail 'a rebound key kept its default binding'
grep -q '@st@' "$rendered" && fail 'rewrite marker leaked into the rendered conf'
for key in M-e M-Tab M-r M-3 M-q; do
  grep -q "^bind -n $key run-shell" "$rendered" || fail "unset key $key lost its default"
done

# Defaults: every key keeps its default binding when no ST_KEY_* is set.
for key in M-w M-e M-Tab M-r M-1 M-2 M-3 M-q; do
  grep -q "^bind -n $key run-shell" "$odd_conf/supertree.conf" || fail "default key $key missing"
done
grep -qx 'bind -n M-Q detach-client' "$odd_conf/supertree.conf" || fail 'default M-Q missing'
grep -q '^bind -n M-/ display-popup -EE ' "$odd_conf/supertree.conf" || fail 'default M-/ missing'

printf '%s\n' 'ST_KEY_TOGGLE="M-e;kill-server"' > "$keys_conf/config"
if install_into "$keys_prefix" "$keys_conf" > "$TEST_ROOT/bad-key.out" 2>&1; then
  fail 'install accepted a key with invalid characters'
fi
grep -q 'invalid key binding' "$TEST_ROOT/bad-key.out" || fail "invalid key was not explained: $(cat "$TEST_ROOT/bad-key.out")"

printf '%s\n' "ST_KEY_PICKER='M-a M-b'" > "$keys_conf/config"
if install_into "$keys_prefix" "$keys_conf" > "$TEST_ROOT/space-key.out" 2>&1; then
  fail 'install accepted a key containing whitespace'
fi
grep -q 'invalid key binding' "$TEST_ROOT/space-key.out" ||
  fail "whitespace key was not explained: $(cat "$TEST_ROOT/space-key.out")"

# doctor checks the tmux config recorded at install time, not just ~/.tmux.conf.
doctor_prefix="$TEST_ROOT/doctor-bin"
doctor_conf="$TEST_ROOT/doctor-config"
custom_tmux="$TEST_ROOT/custom.tmux.conf"
printf 'source-file %s/supertree.conf\n' "$doctor_conf" > "$custom_tmux"
TMUX_CONF="$custom_tmux" install_into "$doctor_prefix" "$doctor_conf" > "$TEST_ROOT/doctor-install.out" 2>&1 ||
  fail "install for doctor test failed: $(cat "$TEST_ROOT/doctor-install.out")"
doctor=$( (unset TMUX_CONF; ST_CONFDIR="$doctor_conf" "$doctor_prefix/st" doctor) 2>&1 || true)
grep -qF "ok    $custom_tmux sources supertree.conf" <<<"$doctor" ||
  fail "doctor did not check the installed tmux config: $doctor"

# A release asset that does not match SHA256SUMS fails before anything is replaced.
zero=0000000000000000000000000000000000000000000000000000000000000000
for asset in st.tar.gz supertree.conf; do
  awk -v name="$asset" -v zero="$zero" '$2 == name { $1 = zero } { print $1 "  " $2 }' \
    "$ST_TEST_ASSETS/SHA256SUMS" > "$TEST_ROOT/bad-sums"
  before_st=$(cksum "$doctor_prefix/st")
  before_conf=$(cksum "$doctor_conf/supertree.conf")
  if ST_TEST_SUMS="$TEST_ROOT/bad-sums" install_into "$doctor_prefix" "$doctor_conf" \
    > "$TEST_ROOT/bad-sum.out" 2>&1; then
    fail "install accepted a $asset checksum mismatch"
  fi
  grep -q "$asset from .* does not match its published checksum" "$TEST_ROOT/bad-sum.out" ||
    fail "$asset checksum mismatch was not explained: $(cat "$TEST_ROOT/bad-sum.out")"
  [ "$(cksum "$doctor_prefix/st")" = "$before_st" ] || fail "$asset checksum mismatch replaced st"
  [ "$(cksum "$doctor_conf/supertree.conf")" = "$before_conf" ] ||
    fail "$asset checksum mismatch rewrote the bindings"
  [ -z "$(find "$doctor_prefix" -name '.st*')" ] || fail "$asset checksum mismatch left staged files"
done

printf 'ok: installer harness selection, window config, dependencies, and uninstall\n'
