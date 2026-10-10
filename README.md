# supertree

Work on several branches of the same repo at once, each in its own git worktree
and configurable tmux session, with your chosen coding agent and terminal tools
already running.

One key switches between trees, one key flips between your primary windows, and
two keys leave tmux while keeping the tree running. Closing a tree with `st down`
lets you reopen the agent in the same conversation.

```
tmux sessions
  webauth/main       [codex][vim][shell]
  webauth/feat-otp   [codex][vim][shell]
  fe/crypto-monitor  [codex][vim][shell]
```

---

## Install

```sh
curl -fsSL https://github.com/dawksh/supertree/releases/latest/download/install.sh | bash
```

The installer first asks which coding-agent harness to use: Claude Code, Codex,
or OpenRouter via OpenCode. It writes the choice and default window layout to
`~/.config/supertree/config`, checks the dependencies required by that layout,
and offers to install anything missing. Nothing is installed without a yes; if
there is no terminal to answer on, it skips and carries on.

Then it installs `st` and its versioned `st-lib-*` module directory to
`~/.local/bin`, the tmux fragment to
`~/.config/supertree/`, and adds one `source-file` line to `~/.tmux.conf`
(backing it up first).

Overrides:

```sh
ST_VERSION=v0.1.0 …     pin a release instead of latest
ST_PREFIX=~/bin …       install st somewhere else
ST_CONFDIR=~/.tmux …    put the tmux fragment somewhere else
ST_NO_TMUX_CONF=1 …     do not touch ~/.tmux.conf
ST_YES=1 …              answer yes to every prompt (CI, dotfile bootstraps)
ST_HARNESS=codex …       choose claude, codex, or openrouter non-interactively
ST_WINDOWS='agent shell' … choose and order tmux windows
```

### From source

```sh
git clone https://github.com/dawksh/supertree ~/projects/supertree
ln -sf ~/projects/supertree/bin/st ~/.local/bin/st
echo 'source-file ~/projects/supertree/tmux/supertree.conf' >> ~/.tmux.conf
tmux source-file ~/.tmux.conf      # or prefix + r
```

Needs `tmux`, `git`, `fzf`, the programs used by the configured windows, and
`~/.local/bin` on `PATH`. `st doctor` checks the active layout.

### Modules

`bin/st` loads `bin/st-lib/*.sh` in filename order and dispatches commands
registered by those modules. A source-install symlink resolves back to the
repository, so the modules stay beside `bin/st`. Release installs put modules
in `st-lib-<version>` beside the installed command.

The numbered modules make the boundaries explicit:

| Module | Responsibility |
| --- | --- |
| `00-core` through `50-tmux` | Shared helpers for identity, repository state, bootstrap, agent, and tmux |
| `60-picker` | Tree picker and `go`/`resume` |
| `70-worktrees` | Worktree creation, trust, listing, status, and removal |
| `90-maintenance` | `doctor`, `update`, and `version` |

To add a command, put a shell file such as `bin/st-lib/85-greeting.sh` in the
module directory:

```bash
cmd_greet() { printf 'hello %s\n' "${1:-world}"; }
st_register_command greet cmd_greet 'greet someone'
```

The command appears in `st help` automatically. Remove that file to remove
the command. The picker, worktree, and maintenance modules can also be removed
individually; the shared modules provide the functions used across features.
Run the shell tests after changing modules.

### Releases

Pull requests to `main` run the shell test suite on Ubuntu and macOS. A merge
to `main` triggers the release workflow, which tests the merged commit and
publishes `install.sh`, `st.tar.gz`, a legacy upgrade entrypoint named `st`,
`supertree.conf`, and `SHA256SUMS` as a GitHub
Release. `st update` then sees the new release automatically.

Each merged pull request gets a patch version by default. Add the
`release:minor` or `release:major` label before merging to request a larger
version bump. If both are present, major takes precedence. Use squash or merge
commits, so each pull request produces one commit on the first-parent history
of `main`.
The release job catches up on any merges that happen while a prior release is
running. The first automated release includes changes since the last manual
release.

In GitHub repository settings, require pull requests and the `Shell tests
(ubuntu-latest)` and `Shell tests (macos-latest)` checks for `main`. Disable
direct pushes and rebase merging to preserve one release per merged pull
request. Create the optional `release:minor` and `release:major` labels in the
repository. The release workflow uses the repository's `GITHUB_TOKEN`; no
separate release token is needed.

### Update

```sh
st update
```

`st update` checks GitHub for the latest release and installs it immediately when
a newer version is available. The archive and modules are validated and staged
before the current executable is replaced. The previous version's modules remain
available if an update fails. Run `st --version` to see the installed version.

Older single-file installations migrate on the first command after `st update`.
The compatibility entrypoint installs the module bundle and then runs that command.

Source installs made with the symlink instructions above are not overwritten;
update those with `git pull` instead.

---

## Quick start

```sh
cd ~/projects/webauth
st new feat-otp        # worktree + deps + env + session, drops you in the agent
# ... work ...
M-q                    # back to where you ran st; the tree keeps running
st resume feat-otp     # back in the running tree
st down feat-otp       # close the tree session; agent history stays
st rm feat-otp         # done with it: session, worktree and branch go away
```

---

## Commands

| command | what it does |
|---|---|
| `st new <branch>` | Create the worktree, bootstrap it, build the tmux session, switch to it. Reuses the branch if it already exists, otherwise creates it. |
| `st trust` | Trust the current contents of this repository's reviewed `.supertree` file. Run again if the file changes. |
| `st go [query]` | Switch to a tree. No query opens an fzf picker; a query picks the first match. Builds the session first if the tree is closed. |
| `st resume [query]` | Same as `go`. Named for the case where you closed a tree earlier and want it back. |
| `st down [branch]` | Close a tree's tmux session. Worktree, branch and agent history stay. No branch = the tree you are in. |
| `st down --all` | Close every tree session. Lists them and asks first. |
| `st down --subtrees` | Close linked worktree sessions and keep main sessions open. Lists them and asks first. |
| `st ls` | A readable grid of every known tree: session state, agent state, changes, and checkout type. Use a tree name with `st go` to switch to it. |
| `st status [tree]` | Check one agent: `running`, `input`, `done`, or `closed`. Omit the tree name inside its tmux session. |
| `st rm <branch>` | Destructive: kills the session, removes the worktree, deletes the branch if merged. |
| `st remove all [--force]` | List and remove Git checkouts under `ST_WORKTREE_ROOT`, including standalone repositories. Asks before removal. |
| `st update` | Check for the latest release and install it automatically when available. |
| `st uninstall` | Remove the installed command, Supertree config, tmux source line, and state. |
| `st doctor` | Check dependencies, `PATH`, the symlink, the `~/.tmux.conf` line. |

### Flags

```
st new <branch> --from <base>    branch off <base> instead of current HEAD
st new <branch> --bare           skip deps, env and the agent; just print the worktree path
st rm <branch> --force           remove even with uncommitted or unpushed work
st remove all --force            also remove standalone repositories and their history
st down --all -y                 skip the confirmation
```

### Internal

`st window`, `st agent`, `st toggle`, `st go --picker`, `st go --popup`, `st _run`, `st _agent`,
`st _sessions` are called by the tmux bindings, not by hand.

---

## Keys

`M` is Meta — the Option/Alt key.

| key | does |
|---|---|
| `M-w` | tree picker, ordered by most recently opened tree, with a `+ new branch…` row |
| `M-e` | toggle between the first two configured windows |
| `M-Tab` | switch to the tree you were in before; press again to come back |
| `M-r` | pick any repo you have used `st new` in, enter a branch, and create a tree there — no `cd` needed |
| `M-1` / `M-2` / `M-3` | select a configured window by position |
| `M-q` | go back to the session you ran `st` from (leaves tmux if you started outside it) |
| `M-Q` (Option-Shift-Q) | leave tmux; keep the current tree session running |

In the tree picker, press Enter to open a tree, Ctrl-D to delete the selected
worktree, or Escape to close the picker.

Prefix stays `C-a`. `M-arrow` pane movement and `S-Enter` are untouched.
`status-left` shows the current repository and branch, so the tree you are typing into is
always on screen.

Agent state appears in `st ls` and the `M-w` picker. `running` means the agent
process is active, `input` means its pane shows a prompt, `done` means the agent
exited, and `closed` means the tree session is not open. Prompt detection is a
best effort check of the pane; custom agents may need their own prompt pattern.

---

## Layout

```
~/projects/webauth                        main checkout
~/projects/.worktrees/webauth-<repo-id>/feat-otp-<branch-id>  worktree
tmux session "webauth/feat-otp"                                windows: codex, vim, shell
```

The IDs in worktree paths are stable hashes of the main checkout path and the
exact branch name. They keep branches such as `feat/a-b` and `feat-a-b`, and
repositories with the same directory name, separate. Existing worktrees made
with the old path layout are reused when Git confirms the exact repository and
branch. Session names stay readable as `<repo>/<branch>`; a short hash is added
only when two trees would otherwise get the same name. Sessions opened under the
older hashed names are renamed the next time supertree lists trees. Windows are addressed
by **name**, not index, so your `base-index` setting is irrelevant.

Each program window drops to an interactive shell in the same worktree when its
program exits. Window order and selection are configurable.

## Windows

Set `ST_WINDOWS` in `~/.config/supertree/config` to choose which tmux windows
each tree receives and the order in which they appear:

```sh
# Default terminal-editor layout
ST_WINDOWS='agent vim shell'

# VS Code or another external editor
ST_WINDOWS='agent shell'

# No coding-agent window
ST_WINDOWS='vim shell'

# Minimal session
ST_WINDOWS='shell'
```

The supported logical names are `agent`, `vim`, and `shell`. `agent` resolves to
the configured harness name, such as `codex`, so changing `ST_HARNESS` does not
require changing the window list. At least one unique window is required.

Order controls startup focus, `M-1` through `M-3`, and the pair switched by
`M-e`. Changing the setting affects new sessions; run `st down <tree>` and
`st resume <tree>` to rebuild an existing session. Re-run the installer once
after upgrading from v0.3.0 so the positional tmux bindings are installed.

## Agent harnesses

The installer creates `~/.config/supertree/config`. Change `ST_HARNESS`, then
close and resume a tree to rebuild it cleanly with the new agent window:

```sh
ST_HARNESS=codex       # claude | codex | openrouter
```

| value | terminal program | continuation command |
|---|---|---|
| `claude` | Claude Code (`claude`) | `claude --continue` |
| `codex` | Codex CLI (`codex`) | `codex resume --last` |
| `openrouter` | OpenCode (`opencode`) connected to OpenRouter | `opencode --continue` |

For OpenRouter, launch OpenCode once, run `/connect`, and select OpenRouter. API
keys remain in OpenCode's own credential store; supertree does not read or copy
them.

Custom terminal agents work too:

```sh
ST_HARNESS=aider
ST_HARNESS_COMMAND='aider'
ST_HARNESS_RESUME_COMMAND='aider --resume'
```

The harness name becomes the tmux window name and must contain only letters,
digits, `_`, or `-`. The config is sourced as shell code, so only put commands
there that you trust. `ST_HARNESS` in the environment overrides the config for
one invocation.

---

## Creating a tree

`st new` does these things; all but the worktree are skipped with `--bare`:

1. **Pre-create** — for a new branch, runs `ST_PRE_CREATE` in the main checkout
   and picks the start point: `--from`, else `ST_BASE_BRANCH`, else current HEAD.
   If the hook fails, no worktree is created.
2. **Worktree** — `git worktree add` under `~/projects/.worktrees/<repo>/<slug>`.
3. **Deps** — symlinks each `ST_LINK_DIRS` entry from the main checkout, but only
   when the lockfile is byte-identical. If it differs, runs `ST_INSTALL_CMD`
   instead, so a branch that changed dependencies never silently runs main's
   `node_modules` or writes into it.
4. **Env** — copies each `ST_COPY_GLOBS` match from the main checkout. Copies,
   not symlinks, so a tree can diverge.
5. **Hook** — runs `ST_POST_CREATE`.

Build output (`.next`, `dist`) is never shared between trees.

### Per-repo config

Optional `.supertree` at the repo root, sourced by `st new` after you trust its
current contents:

```sh
cat .supertree    # review the commands it contains
st trust          # records this version for the repository
st new feat-otp
```

If `.supertree` changes, `st new` stops before creating a worktree until you
review it and run `st trust` again. `st new --bare` does not execute it.

Example `.supertree`:

```sh
ST_LINK_DIRS=(node_modules)      # symlinked from main when the lockfile matches
ST_COPY_GLOBS=('.env*')          # copied from main
ST_INSTALL_CMD='npm ci'          # used when the lockfile differs
ST_LOCKFILES=(package-lock.json yarn.lock pnpm-lock.yaml bun.lockb)
ST_POST_CREATE='echo "PORT=$((3000 + ST_TREE_INDEX))" >> .env.local'
ST_BASE_BRANCH=dev               # new branches start from dev
ST_PRE_CREATE='git fetch origin dev:dev'   # fast-forward local dev first
```

`ST_PRE_CREATE` runs in the main checkout, only when the branch does not exist
yet, and gets `ST_TREE_BRANCH` and `ST_BASE_BRANCH`. `git fetch origin dev:dev`
updates `dev` without checking it out and refuses if `dev` has diverged or is
checked out somewhere; use `git -C <that checkout> pull --ff-only` in that case.

The hook gets `ST_TREE_DIR`, `ST_TREE_BRANCH` and `ST_TREE_INDEX`.
`ST_TREE_INDEX` is a stable small integer per tree — derive a dev server port
from it so two trees can run at once.

The file is sourced as a shell script, so it runs as you. Only trust its contents
after reviewing them. Global user config remains sourced on every invocation.

---

## Closing, resuming, exiting

**Leave the agent, keep the tree open** — use the agent's exit command or
`Ctrl-D`. The window falls back to a shell in the worktree. `M-1` recreates the
configured agent window if it has been closed.

**Leave tmux** — `M-q` switches back to the tmux session you ran `st` from, or
detaches if you started outside tmux. `M-Q` always detaches your client and
returns to your terminal shell. The tree session, agent, and editor keep running. Reattach with
`st resume [branch]` or `tmux attach`.

**Close the tree** — `st down`. Kills only the tmux session. Unsaved editor
buffers in that session are lost.

For `st down` and `st rm`, `st` moves attached clients off a session before
closing it — back to the session they came from when possible (remembered per
client tty under `~/.local/state/supertree/origin/`), otherwise to a remaining
session. `st down --all` can detach clients when it closes the last tmux
session. Your `detach-on-destroy` setting is left alone.

**Close subtrees** — `st down --subtrees` closes every linked worktree
session and leaves each repository's main session open. If a main session is
closed, `st` opens a shell there before closing its linked trees so tmux can
keep the client attached. `st down --all` closes the main sessions too.

**Come back** — `st resume [branch]` or `M-w`. The session is rebuilt and the
configured agent resumes that worktree's own conversation. The first launch for
each agent in a tree starts fresh and leaves a marker; later launches use that
agent's continuation command. Agent transcripts live in their native stores, so
this survives a hard `kill-session`, not just a clean exit.

**Delete the tree** — `st rm <branch>`. Refuses while there are uncommitted
changes or unpushed commits unless you pass `--force`, prints what it will
remove, and asks. The branch is deleted only if it is merged. In the `M-w`
picker, highlight a tree and press Ctrl-D to run the same guarded removal.
The main worktree cannot be deleted from the picker.

**Clear the worktree root** — `st remove all` inventories Git checkouts at the
root and one directory below it. It removes linked worktrees through Git and
leaves their branches when they are not merged. Standalone repositories need
`--force` because their Git history lives in the directory being deleted.
Uncommitted or unpushed work in linked trees also needs `--force`. The command
prints every target and asks once; unrelated files under the root are left alone.

**Uninstall** — `st uninstall` asks before removing the installed `st` command,
its installed modules, Supertree config and state, and the matching source line from `~/.tmux.conf`.
It closes known Supertree sessions. Worktrees and coding-agent installs remain;
run `st remove all` first if you want the worktrees removed. The installer
records custom config locations for uninstall. A source install removes only
the `st` symlink, not the checkout.

---

## State

| path | holds |
|---|---|
| `~/projects/.worktrees/<repo-id>/<branch-id>` | new worktrees (old paths remain usable) |
| `~/.local/state/supertree/repos` | repos `st` knows about (appended by `st new`) |
| `~/.local/state/supertree/idx/<repo-id>/<branch-id>` | that tree's `ST_TREE_INDEX` |
| `~/.local/state/supertree/seen/<repo-id>/<branch-id>.<harness>` | marker meaning that agent has run here, drives continuation |
| `~/.local/state/supertree/trust/<repo-id>` | hash of the reviewed `.supertree` contents |
| `~/.local/state/supertree/origin/<tty>` | which session a client came from, so closing a tree returns it there |

A repo only shows up in `st go` / `st ls` after its first `st new`. Override the
worktree root with `ST_WORKTREE_ROOT`, the state dir with `ST_STATE`.

---

## Troubleshooting

**`M-w` does nothing** — your terminal is eating Option. In WezTerm, Option must
send Meta rather than composing characters. Your `M-arrow` pane binds are the
quick test: if those work, these do.

**`st go` says "no current client"** — run from a shell that is not attached to a
tmux client. Harmless from scripts; from a real pane it switches normally.

**A tree is missing from the picker** — its repo was never registered. Run
`st new` once in that repo, or add the main checkout path to
`~/.local/state/supertree/repos`.

**`st new` ran a full install instead of linking** — the main checkout's lockfile
differs from the new tree's, usually because you have uncommitted lockfile
changes on main. Intended.

**The agent started fresh instead of continuing** — check `st doctor`, the
configured harness, and that tree's `seen` marker. Each agent keeps a separate
marker and its own native history.

**OpenRouter is selected but no models appear** — run `opencode`, enter
`/connect`, choose OpenRouter, then use `/models` to select a model.
