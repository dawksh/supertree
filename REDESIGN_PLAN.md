# supertree redesign — staged plan

Source design: the "supertree — terminal UI" design canvas, 7 boards:

| Board | Covers |
|---|---|
| 01 | Visual language (colour roles, glyphs, message rows, prompts) |
| 02 | M-w tree picker |
| 03 | New tree flow |
| 04 | Delete, refusals, trust |
| 05 | Status bar & toasts |
| 06 | CLI output: ls, doctor, help, config |
| 07 | Tree sidebar (M-s) |

Current-state reference: `DESIGN_SCREENS.md`.

Each stage stays within 5–7 non-test files, is one PR, and can ship on its own.

**Dependency order:** 1 → 2 → 3, then 4 and 5 in either order, then 6, then 7. Stage 8 last.

---

## Stage 0 — Decisions before any code

Places where the design disagrees with the code or carries a risk.

1. **Script output format.** ✅ **Decided: follow the design.** Piped (non-terminal) `st ls` prints tab-separated rows, no header: `label	session	agent	changes	type` (e.g. `webauth/feat-otp	open	input	modified	worktree`). This changes today's aligned-table output, so call it out in the stage 2 PR and release notes (consider the `release:minor` label). `st status` already is TSV and tests check it exactly (`tests/test_worktree_qol.sh:118-123`), so it stays unchanged.
2. **Background updates (boards 05, 02).** ✅ **Decided: tmux runs `st` every 2s.** No watcher daemon and no fzf `--listen` poller. tmux calls `#(st _bar)` via `status-interval 2`; previous per-tree state lives in a tmux option so a change to `input` triggers a toast. The picker refreshes on an fzf timer bind that reloads `st _rows`.
3. **`✓ done` = "success" (board 01).** Wrong meaning: `done` only means the agent exited, possibly after a crash. Use a neutral glyph or drop "success".
4. **Board 04 trust diff uses config names that don't exist** (`ST_INSTALL`, `ST_LINK`, `st_post_create()`). Real names: `ST_INSTALL_CMD`, `ST_LINK_DIRS`, `ST_POST_CREATE`. Fix the board copy.
5. **`st config` "base: main"** is a per-repo `.supertree` setting. A global settings screen writing repo files mixes scopes. ✅ **Decided: drop it from `st config`.** Base branch stays in each repo's `.supertree`; the new-tree flow's `tab` toggle (stage 5) covers one-off overrides.
6. **Minimum tool versions.** ✅ **Checked against the changelogs** ([fzf CHANGELOG](https://github.com/junegunn/fzf/blob/master/CHANGELOG.md), [tmux CHANGES](https://github.com/tmux/tmux/blob/master/CHANGES)):

   | Feature | First version |
   |---|---|
   | fzf `--preview-window '…,<90(hidden)'` | 0.31.0 |
   | fzf `--border-label`, `--preview-label`, `start` event | 0.35.0 |
   | fzf `--listen`, `load` event | 0.36.0 |
   | fzf `become(...)` | 0.38.0 |
   | fzf `transform-header(...)` | 0.40.0 |
   | fzf `--info=inline-right` | 0.42.0 |
   | fzf `--no-input` | 0.59.0 |
   | fzf `every(N)` timer event | **0.73.0** |
   | tmux `display-popup` | 3.2 |
   | tmux `display-message -d` | 3.2 |
   | tmux `display-popup -b -T -s -S -e` | 3.3 (`-e` already used today, so 3.3 is the real floor now) |
   | tmux `#{=N:…}` truncation | 2.2 (ellipsis/marker form not confirmed — check before stage 6) |

   **Decided:** require **fzf ≥ 0.42** and **tmux ≥ 3.3**. The picker's 2s refresh is on only when fzf ≥ 0.73; older fzf gets a static snapshot (today's behaviour). The status bar refreshes through tmux regardless. `st doctor` and the installer check these (stage 7).
7. **New keys `M-s` / `M-S`** (sidebar). Add to the uninstall unbind list (`bin/st-lib/90-maintenance.sh`). ⚠️ **Conflict:** the owner's own tmux config already binds `M-s` (and `M-j`). Pick a different sidebar key or make it configurable before stage 8.

**Status:** 1, 2, 5 decided. 3, 4 are design-copy fixes. 6 is a lookup before stage 3. 7 lands with stage 8.

---

## Stage 1 — Shared UI module (board 01)

- New `bin/st-lib/05-ui.sh`: colour roles, glyphs, `NO_COLOR` and `ST_ASCII` support, `ui_row/ok/info/warn/fail`, "try" hint row, prompts whose default follows risk (additive → Yes, destructive → No, irreversible → typed name).
- `00-core.sh`: `info` / `die` route through the UI module. When output isn't a terminal, fall back to exactly `:: msg` / `st: msg`, so tests and scripts are unchanged.
- **Files:** `05-ui.sh`, `00-core.sh`.
- **Size:** small. Every later stage builds on it.

## Stage 2 — Shared tree-row data and `st ls` (boards 02, 06)

- `st _rows` becomes the single source of rows: status, label, ⌂ for main, ± uncommitted count, ↑ unpushed count, repo group. Sort: trees waiting for input first, then most recent.
- `st ls` grouped by repo with a summary footer in a terminal. Piped: headerless TSV per decision 1 (breaking change, note in PR).
- `st status` output unchanged; colour only in a terminal.
- **Files:** `60-picker.sh` (`picker_rows` → `_rows`), `70-worktrees.sh`, `40-agent.sh` (also extract the agent's question line for preview and sidebar).
- **Risk:** `git status` + `git log` per tree slows down with many trees. Time it; maybe a short cache.

## Stage 3 — Picker (board 02)

- fzf with key-hint header, border label counting trees that need you, preview pane (`st _preview`: path, changes, unpushed commits, agent's last lines) hidden below 90 columns, `^n` new, `^r` repos, `^/` toggle preview.
- `^d` deletes in place and reloads; picker stays open.
- Refresh every 2s (decision 2), fzf ≥ 0.73 only. ⚠️ Found in testing: with `--track --id-nth`, fzf drops keys while a reload runs, and `st _rows` takes ~0.5s. So a background loop owned by the picker rebuilds rows into a temp file every 2s, and fzf's `every(2)` only `cat`s that file. The loop exits with the picker, or by itself if the picker is killed. A change can take up to ~4.5s to show.
- **Files:** `60-picker.sh`, `05-ui.sh`.
- **Needs:** decision 6 done.

## Stage 4 — Popups that wait, delete, force delete, trust diff (board 04)

- `ui_hold`: on error a popup waits for a key before closing. Fixes "flashes and disappears".
- `st rm --popup`: summary rows, says up front whether the branch will be deleted; refusal screen offers ⏎ open / `^f` force / esc. Force delete needs the branch name typed.
- Main checkout or detached tree: clear message plus "try" hint.
- Trust: keep a copy of the trusted `.supertree` (`trust/<id>.sh`), show a diff when it changes (full file the first time); keys `t` trust / `v` open in editor. `st trust --show` gives the same view in a plain shell.
- **Files:** `70-worktrees.sh`, `20-repository.sh`, `60-picker.sh`, `05-ui.sh`, `tmux/supertree.conf` (popup title and border).

## Stage 5 — New-tree flow (board 03)

- One two-step popup for `M-r`, `^n` in the picker, and `+ new tree`: **repo first** (current repo preselected, skipped if only one), then a branch list (local + origin branches, or type a new name), `tab` for base, `^b` for bare. Names checked with `git check-ref-format`.
- Creation as step rows: spinner, elapsed time, last 3 lines of install output. On failure the popup stays open with ⏎ open anyway / `r` retry hook.
- "Esc to finish in background, toast when ready" depends on stage 6 — **not in this PR.**
- **Files:** `30-bootstrap.sh`, `70-worktrees.sh`, `60-picker.sh`, `05-ui.sh`.

## Stage 6 — Status bar and toasts (board 05)

- Driven by `#(st _bar)` with `status-interval 2` (decision 2): sets per-session `@st_*` options, counts, and fires toasts on transitions. No daemon.
- `status-left`: status glyph + repo/branch, long branch names middle-truncated. Agent tab gets ◆ when it needs you.
- Right side ("◆ 2 need you · M-w") opt-in via `ST_STATUS_RIGHT=1`; user's tmux theme untouched otherwise.
- Toasts replace raw `run-shell` messages from `M-e`, `M-1..3`, `M-Tab`, plus a toast when a tree switches to input.
- Optional `ST_NOTIFY=1` desktop notifications.
- Then add stage 5's background-finish toast.
- **Files:** `tmux/supertree.conf`, `50-tmux.sh`, `40-agent.sh`, `install.sh` (writes the opt-in), `90-maintenance.sh` (uninstall resets new options).

## Stage 7 — doctor, help, `st config` (board 06)

- `st doctor`: installer-style check rows, a "fix" line per problem (e.g. `brew install fzf`), problem count, version checks from decision 6.
- `st help` grouped by task (trees / setup / keys). Text still from `st_register_command`, plus a group field.
- `st config`: fzf settings picker (harness, windows as ordered multi-select, status bar, notify). No base-branch row (decision 5). Writes back to the config file; offers "apply to N open trees" (close + resume).
- **Files:** `90-maintenance.sh`, `bin/st` (help groups), config command (likely in `90-maintenance.sh`), `install.sh` (shared tokens).

## Stage 8 — Sidebar (board 07), largest and riskiest

- **8a — drawer mode first** (`ST_SIDEBAR=drawer`): popup docked left, opened with `M-s`. Simple; reuses `_rows`.
- **8b — pinned full/rail mode** as a follow-up PR: one pane moved into the active window via `join-pane`, driven by session/window-change and resize hooks; `M-1..3` skip it; `st down` moves it out first. Edge cases: multiple attached terminals, closing the last session, resizing.
- Before 8b: short spike confirming cross-session `join-pane` behaves as the design assumes.
- **Files:** new `bin/st-lib/65-sidebar.sh`, `tmux/supertree.conf`, `50-tmux.sh`, `90-maintenance.sh` (unbind `M-s` / `M-S`).

---

## Cross-cutting

- **Installer:** runs standalone from `curl`, can't load the UI module. Copy the same tokens in and keep them in sync by hand, or check it in the release script.
- **README:** updated in each stage's PR for the commands and keys that stage changes.
- **Tests:** no new tests unless asked. Existing tests run with non-terminal output, so stage 1's plain fallback keeps them passing. Run the full suite after each stage.
