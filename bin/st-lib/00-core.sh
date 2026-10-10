# shellcheck shell=bash
# supertree core module

die()  { printf 'st: %s\n' "$*" >&2; exit 1; }

info() { printf '\033[2m::\033[0m %s\n' "$*" >&2; }
st_executable() {
  local dir
  case $0 in
    /*) printf '%s' "$0";;
    */*) dir=$(cd "$(dirname "$0")" && pwd); printf '%s/%s' "$dir" "$(basename "$0")";;
    *) command -v -- "$0";;
  esac
}
