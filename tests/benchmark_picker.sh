#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BENCH_ROOT=$(mktemp -d)
trap 'rm -rf "$BENCH_ROOT"' EXIT
export HOME="$BENCH_ROOT/home"
export ST_STATE="$BENCH_ROOT/state"
export ST_CONFIG="$BENCH_ROOT/config"
export ST_WORKTREE_ROOT="$BENCH_ROOT/worktrees"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$ST_WORKTREE_ROOT"
printf 'ST_HARNESS=codex\n' > "$ST_CONFIG"
printf '#!/bin/sh\ncat >/dev/null\nexit 130\n' > "$HOME/.local/bin/fzf"
chmod +x "$HOME/.local/bin/fzf"
printf '#!/bin/sh\nexit 0\n' > "$HOME/.local/bin/tmux"
chmod +x "$HOME/.local/bin/tmux"

BENCH_REPOS=${BENCH_REPOS:-8}
BENCH_BRANCHES=${BENCH_BRANCHES:-4}
for i in $(seq 1 "$BENCH_REPOS"); do
  repo="$BENCH_ROOT/repos/repo$i"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" -c user.name=Bench -c user.email=bench@example.com \
    commit -q --allow-empty -m initial
  printf '%s\n' "$repo" >> "$ST_STATE/repos"
  for j in $(seq 1 "$BENCH_BRANCHES"); do
    git -C "$repo" worktree add -q -b "branch$j" "$ST_WORKTREE_ROOT/repo$i/branch$j"
  done
done

python3 - "${ST_BENCH_BIN:-$ROOT/bin/st}" "$BENCH_REPOS" "$BENCH_BRANCHES" <<'PY'
import os
import statistics
import subprocess
import sys
import time

st = sys.argv[1]
repos, branches = map(int, sys.argv[2:4])
for name, args in (("picker", ["go"]),
                   ("tree discovery", ["_sessions"]),
                   ("popup dispatch", ["go", "--popup", "/dev/pts/0"])):
    samples = []
    for _ in range(8):
        start = time.perf_counter()
        result = subprocess.run([st, *args], stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, env=os.environ.copy())
        if result.returncode:
            raise SystemExit(f"{name} failed: {result.returncode}")
        samples.append((time.perf_counter() - start) * 1000)
    print(f"{name}: median {statistics.median(samples):.1f} ms; "
          f"range {min(samples):.1f}-{max(samples):.1f} ms "
          f"(8 runs, {repos} repos, {repos * branches} worktrees)")
PY
