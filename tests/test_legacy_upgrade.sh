#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/home"
tar -czf "$TMP/st.tar.gz" -C "$ROOT/bin" st st-lib

cat > "$TMP/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = -fsSL ]
[ "$3" = -o ]
case $2 in
  */releases/download/v2.0.0/st.tar.gz) cp "$ST_TEST_BUNDLE" "$4";;
  */releases/download/v2.0.0/SHA256SUMS)
    sum=${ST_TEST_SUM:-$(shasum -a 256 "$ST_TEST_BUNDLE" | awk '{ print $1 }')}
    printf '%s  st.tar.gz\n' "$sum" > "$4";;
  *) exit 22;;
esac
CURL
chmod +x "$TMP/bin/curl"

sed "s|^ST_VERSION='dev'$|ST_VERSION='v2.0.0'|" \
  "$ROOT/scripts/legacy-upgrade.sh" > "$TMP/bin/st"
chmod +x "$TMP/bin/st"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

before=$(cksum "$TMP/bin/st")
if output=$(HOME="$TMP/home" PATH="$TMP/bin:$PATH" \
  ST_TEST_BUNDLE="$TMP/st.tar.gz" ST_RELEASE_ROOT=https://example.invalid/supertree \
  ST_TEST_SUM=0000000000000000000000000000000000000000000000000000000000000000 \
  "$TMP/bin/st" --version 2>&1); then
  fail 'legacy upgrade with a checksum mismatch unexpectedly succeeded'
fi
grep -q 'does not match its published checksum' <<<"$output" ||
  fail "checksum mismatch did not explain itself: $output"
[ "$(cksum "$TMP/bin/st")" = "$before" ] || fail 'checksum mismatch replaced st'
[ ! -e "$TMP/bin/st-lib-v2.0.0" ] || fail 'checksum mismatch installed modules'
[ -z "$(find "$TMP/bin" -name '.st*')" ] || fail 'checksum mismatch left staged files'

output=$(HOME="$TMP/home" PATH="$TMP/bin:$PATH" \
  ST_TEST_BUNDLE="$TMP/st.tar.gz" ST_RELEASE_ROOT=https://example.invalid/supertree \
  "$TMP/bin/st" --version)
[ "$output" = 'st v2.0.0' ]
[ -f "$TMP/bin/st-lib-v2.0.0/00-core.sh" ]
grep -q 'st_lib_dir=' "$TMP/bin/st"

printf 'ok: legacy single-file upgrade to modular bundle\n'
