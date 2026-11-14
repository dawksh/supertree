# shellcheck shell=bash
# supertree identity module

slugify() { printf '%s' "$1" | tr '/.: ' '----'; }

# sha256sum first: macOS shasum is a perl script and costs ~2x per call.
sha256_stream() {
  local digest
  if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum)
  elif command -v shasum >/dev/null 2>&1; then digest=$(shasum -a 256)
  elif command -v openssl >/dev/null 2>&1; then digest=$(openssl dgst -sha256)
  else die "a SHA-256 tool (shasum, sha256sum, or openssl) is required"
  fi
  digest=${digest##*= }
  printf '%s\n' "${digest%% *}"
}

identity_hash() {
  local digest
  digest=$(printf '%s' "$1" | sha256_stream)
  printf '%s\n' "${digest:0:32}"
}

config_fingerprint() { sha256_stream < "$1"; }

key_part() {
  # LC_ALL=C makes the substitution per byte, matching the old `tr -c` keys.
  local label identity=${2:-$1} LC_ALL=C
  label=${1//[!A-Za-z0-9_-]/-}
  label=${label:0:48}
  [ -n "$label" ] || label=item
  printf '%s-%s' "$label" "$(identity_hash "$identity")"
}

repo_key() {
  case $1 in
    */|'') key_part "$(basename "$1")" "$1";;
    *) key_part "${1##*/}" "$1";;
  esac
}

branch_key() { key_part "$1"; }

tree_dir()  { printf '%s/%s/%s' "$ST_WORKTREE_ROOT" "$1" "$2"; }

# Folder names keep 5 hash chars so paths stay readable; state keys keep the full hash.
short_key() { printf '%s-%s' "${1%-*}" "$(printf '%s' "${1##*-}" | cut -c1-5)"; }

short_tree_dir() { tree_dir "$(short_key "$1")" "$(short_key "$2")"; }

sess_name() { printf '%s/%s' "$1" "$2"; }

tree_label() { printf '%s/%s' "${1%-*}" "$2"; }
