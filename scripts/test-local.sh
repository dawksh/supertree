#!/usr/bin/env bash
# Local tests must never address the developer's tmux server, even by mistake.
set -euo pipefail

test_repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
test_real_tmux=$(command -v tmux || true)
test_sandbox=$(mktemp -d /tmp/st-local-tests.XXXXXX)
cleanup_test_sandbox() {
  # Only sockets physically inside this runner's private directory are eligible.
  if [ -n "$test_real_tmux" ]; then
    for test_socket in "$test_sandbox"/sockets/*.sock; do
      [ -S "$test_socket" ] || continue
      "$test_real_tmux" -N -S "$test_socket" kill-server >/dev/null 2>&1 || true
    done
  fi
  rm -rf -- "$test_sandbox"
}
trap cleanup_test_sandbox EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Reuse compilation caches without letting test subprocesses read user dotfiles.
if command -v go >/dev/null 2>&1; then
  export GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)"
fi
mkdir -p "$test_sandbox/home" "$test_sandbox/bin" "$test_sandbox/tmp" "$test_sandbox/sockets"
export HOME="$test_sandbox/home" TMPDIR="$test_sandbox/tmp/"
unset TMUX TMUX_PANE TMUX_TMPDIR TMUX_CONF
while IFS= read -r test_var; do
  case $test_var in ST_*) unset "$test_var";; esac
done < <(compgen -e)

if [ -n "$test_real_tmux" ]; then
  {
    printf '#!/usr/bin/env bash\nset -euo pipefail\n'
    printf 'sandbox=%q\nreal_tmux=%q\n' "$test_sandbox" "$test_real_tmux"
    cat <<'GUARD'
requested=''
forward=(-f /dev/null)
while [ "$#" -gt 0 ]; do
  case $1 in
    -S|-L)
      [ "$#" -ge 2 ] || { printf 'test tmux: missing socket argument\n' >&2; exit 2; }
      if [ "$1" = -S ]; then requested=$2
      else requested="label:$2:${TMUX_TMPDIR:-}"; fi
      shift 2;;
    -f|-T|-c)
      [ "$#" -ge 2 ] || exit 2
      forward+=("$1" "$2"); shift 2;;
    -N|-C|-CC|-u|-2|-l|-D|-V|-h|-v|-vv) forward+=("$1"); shift;;
    --) shift; break;;
    -*) printf 'test tmux: unsupported global option: %s\n' "$1" >&2; exit 2;;
    *) break;;
  esac
done
case ${TMUX:-} in
  "$sandbox"/sockets/*) [ -n "$requested" ] || requested=${TMUX%%,*};;
  *) unset TMUX TMUX_PANE;;
esac
[ -n "$requested" ] || requested=default
case $requested in
  "$sandbox"/sockets/*.sock)
    candidate=${requested#"$sandbox/sockets/"}
    if [[ $candidate =~ ^[a-f0-9]{24}[.]sock$ ]]; then target=$requested
    else target=''; fi;;
  *) target='';;
esac
if [ -z "$target" ]; then
  if command -v sha256sum >/dev/null 2>&1; then digest=$(printf '%s' "$requested" | sha256sum)
  else digest=$(printf '%s' "$requested" | shasum -a 256); fi
  target="$sandbox/sockets/${digest:0:24}.sock"
fi
[ ! -L "$sandbox" ] && [ ! -L "$sandbox/sockets" ] && [ ! -L "$target" ] || {
  printf 'test tmux: refusing a symlink in the private socket namespace\n' >&2; exit 2;
}
printf '%s\t%s\n' "$target" "${1:-}" >> "$sandbox/tmux-targets.log"
exec "$real_tmux" -S "$target" "${forward[@]}" "$@"
GUARD
  } > "$test_sandbox/bin/tmux"
  chmod 0755 "$test_sandbox/bin/tmux"
fi
export PATH="$test_sandbox/bin:$PATH"
export SUPERTREE_TEST_SANDBOX="$test_sandbox"

case ${1:-} in
  --) shift; [ "$#" -gt 0 ] || { printf 'test-local: expected a command after --\n' >&2; exit 2; }; "$@";;
  --shell|'')
    if [ "${1:-}" != --shell ]; then
      command -v go >/dev/null || { printf 'Go is required\n' >&2; exit 1; }
      go -C "$test_repo" vet ./...
      go -C "$test_repo" test -race ./...
    fi
    cd "$test_repo"
    bash -n dev
    for test_file in bin/st bin/st-lib/*.sh install.sh tests/*.sh scripts/*.sh; do bash -n "$test_file"; done
    for test_file in tests/*.sh; do bash "$test_file"; done;;
  *) printf 'usage: bash scripts/test-local.sh [--shell | -- command ...]\n' >&2; exit 2;;
esac
