# supertree — Screen & Flow Inventory (Design Brief Input)

This document describes **every screen, prompt, message and flow** that exists in supertree today, so a designer (human or LLM) can produce a new design for it. It describes the current behaviour as built. It does not propose a new design, except for the "Constraints" and "Rough edges" sections at the end, which point out what a redesign needs to respect or could fix.

All names and paths in examples are placeholders (`webauth`, `feat-otp`, `~/projects/...`).

---

## 0. What the product is (read this first)

**supertree** (command: `st`) is a terminal tool for developers who work on several branches of the same git repository at once, each with an AI coding agent running in it.

For each branch it creates:

1. A **git worktree** — a separate folder holding that branch, so branches don't fight over one checkout.
2. A **tmux session** — a persistent terminal workspace that keeps running in the background when you leave it.
3. Inside that session, a set of **windows** (tabs): by default an **agent** window (Claude Code, Codex, OpenCode, or a custom agent), a **vim** (Neovim) window, and a **shell** window.

The user calls one such branch-workspace a **tree**. The whole product is about creating trees, jumping between them, seeing what the agent in each tree is doing, closing them, bringing them back, and deleting them.

### Where the UI lives

There is **no GUI**. Every screen is one of these surfaces:

| Surface | What it is | Where it appears |
|---|---|---|
| **CLI output** | Text printed by `st <command>` in a normal shell | User's terminal |
| **Inline prompts** | `[y/N]` confirmations and `branch: ` text input read from the keyboard | Same terminal, or inside a popup |
| **fzf pickers** | Fuzzy-search list (fzf) with a search box, arrow-key selection | Full terminal, or inside a tmux popup |
| **tmux popups** | Floating box drawn over the current tmux screen, 80% wide × 70% tall | Over whatever tree the user is in |
| **tmux status bar** | One-line bar at the bottom of tmux; left side shows current tree | Always visible inside tmux |
| **tmux windows** | Tabs inside a session (agent / vim / shell) | Inside tmux |
| **Keyboard shortcuts** | Global Alt/Option key bindings inside tmux | No visual, they trigger the above |
| **Installer** | Styled, coloured, step-by-step script output | Terminal, once at install time |

### Visual vocabulary used today

- **CLI info line**: dim `::` prefix then plain text. Example: `:: linked node_modules -> main checkout`
- **CLI error line**: `st: <message>` on stderr, then the command exits. No colour.
- **Confirmation**: plain `proceed? [y/N] ` — default is **No**.
- **Text input**: plain `branch: ` with the cursor after it.
- **Installer** is the only place with colour, glyphs and layout (cyan, bold, dim, green ✔, red ✘, yellow !). See §1.
- **Agent status words** (shared across picker, `ls`, `status`): `running`, `input`, `done`, `closed`. See §2.

### Core terms (glossary for the designer)

| Term | Meaning |
|---|---|
| Tree | One branch + its worktree folder + its tmux session. Shown as `repo/branch`, e.g. `webauth/feat-otp`. |
| Main tree / main checkout | The original repo folder (`~/projects/webauth`). Also listed as a tree. Cannot be deleted by st. |
| Linked worktree / subtree | A tree st created under `~/projects/.worktrees/...`. |
| Root / repo | A repository st knows about (registered the first time you run `st new` in it). |
| Harness / agent | The AI coding tool in the agent window: `claude`, `codex`, `openrouter` (OpenCode), or custom. |
| Session open / closed | Whether the tmux session for the tree is currently running. Closing does not delete anything. |
| Origin session | The tmux session you were in when you jumped to a tree; `M-q` takes you back there. |
| `M-` | Meta = Option (macOS) / Alt key. `M-w` = Option+W. |

---

## 1. Installer (first-run experience)

Run with `curl -fsSL .../install.sh | bash`. Only screen with real visual styling. Fixed width 56 chars for rules.

### 1.1 Title banner

```
   ___  _   _  ___  ___  ___  _____  ___  ___  ___          (cyan, bold)
  / __|| | | || _ \| __|| _ \|_   _|| _ \| __|| __|
  \__ \| |_| ||  _/| _| |   /  | |  |   /| _| | _|
  |___/ \___/ |_|  |___||_|_\  |_|  |_|_\|___||___|

  worktrees × tmux × coding agents   installer v0.x.y     (dim)
  ────────────────────────────────────────────────────────  (dim rule)
```

### 1.2 Step layout pattern

Each step: cyan number, bold title, dim one-line description, blank line, then indented rows.

```
  01  Workspace
      Choose what every new tree opens.
```

Row pattern: `      <glyph>  <name padded to 10>  <dim detail>`
Glyphs: green `✔` ok · red `✘` missing/failed · yellow `!` warning · dim `…` in progress (overwritten in place on a real terminal).

### 1.3 Step 01 — Workspace (harness choice)

Shown only if no harness is set by env var or existing config, and a keyboard is available:

```
      1  Claude Code (default)
      2  Codex
      3  OpenRouter (via OpenCode)

      choose harness [1] _
```

- Accepts `1/2/3` or the names `claude/codex/openrouter`. Enter = 1.
- Invalid input → red error `✘ unknown harness choice: x` and the installer exits.
- Window layout is **not** asked; defaults to `agent vim shell` unless already in config or `ST_WINDOWS` env var set.

Then confirmation rows:

```
      ✔  harness    Claude Code
      ✔  windows    agent · vim · shell
```

### 1.4 Step 02 — Dependencies

```
  02  Dependencies
      Checking the tools this workspace needs.

      ✔  tmux       tmux 3.4
      ✔  git        git version 2.45.0
      ✘  fzf        not found — tree picker (M-w)
      ✔  nvim       NVIM v0.10.0
      ✘  claude     not found — selected agent harness (M-1)
```

Dependency list = `tmux git fzf` + `nvim` if vim window enabled + the agent binary if agent window enabled. Each missing one has a "why" text:

| dep | why text |
|---|---|
| tmux | required for sessions and windows |
| git | required for worktrees |
| fzf | tree picker (M-w) |
| nvim | editor window (M-2) |
| claude / codex | selected agent harness (M-1) |
| opencode | OpenRouter agent harness (M-1) |

Branches when something is missing:

- **No package manager found** → yellow warnings: `2 missing, and no supported package manager found`, `install them yourself, then re-run this installer`, and on macOS `on macOS: https://brew.sh`.
- **Package manager found** → prompt `install 2 missing package(s) with brew? [Y/n]` (default **Yes** here, unlike the rest of st).
  - Yes → per package: `… installing fzf` (dim) replaced by `✔ installed fzf` or `✘ failed fzf — see log below`. If any still missing: `! still missing: fzf` then last 12 lines of install log, dim and indented.
  - No → `! skipped — supertree installs anyway, some features stay dark`
- **No keyboard available** (piped, CI) → `! no terminal to ask on; skipping`.
- `ST_YES=1` → answers yes automatically.

### 1.5 Step 03 — Install

```
  03  Install
      Putting supertree in the right places.

      ✔  command    ~/.local/bin/st
      ✔  bindings   ~/.config/supertree/supertree.conf
      ✔  harness    Claude Code · ~/.config/supertree/config
      ✔  windows    agent · vim · shell
      ✔  tmux config  ~/.tmux.conf (backup: .pre-supertree)     (or: "already wired up")
      ✔  tmux         reloaded                                   (only if tmux is running)

      !  ~/.local/bin is not on your PATH                        (only if so)
         export PATH="~/.local/bin:$PATH"                         (dim)
```

Possible fatal errors here (red, exits): could not download, archive has unexpected paths, does not look like a script, missing modules, invalid shell syntax, invalid version metadata, could not install modules/command.

### 1.6 Step 04 — Ready

```
  ────────────────────────────────────────────────────────
  04  Ready
      supertree v0.x.y is installed.

      st doctor          verify the install
      st new <branch>    start from any git repo
      opencode           run /connect and select OpenRouter once   (OpenRouter only)

      M-w picker   M-e toggle   M-q / M-Q leave tmux
  ────────────────────────────────────────────────────────
```

---

## 2. Shared concept: Agent status

Shown in the tree picker (§5), `st ls` (§8) and `st status` (§9). The designer should treat this as a reusable status badge.

| Value | Meaning | How it's detected |
|---|---|---|
| `running` | Agent program is active and working | Agent pane marked running, or pane's current program is the agent / node / bun |
| `input` | Agent is waiting for the user (prompt or yes/no question) | Last 8 lines of the agent pane end in a `›` / `❯` prompt or a `[y/N]` question. Best-effort guess. |
| `done` | Session is open but the agent has exited (window fell back to a shell, or was closed) | Pane marked done, or no agent pane |
| `closed` | The tmux session is not running at all | No session |

`st ls` uses `-` instead of a status when the session is closed (the SESSION column already says `closed`).

`input` is the most important state for the user: it means "go look at this tree, the agent needs you". Today it has **no** colour, icon, sound or notification — it is just the word.

---

## 3. Help screen — `st`, `st help`, `st -h`, `st --help`

Plain text, no colour. Command list is generated from installed modules (a module can add its own command and it shows up here automatically).

```
st — supertree: worktree + tmux + coding-agent harness

  down [branch|--subtrees|--all]              close sessions; keep worktrees
  last                                        switch to the previous tree
  go [query]                                  pick a tree and switch to it
  resume [query]                              reopen a tree and resume its agent
  roots                                       pick a known repo and create a tree in it
  new <branch> [--from <base>] [--bare]       create a worktree and open its session
  trust                                       trust a reviewed .supertree config
  ls                                          show every tree in a readable grid
  status [tree]                               show agent status
  rm <branch> [--force]                       remove a worktree and its session
  remove all [--force]                        remove Git checkouts under the worktree root
  update                                      install the latest release
  uninstall                                   remove the command, config, bindings, and state
  doctor                                      check the installation
  version                                     show the installed version
keys (inside tmux)
  M-w   tree picker          M-e   toggle first <-> second configured window
  M-Tab switch to the previous tree
  M-1 / M-2 / M-3           select configured window by position
  M-q         back to the session st was opened from
  M-Q         leave tmux; keep the tree session running
```

(Order follows module file order: tmux → picker → worktrees → maintenance.)

- `st --version` / `st -V` / `st version` → `st v0.x.y` (or `st dev` for source installs).
- Unknown command → `st: unknown command: foo (try: st help)`.

---

## 4. Create a tree — `st new <branch>`

The main "create" flow. Run from inside any git repo (or via the `+ new branch…` picker row, or `M-r`).

### 4.1 Inputs

| Input | Required | Meaning |
|---|---|---|
| `<branch>` | yes | Branch name. If it exists, reuse it; otherwise create it. |
| `--from <base>` | no | Branch off this instead of current HEAD / configured base. |
| `--bare` | no | Only create the worktree folder; skip deps, env files, hooks and the tmux session. Prints the path. |
| `--repo <path>` | internal | Used by pickers so you don't need to `cd` into the repo. |

### 4.2 Step sequence (what the user sees)

1. **Trust check** (only if repo has a `.supertree` config file). If the file is new or changed since last `st trust`:
   `st: untrusted or changed ~/projects/webauth/.supertree; review it, then run 'st trust' in that repository` → stops. (See §12.)
2. **Repo registered** silently (so it appears in pickers from now on).
3. **Worktree**:
   - Already exists and was made by st → `:: worktree already exists: webauth/feat-otp` and continues (acts like "open").
   - Exists but checked out somewhere st didn't make → `st: feat-otp is already checked out outside supertree: <path>` → stops.
   - Target folder already taken → `st: path already exists: <path>` → stops.
   - New branch → optional **pre-create hook** (`:: pre-create hook` + hook output). If hook fails: `st: pre-create hook failed; no worktree created`.
4. **Bootstrap** (skipped with `--bare`). Each prints an info line:
   - `:: linked node_modules -> main checkout` (when the lockfile matches main)
   - `:: lockfile differs from main, running: npm ci` then the install's own output scrolls (can take minutes; no progress UI of its own)
   - `:: copied .env` / `:: copied .env.local` (one per file)
   - `:: post-create hook` + hook output
5. **Session built** (windows created per config; first window focused).
6. **Switch**: if already in tmux, the screen switches to the new tree instantly. If outside tmux, tmux attaches and takes over the terminal.

With `--bare`: `:: bare: skipped deps, env and agent` then the path on its own line (meant for scripts).

Errors: `st: usage: st new <branch> [--from <base>] [--bare]`, `st: unknown flag: --x`, `st: not inside a git repository`, `st: cannot find a session name for <branch>`.

### 4.3 Where the user lands

Inside the new tree session, on the first configured window (default: agent window, with a fresh agent conversation). See §6.

---

## 5. Tree picker — `M-w` (also `st go`, `st resume`)

**The central screen of the product.** The fastest way to see all trees and jump between them.

### 5.1 How it opens

| Trigger | Surface |
|---|---|
| `M-w` inside tmux | tmux **popup**, 80% width × 70% height, centered over the current screen, closes itself when done |
| `st go` / `st resume` with no query in a shell | fzf takes the full terminal |
| `st go <query>` / `st resume <query>` | **No picker** — jumps straight to the first tree whose row contains the query (case-insensitive). `st: no tree matching: x` if none. |

`go` and `resume` are the same command with two names ("resume" reads better after closing a tree).

If no trees exist at all: `st: no worktrees known yet — run 'st new <branch>' inside a repo` (in a popup this flashes and closes).

### 5.2 Layout

fzf, list reversed (search box on top, first item right under it), bordered, border title ` Trees `, no preview pane.

```
╭──────────────────────────── Trees ─────────────────────────────╮
│ Find tree › _                                                  │
│   4/4                                                          │
│ > webauth/feat-otp       input                                 │
│   webauth/main           running                               │
│   fe/crypto-monitor      done                                  │
│   fe/main                closed                                │
│   + new branch…                                                │
╰────────────────────────────────────────────────────────────────╯
```

Each row = **tree label** left-aligned and padded to the longest label + 2 spaces + **agent status** word (§2). Hidden columns per row: worktree path, session name (not displayed, used on select).

**Row order**: most-recently-opened trees first (from a recent-list kept by st), then trees never opened in list order (per repo: main checkout first, then linked worktrees). Search typing filters but does not re-sort.

**Last row**: `+ new branch…` — always present (unless the worktree module is removed).

Rows include main checkouts (e.g. `webauth/main`) and linked worktrees together; there is no visual difference between them today. Trees from different repos are mixed in one list.

Label naming: `repo/branch`. If two repos share a folder name, the repo part gets a short hash (`webauth-a1b2c3/main`). Detached checkouts show `(detached)` as branch.

Status is a snapshot taken when the picker opened; it does not live-update while open.

### 5.3 Keys inside the picker

| Key | Action |
|---|---|
| type | Fuzzy-filter rows |
| ↑ / ↓ (fzf defaults: Ctrl-K/J, Ctrl-P/N) | Move selection |
| Enter on a tree | Open it (§5.4) |
| Enter on `+ new branch…` | New-branch flow (§5.5) |
| Ctrl-D on a tree | Delete flow (§5.6) |
| Ctrl-D on `+ new branch…` | Nothing, closes |
| Esc / Ctrl-C | Close picker, nothing happens |

No on-screen hint shows these keys today.

### 5.4 Open a tree (Enter)

- If the tree's session is closed it is rebuilt first (windows recreated; agent window **continues its previous conversation** if one exists).
- Recent list updated; origin session remembered (for `M-q`).
- Screen switches to that tree. Popup disappears.

### 5.5 New branch from picker (`+ new branch…`)

Inside the same popup, fzf disappears and a raw text prompt appears:

```
branch: _
```

- Empty + Enter → closes, nothing happens.
- If st knows **more than one repo**, a second fzf list appears (no border, prompt `repo> `, rows are absolute repo paths). Esc closes. If only one repo, it's used automatically.
- Then runs §4 (`st new`) inside the popup: info lines scroll in the popup, then the screen switches to the new tree.

Note: the repo is chosen **after** the branch name, and the repo list shows full paths (unlike `M-r`, which shows `~/` paths).

### 5.6 Delete a tree from picker (Ctrl-D)

Runs the same guarded removal as `st rm` (§11) inside the popup.

- Main checkout selected → `st: cannot delete the main worktree` (popup closes immediately, message barely visible).
- Detached checkout → `st: cannot delete a detached worktree from the picker`.
- Uncommitted / unpushed work → lists them, then `st: refusing to remove feat-otp — commit/push, or pass --force` (popup closes immediately). No way to force from the picker.
- Otherwise shows the `will remove:` summary and `proceed? [y/N]` (§11.2) in the popup.

---

## 6. Inside a tree (the tmux session)

This is where the user spends most time. supertree does not draw this screen; it configures it.

### 6.1 Session anatomy

```
┌───────────────────────────────────────────────────────────────┐
│                                                               │
│   (active window content: the agent, nvim, or a shell)        │
│                                                               │
│                                                               │
├───────────────────────────────────────────────────────────────┤
│ webauth/feat-otp  0:claude* 1:vim 2:shell                     │  ← tmux status bar
└───────────────────────────────────────────────────────────────┘
```

- **Status bar, left**: the tree label `repo/branch` (up to 60 chars). This is the only always-on "where am I" indicator. If not a st session, shows the plain tmux session name.
- **Status bar, rest**: user's own tmux theme — st doesn't style it (no colour, no agent status, no other trees shown).
- **Windows** (tabs): configured by `ST_WINDOWS`, any order, 1–3 of:

| Type | Window name | Runs | When the program exits |
|---|---|---|---|
| `agent` | the harness name (`claude`, `codex`, `openrouter`, or custom like `aider`) | the agent; continues the previous conversation if this tree has run this agent before | drops to an interactive shell in the worktree; status becomes `done` |
| `vim` | `vim` | `nvim` | drops to a shell |
| `shell` | `shell` | user's shell | — |

Common layouts: `agent vim shell` (default), `agent shell` (external editor like VS Code), `vim shell` (no agent), `shell`.

Session names are `repo/branch` (dots/colons in branch become `_`).

### 6.2 Keyboard shortcuts inside any tmux session

All are global (no prefix key). Nothing visual unless noted.

| Key | Action | Feedback |
|---|---|---|
| `M-w` | Open tree picker popup (§5) | popup |
| `M-r` | Open roots picker popup (§7) | popup |
| `M-e` | Toggle between window 1 and window 2 of the config (e.g. agent ↔ vim). From any other window → go to window 1. | window switches. If fewer than 2 windows configured: message `:: toggle needs at least two configured windows` |
| `M-1` / `M-2` / `M-3` | Go to configured window by position. If that window was closed, it is recreated (e.g. restart agent). | window switches. Messages: `:: no window configured for slot 3`, `:: vim window is not enabled in ST_WINDOWS` |
| `M-Tab` | Jump to the previously used tree; press again to bounce back. Rebuilds it if closed. | screen switches. `:: no other tree opened yet` if none |
| `M-q` | Go back to the session you were in before jumping into trees. If you started from a plain terminal, detaches (leaves tmux). | screen switches or tmux disappears |
| `M-Q` | Always leave tmux (detach). Everything keeps running. | tmux disappears, back to plain terminal |

Messages from these keys appear as tmux's own run-shell output overlay (unstyled), which most users will find jarring.

Prefix key `C-a`, `M-arrow` pane navigation and `S-Enter` are the user's own and left alone.

---

## 7. Roots picker — `M-r` (also `st roots`)

"Create a tree in any repo without `cd`-ing to it."

### 7.1 Layout

Popup (80% × 70%) when from `M-r`; full terminal from `st roots`. fzf, bordered, border title ` Roots `, prompt `Find root › `.

```
╭──────────────────────────── Roots ─────────────────────────────╮
│ Find root › _                                                  │
│   3/3                                                          │
│ > ~/projects/fe                                                │
│   ~/projects/webauth                                           │
│   /opt/work/infra                                              │
╰────────────────────────────────────────────────────────────────╯
```

Rows: every registered repo whose folder still exists, sorted alphabetically, home shown as `~`. No branch count, no status, no recency.

### 7.2 Flow

1. Pick root (Enter). Esc closes.
2. Raw prompt `branch: _`. Empty → closes.
3. Runs `st new <branch>` in that repo (§4); output shows in the popup, then switches to the new tree.

Empty state: `st: no roots known yet — run 'st new <branch>' inside a repo`.

---

## 8. List view — `st ls`

Plain text table in the terminal. Read-only; no selection.

```
TREE                SESSION  AGENT    CHANGES   TYPE
------------------  -------  -------  --------  --------
webauth/main        open     running  clean     main
webauth/feat-otp    open     input    modified  worktree
fe/main             closed   -        clean     main
fe/crypto-monitor   open     done     modified  worktree
```

| Column | Values |
|---|---|
| TREE | `repo/branch` label |
| SESSION | `open` / `closed` |
| AGENT | `running` / `input` / `done` / `-` (closed) |
| CHANGES | `modified` (uncommitted changes) / `clean` |
| TYPE | `main` / `worktree` |

Order: by repo (as registered), main first then linked worktrees. Not by recency (unlike the picker). Empty state: header and dashes only, no rows, no hint. No colours.

---

## 9. Single status — `st status [tree]`

Prints one line: label, tab, status.

```
webauth/feat-otp	input
```

- No tree given inside tmux → current tree. Outside tmux → `st: usage: st status <tree>  (omit tree inside tmux)`.
- Not a st session → `st: <session> is not a supertree session`.
- No match → `st: no tree matching: x`.

Designed for scripts/automation more than humans.

---

## 10. Close trees — `st down`

Closes tmux sessions. **Keeps** the worktree, branch, files and agent history. Unsaved editor buffers are lost.

### 10.1 Variants

| Command | Closes |
|---|---|
| `st down` (inside tmux) | The current tree |
| `st down <branch or label or part>` | That tree (exact label, exact session name, or first partial match) |
| `st down --subtrees` | All open linked-worktree sessions; main checkouts stay open |
| `st down --all` | All open st sessions |
| `-y` / `--yes` | Skip confirmation (bulk variants only) |

### 10.2 Single close — no confirmation

```
:: closed webauth/feat-otp (worktree kept — 'st resume' brings it back)
```

Already closed: `:: webauth/feat-otp already closed`.
Errors: `st: no tree matching: x`, `st: this is not a supertree session; refusing to close it`, `st: usage: st down [branch|--all]  (no branch only works inside a tree session)`.

### 10.3 Bulk close — confirmation

```
will close:
  webauth/feat-otp
  fe/crypto-monitor
unsaved editor buffers are lost. proceed? [y/N] _
```

- Not `y`/`Y`/`yes` → `st: aborted`.
- Done → `:: closed selected tree sessions (worktrees kept)`.
- Nothing open → `:: no matching tree sessions open`.
- `--subtrees` first opens a plain shell session in a repo's main checkout if it wasn't open, so the user has somewhere to land.

### 10.4 What happens to people looking at a closed session

Before closing, anyone viewing it is moved: back to their origin session if it still exists, else the most recently used other session. If it was the last session, tmux exits for them. If you close the tree you're sitting in, that happens last.

---

## 11. Delete a tree — `st rm <branch>`

**Destructive.** Closes session, removes the worktree folder, deletes the branch **only if merged**. Also reachable via Ctrl-D in the picker (§5.6).

### 11.1 Safety refusal (without `--force`)

```
uncommitted changes:
 M src/auth.ts
?? notes.md
unpushed commits:
add otp verify endpoint
wip
st: refusing to remove feat-otp — commit/push, or pass --force
```

### 11.2 Confirmation

```
will remove:
  tree      webauth/feat-otp
  session   close
  checkout  remove
  branch    delete if merged
proceed? [y/N] _
```

- No → `st: aborted`.
- Yes → (session closed, viewers moved per §10.4) → `:: branch feat-otp kept (not merged)` if applicable → `:: removed webauth/feat-otp`.

### 11.3 Errors

`st: usage: st rm <branch> [--force]`, `st: no managed worktree for feat-otp`, `st: cannot remove the main worktree`, `st: no worktree for <branch> at <path>`, `st: an older session for <branch> is still open; close it before removing the tree`, `st: worktree for <branch> changed; refusing to remove <path>`.

---

## 12. Trust repo config — `st trust`

A repo can contain a `.supertree` file (shell script) that customises tree creation: which folders to link (e.g. `node_modules`), which files to copy (e.g. `.env*`), install command, base branch, pre/post-create hooks. Because it runs code, st requires the user to approve it.

Flow:

1. User runs `st new` → blocked with the "untrusted or changed" error (§4.2).
2. User reads the file (`cat .supertree` — st provides no viewer/diff).
3. `st trust` → `:: trusted ~/projects/webauth/.supertree; changes will require trusting it again`.
4. `st new` works. Any edit to the file → blocked again.

Errors: `st: no .supertree in <path>`, `st: usage: st trust`.

---

## 13. Bulk cleanup — `st remove all [--force]`

Wipes everything under the worktree root (`~/projects/.worktrees` by default), including stray standalone repos found there.

```
will remove from ~/projects/.worktrees:
  worktree   webauth/feat-otp
  worktree   fe/crypto-monitor
  repository ~/projects/.worktrees/scratch-repo
proceed? [y/N] _
```

- Standalone repos without `--force` → `st: standalone repositories require --force (their history would be deleted)` (before the prompt).
- Uncommitted/unpushed work without `--force` → `st: refusing to remove webauth/feat-otp with uncommitted or unpushed work; pass --force`.
- Per item: `:: removed webauth/feat-otp`, `:: branch x kept (not merged)`, `:: removed <path>`.
- Nothing there: `:: no worktrees under <root>` / `:: no Git checkouts under <root>`.
- Other safety errors: `unsafe worktree root`, `worktree changed while listing`, `refusing to delete <dir>; linked worktree outside removal list: <path>`, `checkout changed; refusing to remove <dir>`.

---

## 14. Health check — `st doctor`

```
  ok    tmux
  ok    git
  MISS  fzf
  ok    nvim
  ok    claude
  ok    st on PATH (~/.local/bin/st)
  ok    ~/.tmux.conf sources supertree.conf
  --    worktree root ~/projects/.worktrees
  --    repos known   3
  --    harness       Claude Code (claude)
  --    windows       agent vim shell
  --    config        ~/.config/supertree/config
```

Three row kinds: `ok` (pass), `MISS` (fail — exit code 1), `--` (info). No colour (unlike the installer, which shows the same kind of data styled).

---

## 15. Update — `st update`

Single-shot, no prompt.

- Up to date: `:: st v0.5.1 is already the latest version`
- Updated: `:: updated st v0.5.0 -> v0.5.1`
- Errors: `curl is required to update st`, `could not check for updates`, `could not determine the latest release`, `development install detected — update it with git`, `source install detected — update it with git`, `cannot update <path> (directory is not writable)`, download/validation/install failures.

No progress indicator, no changelog shown.

---

## 16. Uninstall — `st uninstall`

```
will uninstall:
  command  ~/.local/bin/st
  bindings ~/.config/supertree/supertree.conf
  config   ~/.config/supertree/config
  state    ~/.local/state/supertree
  tmux     ~/.tmux.conf (Supertree source line)
proceed? [y/N] _
```

Yes → closes all st sessions (moving viewers), removes the tmux line, key bindings, status-bar setting, config, state, modules, command → `:: uninstalled st`. Worktrees and agents are **kept** (user is told in docs to run `st remove all` first if wanted — not on screen).

---

## 17. Internal commands (not user-facing, no screens)

Called by key bindings or by st itself: `st window <n>`, `st agent`, `st toggle`, `st last`, `st leave`, `st go --picker`, `st go --popup <client>`, `st roots --popup <client>`, `st _run`, `st _agent`, `st _claude`, `st _sessions`. Listed so the designer knows they exist; they don't need their own screens.

---

## 18. End-to-end flows

### Flow A — First install
Installer §1 → (optionally) `st doctor` §14 → `cd` into a repo → `st new feat-x` §4 → lands in agent window §6.

### Flow B — Start parallel work on a new branch
Option 1: in a repo shell, `st new feat-y`.
Option 2: inside tmux, `M-w` → `+ new branch…` → type name → pick repo (if several) → lands in tree.
Option 3: inside tmux, `M-r` → pick repo → type name → lands in tree.

### Flow C — Daily juggling between trees
In tree A, agent is working → `M-w` → see tree B shows `input` → Enter → answer agent in B → `M-Tab` → back to A → `M-Tab` → back to B. `M-e` flips agent ↔ editor within a tree.

### Flow D — Step away and come back
`M-q` (back to where you started) or `M-Q` (leave tmux). Everything keeps running. Later: `st resume feat-y` or `M-w`.

### Flow E — Close for the day, resume tomorrow
`st down` (current) / `st down --subtrees` / `st down --all`. Next day: `M-w` or `st resume feat-y` → session rebuilt → agent continues **the same conversation** in that tree (first-ever run in a tree starts fresh; every later run continues).

### Flow F — Agent exited / closed window
Agent exits → window becomes a shell, status `done`. `M-1` (or whatever slot agent is) recreates the agent window if it was closed.

### Flow G — Finish a branch
Merge/push → `st rm feat-y` (§11) or `M-w` → highlight → Ctrl-D. Refuses if unpushed/uncommitted unless `--force` (CLI only).

### Flow H — Repo with custom config
Repo adds `.supertree` → `st new` blocked → user reads file → `st trust` → `st new` runs with linking/copying/hooks → any change to the file → blocked again.

### Flow I — Check status without switching
`st ls` (all trees, table) or `st status <tree>` (one line). Or `M-w` and Esc.

### Flow J — Change agent or window layout
Edit `~/.config/supertree/config` (`ST_HARNESS=codex`, `ST_WINDOWS='agent shell'`) by hand → `st down <tree>` → `st resume <tree>` to rebuild. No settings screen exists.

### Flow K — Full cleanup / removal
`st remove all [--force]` (§13) then `st uninstall` (§16).

---

## 19. Data each screen can draw on

Useful when designing richer screens — everything here is already known or cheap to get:

| Data | Source |
|---|---|
| Repo name, branch, worktree path, session name | git + st state |
| Main vs linked worktree | git |
| Session open/closed | tmux |
| Agent status (running/input/done/closed) | tmux pane check |
| Uncommitted changes (yes/no, and file list) | `git status` |
| Unpushed commits (list) | `git log` |
| Recency order of trees | `~/.local/state/supertree/recent` |
| Tree index number (stable small integer per tree, used for ports) | `~/.local/state/supertree/idx/...` |
| Whether agent has run here before (continue vs fresh) | `~/.local/state/supertree/seen/...` |
| Trust state of `.supertree` | `~/.local/state/supertree/trust/...` |
| Harness name/label, window layout | config file |
| Known repos | `~/.local/state/supertree/repos` |
| Origin session per terminal | `~/.local/state/supertree/origin/...` |

Not available today without new work: last agent message, time since status change, diff size, PR/CI state, branch ahead/behind counts.

---

## 20. Constraints a new design must respect

- **Terminal only.** Everything renders as monospace text with ANSI colour. No images, no mouse required (mouse may be optional).
- **Pickers are fzf.** Any picker design must map onto fzf features: prompt, border + border label, header lines, preview pane, key bindings (`--bind`, `--expect`), ANSI colours in rows (`--ansi`), multi-column via fixed-width padding. Live-reload is possible via `--bind reload(...)`.
- **Popups are tmux `display-popup`.** Size in % of screen; closes the moment the command inside exits — so any message printed right before exit vanishes unless the design adds a pause.
- **Status bar** is tmux's: one line, `#{...}` format strings, can show text and colours; st currently owns only `status-left`.
- **Width**: must work in narrow (~80 col) and wide terminals; labels can be long (`repo-hash/feature/very-long-branch-name`).
- **Colour**: must degrade when `NO_COLOR` is set or output isn't a terminal (installer already does this).
- **Non-interactive use**: `st status`, `st ls`, `st new --bare`, `st doctor` are used by scripts; their plain output format matters.
- **Keyboard**: Alt/Option-based global keys; must not clash with the user's prefix (`C-a`), `M-arrow`, `S-Enter`.

## 21. Rough edges in the current UI (redesign opportunities)

1. Only the installer is styled; every other command is plain text with a different look (`::`, `st:`, `ok/MISS`).
2. `input` status — the most actionable state — is not visually highlighted anywhere and has no notification.
3. Picker shows no key hints (Enter / Ctrl-D / Esc), no legend for statuses, no main-vs-worktree distinction, no repo grouping, no "changes" info, no preview.
4. Errors inside popups (e.g. delete refused, cannot delete main) flash and disappear instantly.
5. `+ new branch…` asks for branch name **before** repo, and the repo list shows raw absolute paths with no border/title — inconsistent with `M-r` (repo first, `~` paths, bordered).
6. Text input `branch: ` is a bare read with no validation, no hint about `--from`, no indication of which repo.
7. `st ls` and the picker use different orderings (registration vs recency) and different columns.
8. Messages from key shortcuts (`M-e`, `M-1`, `M-Tab`) appear as raw tmux run-shell output.
9. The status bar shows only the label — no agent status, no count of trees needing input.
10. No settings screen; changing agent/windows means editing a config file and rebuilding sessions.
11. Trust flow gives no in-app way to view or diff `.supertree`.
12. Long installs during `st new` (`npm ci`) have no progress framing.
13. Confirmations default to No (`[y/N]`) in st but Yes (`[Y/n]`) in the installer.
14. `st doctor` repeats installer checks without the installer's visuals.
