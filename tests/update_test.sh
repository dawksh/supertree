#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

mkdir -p "$TMP/home" "$TMP/fakebin" "$TMP/install"

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
    printf '%s/releases/tag/%s' "${ST_RELEASE_ROOT:?}" "${ST_TEST_LATEST:?}"
    ;;
  */releases/download/*/st.tar.gz)
    cp "${ST_TEST_ASSET:?}" "$out"
    ;;
  */releases/download/*/SHA256SUMS)
    sum=${ST_TEST_SUM:-$(shasum -a 256 "${ST_TEST_ASSET:?}" | awk '{ print $1 }')}
    printf '%s  st.tar.gz\n' "$sum" > "$out"
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

output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_RELEASE_ROOT=https://example.invalid/supertree \
  ST_TEST_LATEST=v1.1.0 ST_TEST_ASSET="$TMP/release.tar.gz" \
  "$installed" update 2>&1)
grep -q 'updated st v1.0.0 -> v1.1.0' <<<"$output"
[ "$("$installed" --version)" = 'st v1.1.0' ]
[ -f "$TMP/install/st-lib-v1.1.0/00-core.sh" ]
[ ! -e "$TMP/install/st-lib-v1.0.0" ] || fail 'successful update left the old st-lib-v1.0.0'

output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_RELEASE_ROOT=https://example.invalid/supertree \
  ST_TEST_LATEST=v1.1.0 ST_TEST_ASSET="$TMP/release.tar.gz" \
  "$installed" update 2>&1)
grep -q 'already the latest version' <<<"$output"

stamp "$ROOT/bin/st" v1.0.0 "$installed"
cp -R "$ROOT/bin/st-lib" "$TMP/install/st-lib-v1.0.0"
before=$(cksum "$installed")
if output=$(HOME="$TMP/home" PATH="$TMP/fakebin:$PATH" \
  ST_RELEASE_ROOT=https://example.invalid/supertree \
  ST_TEST_LATEST=v1.1.0 ST_TEST_ASSET="$TMP/release.tar.gz" \
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
  ST_RELEASE_ROOT=https://example.invalid/supertree \
  ST_TEST_LATEST=v1.1.0 ST_TEST_ASSET="$TMP/bad-asset" \
  "$installed" update >/dev/null 2>&1; then
  printf 'malformed update unexpectedly succeeded\n' >&2
  exit 1
fi
[ "$(cksum "$installed")" = "$before" ]

printf 'update tests passed\n'
