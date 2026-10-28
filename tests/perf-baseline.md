# Local command latency baseline

Measured against an isolated archive of commit `a8b694d` with
`bash tests/benchmark_picker.sh` after the full fixture was established.
Each value is the median of eight runs on this Mac, using eight repositories with
four worktrees each. A deterministic tmux stub makes session calls noninteractive;
the numbers measure CLI command latency, not terminal rendering or Git/network
operations during new worktree creation.

| Command path | Baseline median | After median |
| --- | ---: | ---: |
| `st` with an existing home session | 54.2 ms | 55.4 ms |
| `st` creating home | 70.9 ms | 56.6 ms |
| `st open` from a live worktree | 314.9 ms | 58.7 ms |
| `st go repo1/branch1` to a live tree | 250.5 ms | 54.9 ms |
| `st _sidebar-rows` from home with an initialized tree cache | 148.6 ms | 21.1 ms |
| `st go` picker through fzf stub | 135.7 ms | 129.1 ms |
| `st _sessions` tree discovery | 125.1 ms | 116.0 ms |
| `st go --popup` dispatch through tmux stub | 20.9 ms | 21.6 ms |

The benchmark script recreates the fixture each run; compare future medians from
the same script and machine. The picker benchmark drains fzf input, so it includes
row preparation but not the time to draw the popup on screen.

The after column was measured after the live-session, repository, and sidebar
changes in this worktree. Home entry and popup dispatch were already fast; their
small differences are within run-to-run variation. Exact live-tree commands use
the fast path, while fuzzy queries and closed trees still use full discovery.

The initialized sidebar cache has tree rows but no home row, matching the cache
produced by Supertree. The tmux stub also reports one live tree window with its
worktree path for testing the direct-open fast path. The fixture root is
canonicalized before creating repositories so the stub path matches Git's path
on macOS.
