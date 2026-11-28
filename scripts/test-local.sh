#!/usr/bin/env bash
# Local tests must never address the developer's tmux server, even by mistake.
set -euo pipefail

test_repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
test_real_tmux=$(command -v tmux || true)
test_sandbox=$(mktemp -d /tmp/st-local-tests.XXXXXX)
cleanup_test_sandbox() {
  # Native hosts are addressed only through sockets created inside this runner.
  # Ask them to stop owned jobs and wait for their process-held lock, never kill
  # a PID taken from a file or touch the developer's normal host namespace.
  if [ -d "$test_sandbox/native-sockets" ]; then
    if ! python3 - "$test_sandbox" <<'NATIVE_CLEANUP'
import fcntl, json, os, pathlib, socket, stat, struct, sys, time
root = pathlib.Path(sys.argv[1]).resolve()
def read_frame(conn):
    def read(n):
        result = bytearray()
        while len(result) < n:
            chunk = conn.recv(n - len(result))
            if not chunk:
                raise RuntimeError("test host closed before cleanup acknowledgement")
            result.extend(chunk)
        return result
    size = struct.unpack("!I", read(4))[0]
    if not 0 < size <= 8 * 1024 * 1024:
        raise RuntimeError("invalid test host frame")
    return json.loads(read(size))
def send(conn, value):
    data = json.dumps(value).encode()
    conn.sendall(struct.pack("!I", len(data)) + data)
for path in (root / "native-sockets").glob("*.sock"):
    try:
        info = path.lstat()
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid():
            raise RuntimeError("unsafe native test socket")
        namespace = path.stem
        if len(namespace) != 32 or any(c not in "0123456789abcdef" for c in namespace):
            raise RuntimeError("unexpected native test socket")
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(5)
            conn.connect(str(path))
            hello = dict(version=1, namespace=namespace, op="hello")
            send(conn, hello)
            status = read_frame(conn)["status"]
            directory = pathlib.Path(status["directory"]).resolve()
            if not directory.is_relative_to(root) or status["namespace"] != namespace or status["version"] != 1:
                raise RuntimeError("native test host escaped runner scope")
            lock = os.open(directory / "host.lock", os.O_RDWR | os.O_NOFOLLOW)
            try:
                send(conn, dict(hello, op="shutdown", epoch=status["epoch"], force=True))
                if read_frame(conn).get("error"):
                    raise RuntimeError("native test host refused cleanup")
                deadline = time.monotonic() + 30
                while True:
                    try:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        break
                    except BlockingIOError:
                        if time.monotonic() > deadline:
                            raise RuntimeError("native test jobs did not stop")
                        time.sleep(.02)
            finally:
                os.close(lock)
    except (FileNotFoundError, ConnectionRefusedError):
        pass
NATIVE_CLEANUP
    then
      printf 'test-local: native cleanup failed; preserving private test state at %s\n' "$test_sandbox" >&2
      return 1
    fi
  fi
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
