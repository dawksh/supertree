# shellcheck shell=bash
# supertree core module

# The ui module styles these once loaded; before that they print plain lines.
die() {
  declare -F ui_fail >/dev/null && ui_fail "$*"
  printf 'st: %s\n' "$*" >&2
  exit 1
}

info() {
  declare -F ui_info >/dev/null && { ui_info "$*"; return 0; }
  printf '\033[2m::\033[0m %s\n' "$*" >&2
}
st_executable() {
  local dir
  case $0 in
    /*) printf '%s' "$0";;
    */*) dir=$(cd "$(dirname "$0")" && pwd); printf '%s/%s' "$dir" "$(basename "$0")";;
    *) command -v -- "$0";;
  esac
}
