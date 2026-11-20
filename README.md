# supertree

Work on several branches of the same repo at once, each in its own git worktree
and configurable tmux session, with your chosen coding agent and terminal tools
already running.

Run `st` on its own to reopen your most recent tree. If that tree is unavailable,
Supertree opens the tree in your current checkout or focuses the tree list at home.
Open a tree to get your agent, editor
and shell as tabs. One key switches between trees,
one key flips between your primary windows, and two keys leave tmux while keeping
the tree running. Closing a tree with `st down` lets you reopen the agent in the
same conversation.

```
 Supertree                 1. codex ●   2. nvim   3. shell      webauth/feat-otp
 n new · d remove
 webauth                │
   · main               │  claude
 › ● feat-otp           │
   ◆ crypto-mon         │
 fe                     │
   ○ old-branch         │
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
In an interactive terminal, each stage fills the screen and downloads show a
branching ASCII animation. Redirected output remains a plain log.

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
ST_WITH_NEXT=1 …        include the experimental Go core (default: shell-only)
```

To include the workspace core without installing Go:

```sh
curl -fsSL https://github.com/dawksh/supertree/releases/latest/download/install.sh | ST_WITH_NEXT=1 bash
```

Prebuilt core binaries support macOS and Linux on Intel/AMD 64-bit and ARM64.
Updates and repairs preserve your choice. `ST_WITH_NEXT=1 st repair` adds or restores
the core for the installed release; `ST_WITH_NEXT=0 st repair` stops and removes it
while keeping its workspace database. Shell-only installation works on other
platforms and with releases that predate the Go assets.

### From source

Try this checkout through the usual worktree, agent, Neovim, and shell workflow.
The Go controls are an optional addition to that workspace.

```sh
./dev open                    # build and open this checkout's local workspace
./dev                         # reopen the last local worktree
./dev new <branch>             # create a worktree through the usual flow
./dev test                    # automated checks, in a disposable test environment
```

For a first manual check:

1. Run `./dev open` from this checkout. Use Option-1 for the agent, Option-2
   for Neovim, and Option-3 for the shell. Option-S focuses the tree sidebar.
2. Edit in Neovim and run a command in the shell. The existing Option-v split
   command lets you bring terminals beside each other.
3. From the local shell, run `st new local-harness-test` to try a new worktree.
   Use Option-w to switch trees. Each keeps its own programs.
4. From the shell, run `st control` to try the new workspace controls in a popup.
   Press `q` to return to your terminal. You can keep using the regular agent,
   editor, shell, tree picker, and splits without opening these controls.
5. If you explicitly enable experimental paired mode, run `st share --diff --message 'Review this change'`
   from the shell. In Neovim, select saved lines and run `:SupertreeAsk` to ask
   about them. Both use the agent already in this tree's Codex tab.

Inside the sandbox, `st` points to this checkout. Outside it, use `./dev` so
commands target the local environment. `./dev test` runs automated tests; it does
not open your manual workspace.

The worktree is the workspace: its agent, Neovim, shell, and extra test/server
terminals share one tab strip. `st control` shows that workspace's processes,
shared context, and questions in an optional popup; it is not a separate desktop.
The base tab strip, pane borders, sidebar, and paired conversation use the same
palette as the controls. Old extra-terminal sessions stay open and are labelled
as extra terminals; opening the checkout returns to its normal worktree tabs.

The agent tab runs the actual selected harness: Codex, Claude Code, OpenCode, or
your configured command. Chat and answer its approvals there, edit in Option-2,
and run tests in Option-3. Supertree provides the surrounding workspace without
replacing the harness's own interface.

The tab says `terminal` for that normal harness and `paired` for the experimental
native conversation console. Fresh dev config uses `ST_AGENT_MODE=terminal`.
Native context delivery (`st share` and `:SupertreeAsk`) currently requires
explicitly opting into `ST_AGENT_MODE=paired`; it does not inject messages into
the regular harness. Changing config does not replace any running programs.

Additional development commands:

```sh
./dev control                 # optional controls; popup when inside local tmux
./dev next inspect --json      # rebuild and inspect local workspaces
./dev next commands --json     # rebuild and discover commands
./dev next daemon start        # start the isolated persistent workspace core
./dev next events --json       # watch workspace changes (Ctrl-C to disconnect)
./dev package                 # build release assets in .dev/releases; no publishing
./dev stop                    # close local sessions; keep worktrees and state
```

This needs Go 1.24+, Git, tmux, and fzf for the sidebar. It creates an ignored
`.dev/` directory with separate config, state, worktrees, and command wrappers,
plus a separate tmux socket per checkout. It does not install anything or change
your dotfiles. Commands keep their caller's working directory, so you can also
run `/path/to/supertree/dev` from another repository.

Fresh sandboxes use `ST_WINDOWS='agent vim shell'`. Edit `.dev/config` to choose
your agent or customize the existing layout. Older sandboxes retain their config;
if yours says `ST_WINDOWS=shell`, change it to `ST_WINDOWS='agent vim shell'` and
reopen the tree. Missing tabs are added without restarting existing programs.
Local terminals use a clean Bash shell, with `st` pointing to this checkout.
Keyboard shortcuts and background jobs use the same local wrappers.
Shortcut bindings use absolute checkout paths so a changed server PATH cannot
send them to an installed release. Option-1 returns to the shell from the home
sidebar; Option-S focuses the sidebar.

State and sessions persist between launches; `./dev stop` closes the local core
and tmux server without removing worktrees. `./dev tmux list-sessions` inspects that server.
Each launch rebuilds the Go client; commands inside the sandbox use the current
build without rebuilding on every keypress. `./dev build` rebuilds it explicitly.
An already running daemon keeps its loaded code: run `./dev next daemon stop`
then `./dev next daemon start` after changing core code. Stopping only the daemon
leaves your local terminals running.

For a persistent installation from source:

```sh
git clone https://github.com/dawksh/supertree ~/projects/supertree
ln -sf ~/projects/supertree/bin/st ~/.local/bin/st
echo 'source-file ~/projects/supertree/tmux/supertree.conf' >> ~/.tmux.conf
tmux source-file ~/.tmux.conf      # or prefix + r
```

Needs `tmux`, `git`, `fzf`, the programs used by the configured windows, and
`~/.local/bin` on `PATH`. `st doctor` checks the active layout, generated split
binding, and live tmux binding. If files or bindings are damaged, run `st repair`.
It downloads and verifies the installer for the **installed release**, reinstalls
its command and modules, regenerates bindings from your config, and reloads tmux.
A config with invalid shell syntax is saved as `config.broken.*` before defaults
are restored. Repair reports live keys overridden by other tmux bindings and
duplicate `ST_KEY_*` assignments, naming each affected key and the active tmux
action. Move the Supertree `source-file` line after conflicting bindings or
choose a different `ST_KEY_*` value, then run repair again. Run `st update`
separately to move to a newer release. Source installs should restore their
checkout with Git and reload their tmux config.

### Modules

`bin/st` loads `bin/st-lib/*.sh` in filename order and dispatches commands
registered by those modules. A source-install symlink resolves back to the
repository, so the modules stay beside `bin/st`. Release installs put modules
in `st-lib-<version>` beside the installed command.

The numbered modules make the boundaries explicit:

| Module | Responsibility |
| --- | --- |
| `00-core` through `50-tmux` | Shared helpers for output styling (`05-ui`), identity, repository state, bootstrap, agent, and tmux |
| `55-sidebar` | Tree sidebar panes and their refresher |
| `60-picker` | Tree picker and `go`/`resume` |
| `70-worktrees` | Worktree creation, trust, listing, status, and removal |
| `90-maintenance` | `doctor`, `update`, and `version` |

To add a command, put a shell file such as `bin/st-lib/85-greeting.sh` in the
module directory:

```bash
cmd_greet() { printf 'hello %s\n' "${1:-world}"; }
st_register_command greet cmd_greet 'greet someone'
```

The command appears under "more" in `st help` automatically. Remove that file to remove
the command. The picker, worktree, and maintenance modules can also be removed
individually; the shared modules provide the functions used across features.
Run the shell tests after changing modules.

### Experimental workspace core

The worktree workspace remains the main interface. `st control` opens the Go
controls as a popup from its shell; closing them returns to that workspace.
`st next tui` also runs the controls directly for development or standalone use.

The Go core provides workspace inspection, a discoverable command registry, and
an opt-in local daemon with durable identity and change subscriptions. Build it
from this checkout with Go 1.24 or newer:

```sh
go build -o bin/st-next ./cmd/st-next
bin/st next inspect
bin/st next inspect --json
bin/st next commands --json
```

For a source install, the existing `st` symlink finds the built binary beside
`bin/st`. Release installs opt in with `ST_WITH_NEXT=1` and place the verified
prebuilt binary inside `st-lib-<version>`, alongside its matching shell modules.
Normal Supertree commands do not require Go or this binary. `st next --version`
reports the core binary's release version and source commit.

Inspection reads registered repositories from `$ST_STATE/repos` and includes the
current Git checkout without registering it. It lists main, linked, detached, and
missing worktrees, including linked worktrees outside `ST_WORKTREE_ROOT`. The
`managed` field indicates a main checkout or linked tree under that root; it does
not adopt a worktree or authorize changes to it. Bare repositories are reported
with `kind: "bare"`.

JSON results contain `schema_version: 1`, `workspaces`, `sessions`, `tmux_status`,
and `warnings`. Missing repositories and individual scan failures produce warnings
so a partial result remains inspectable. `tmux_status` distinguishes `running`,
`stopped`, `unavailable`, and `error`. Session associations use the session's initial
working directory, choosing the deepest matching worktree. They do not establish
agent identity or infer agent activity.

Without a daemon, inspection remains read-only and IDs are hashes of canonical
checkout paths. They survive branch switches and symlink aliases. With a daemon,
SQLite preserves the initially imported IDs and tracks Git administrative-directory
identity across restarts and moves on the same filesystem, including `git worktree
move`. Copies or recreated Git administrative directories receive separate
identities. Cross-filesystem moves have no automatic identity transfer yet.
Offline inspection after a move uses its new path hash; use the daemon for durable IDs.

The CLI aliases and generic invocation use the same command registry:

```sh
bin/st next invoke command.list '{}'
bin/st next invoke workspace.inspect '{}'
bin/st next inspect --workspace <workspace-id> --json
bin/st next invoke workspace.inspect '{"workspace_id":"<workspace-id>"}'
```

`commands --json` exposes command versions, argument and result schemas, effects,
and recording, repetition, sensitivity, and undo metadata for future macro support.
`invoke` always returns JSON. Failed JSON requests exit nonzero with a structured
error on stderr and no result on stdout.

`st next` forwards the shell's resolved `ST_STATE` and `ST_WORKTREE_ROOT` settings.
Running `bin/st-next` directly reads these environment variables and uses their
normal home-directory defaults; it does not source shell config. Set
`ST_NEXT_TMUX_SOCKET` to inspect an explicit tmux socket, otherwise tmux uses its
normal environment. Direct discovery does not create state directories, execute
project hooks, or modify Git or tmux state. The daemon writes only its own metadata.

#### Persistent local core

```sh
./dev next daemon start
./dev next daemon status --json
./dev next inspect --json
./dev next commands --json
./dev next events --json
# In another terminal: change a branch or create a worktree using normal tools.
./dev next daemon stop          # leaves tmux and terminal programs running
```

`daemon start` detaches and waits until ready; starting it again returns the same
daemon. `daemon serve` runs in the foreground for debugging. The daemon stores
identities in `$ST_STATE/next/state.db` and logs in `$ST_STATE/next/daemon.log`.
It remembers repositories discovered by clients without rewriting `$ST_STATE/repos`.
Unavailable repositories and missing workspaces remain visible. A process-held
state lock prevents competing daemons, and a new daemon can reclaim a crashed
daemon's stale socket. Database schema versions newer than this binary are rejected.

CLI commands automatically use the daemon when its socket exists. Otherwise,
`inspect`, `commands`, and the original read commands use direct discovery.
`st next --offline inspect --json` explicitly bypasses a daemon. An existing but
unreachable socket produces an error; it does not silently switch identity models.
`ST_NEXT_SOCKET` selects an absolute socket path and requires an online daemon.
The dev launcher clears this override to keep the sandbox separate.

The default socket is `/tmp/supertree-next-<uid>/<state-path-hash>.sock`, inside an
owner-only directory. `daemon status --json` reports the exact path and capabilities.
Clients sharing `ST_STATE` share one daemon; conflicting worktree-root or explicit
tmux-socket settings are rejected. CLI requests include their current directory,
so a second client can discover another checkout in the same core.

#### Socket clients and change events

The socket accepts newline-delimited JSON-RPC 2.0 requests, including batches of
ordinary requests. Method names are the same commands exposed by `commands --json`:

```json
{"jsonrpc":"2.0","id":1,"method":"workspace.inspect","params":{},"meta":{"directory":"/absolute/checkout","actor":"agent:review","request_id":"review-42"}}
```

Responses retain the request ID. JSON-RPC errors include the structured command
error in `error.data`. Actor labels provide attribution, not authentication.
The CLI defaults to actor `human`; set `ST_NEXT_ACTOR` and `ST_NEXT_REQUEST_ID`
for scripts and agent clients. Frames are limited to 8 MiB, writes time out after
five seconds, and ordinary idle connections close after one minute.

`event.subscribe` uses a dedicated connection and requires a request ID:

```json
{"jsonrpc":"2.0","id":2,"method":"event.subscribe","params":{}}
```

Its acknowledgement contains `epoch`, `sequence`, `resnapshot_required`, and a
workspace snapshot. Subsequent notifications use method `event` with sequenced
`workspace.changed` events in `params`. Events invalidate workspace state; inspect
again to obtain current content. The daemon polls Git and tmux once a second,
also refreshing before inspection and subscriptions. These are observed changes,
not an audit of every intermediate external action.

`events --json` prints the acknowledgement followed by event params as JSON lines.
Canceling it closes only that subscription. Reconnect explicitly with a saved cursor:

```sh
./dev next events --epoch <previous-epoch> --after <last-sequence> --json
./dev next events --workspace <workspace-id> --json
```

The daemon retains the latest 256 events in memory. With a valid cursor, consume
the replay after your saved sequence before advancing to live events; the
acknowledgement's sequence is the current high-water mark. If `resnapshot_required`
is true, replace your state with the supplied snapshot and use its sequence.
A restart, expired cursor, or replay larger than the 32-event subscriber queue
requires a fresh snapshot. A live subscriber that falls behind receives a
`resnapshot_required` notification and disconnects; reconnect without a cursor.
Event history is not persisted across daemon restarts. CLI event streams report
disconnects and require an explicit reconnect.

Run `./dev test` for Go vet, race checks, and the shell suite. The runner uses a
temporary HOME and remaps tmux's default, named, and explicit sockets into its
own disposable directory. Test cleanup addresses only those private sockets.
For a focused check, use `bash scripts/test-local.sh -- go test -race ./internal/process`;
`bash scripts/test-local.sh --shell` runs only the shell suite. CI and release
checks use the same runner. Test commands should never run against your normal
tmux environment.

### Named terminal processes

Start the local core, then launch named programs in the current checkout:

```sh
./dev next daemon start
./dev next process start --name shell --kind shell -- /bin/bash --noprofile --norc
./dev next process start --name tests --kind tests -- go test ./...
./dev next process list
./dev next process attach <process-id>
./dev next process output <process-id> --lines 200
./dev next process stop <process-id>
./dev next process restart <process-id>
```

`start` uses an executable plus argument array; shell syntax runs only when you
explicitly launch a shell with `-c`. Use `--workspace <id>` to target another
checkout, `--cwd <relative-directory>` for a subdirectory, and repeated
`--env KEY=VALUE` flags for overrides. Environment values are stored in the private
launch definition; inspection exposes their names. Avoid putting secrets in argv.
Names are unique within a workspace; restart an existing definition explicitly.

Each process has a durable ID and a tagged tmux pane. Starts and moves happen in
the background. `attach` changes focus only as an explicit terminal action;
`attach --json` or `invoke process.attach` returns coordinates without focusing.
Exiting the core or UI leaves programs running. Restarting the daemon reconciles
existing tagged panes and never automatically relaunches missing programs.
Exited owned terminals retain their output and exit status until stopped/restarted.

```sh
./dev next process move --process <id> --target <other-id> --direction vertical
./dev next process move --process <id>   # detach into its own window
./dev next process register --name editor --workspace <id> --pane %3
```

Normal agent/editor/shell tabs appear automatically as `source: "observed"`
when the daemon discovers a Supertree session. Observing them changes no pane
options and enables attachment/output only. Stop and move require explicit
registration; restart requires a known launch definition. IDs track the tmux
server and pane process, so a reused pane number cannot target an older terminal.
The controls label these existing terminals as read-only.

Registration explicitly enables management of an existing terminal. Its original
launch definition is unknown, so it cannot be restarted by the core.
Stopping a registered pane still closes that terminal. Process IDs survive moves;
reused pane IDs after a tmux restart cannot inherit another process's identity.
The core listens to tmux control-mode changes and also reconciles once a second.

All actions are discoverable through `commands --json` and available via
`invoke process.start`, `process.list`, `process.register`, `process.stop`,
`process.restart`, `process.move`, `process.attach`, and `process.output`.
Mutation arguments accept revision checks where advertised. Repeating a start
with the same actor/request ID returns the previous launch; changing its arguments
with that request ID is rejected. A different request ID never implicitly restarts
an existing named process.

### Go workspace TUI

```sh
./dev next tui                  # starts the local core if needed
./dev next tui --workspace <id>
```

The control client opens in your current terminal. Tab switches between workspaces
and processes; arrows or `j`/`k` select. `s` starts a shell, `n` opens a named-process
form, Enter attaches, `x` stops after confirmation, `r` restarts, and `m` joins a
terminal beside another or detaches it into a separate window. `p` opens the
command palette; its entries and JSON argument models come from the core registry.
Use `?` for help and `q` to close the TUI. Closing it leaves the core and terminals
running. Detach from an attached tmux terminal to return to the control client.

The TUI subscribes to changes, reconnects with a fresh snapshot when required,
and never repeats a mutation on reconnect. Background process actions preserve
focus. If the terminal is too small, enlarge it to at least 60 columns × 16 rows.
After rebuilding core code, restart an already running daemon before opening the
new client so both use the latest registered commands.

Agent clients can inspect `view.list` (also included in workspace snapshots) to
see each TUI's workspace/process selection, panel, pending command/input, and
available command names. `view.select` accepts `view_id`, `workspace_id`, and
`process_id`, plus an optional `expected_revision`. It changes the visible
selection without selecting a tmux pane. Selection changes are rejected while
human input is active. Agents operate the same `process.*` commands directly;
no simulated keystrokes or screen-coordinate parsing is needed. Connected views
expire after 30 seconds without a heartbeat; closing a view stops no processes.

### Pairing through shared goals and context

The Go baseline now includes named processes, the control TUI, and one native
agent integration. Git worktree creation/removal still uses the existing shell
commands. Macros, registers, bookmarks, and the Lua extension runtime come later.

From a local worktree's shell, run `st control`. It opens controls for that checkout;
`q` returns to your editor and terminals. Direct `./dev next tui` remains available.

Press `c` for the pairing panel. `t` creates a shared goal; `a` selects this
worktree's paired agent. An ordinary CLI tab shows migration instructions.
`f` captures saved file lines, `d` captures the tracked Git diff, `o` captures the
selected process's output, and `N` adds a note. Use arrows to select a context item
and Space to mark it. `i` opens a message with the marked context IDs, which you
can inspect or edit before sending. Capturing context alone sends nothing.

Questions appear in the same panel. Select one and press Enter to answer;
provider approvals require an explicit `accept` or `decline`. `I` interrupts the
selected native agent after confirmation, checking that its turn has not changed.
Keep editing in your editor or shell
while the agent runs. `r` recaptures a selected context source; `u` marks the shared
goal done after confirmation. Agent turn completion does not mark a goal successful.

The same workflow is available through structured commands:

```sh
./dev next daemon start
./dev pair status             # native agent in the usual Codex tab
./dev pair send --message 'Investigate this failure'
./dev pair interrupt
./dev next task add --title 'Investigate the failing test' --notes 'Keep the interface stable'
./dev next context add --file internal/example.go --start-line 10 --end-line 30
./dev next context add --kind diff
./dev next agent start --name pair --task <task-id>
./dev next agent list
./dev next agent send <agent-id> --message 'Investigate this failure' --context <context-id>
./dev next question list
./dev next question answer <question-id> --revision 1 --answer 'decline'
./dev next agent interrupt <agent-id>
./dev next invoke delivery.list '{}'
./dev next invoke delivery.inspect '{"delivery_id":"<delivery-id>"}'
```

#### Pairing in the usual agent tab

Dev sandboxes default to `ST_AGENT_MODE=terminal`, showing the actual harness UI.
The native pairing console is experimental and opt-in: set `ST_AGENT_MODE=paired`
in `.dev/config` to try it with Codex. Opening a new worktree then starts native
pairing in its normal Codex tab beside the unchanged Neovim and shell tabs.
Type messages there as usual. `/interrupt` stops a turn; `/quit` returns to its
shell. `st pair status`, `st pair send --message '...'`, and `st pair interrupt`
target this worktree's agent without copying IDs or opening another agent.
The control popup answers approvals and questions while the other tabs stay usable.

Already running CLI sessions stay running. To migrate one, exit it normally and,
from the shell in that same agent tab, run:

```sh
st pair run --thread <exact-Codex-thread-id>
```

Omit `--thread` for a new conversation. Once paired, reopening the tree resumes
its checkpointed exact conversation and keeps its stable agent identity. The
core never scrapes a terminal or uses an implicit last conversation for migration.
`ST_AGENT_MODE=terminal` retains ordinary CLI launch; other harnesses keep their
existing behavior. Existing installations default to terminal mode until configured.
The native primary agent appears as `primary: true` and its terminal as
`source: "hosted"`. It is relaunched through the usual worktree/agent-tab flow;
generic restart is reserved for processes with a recorded launch definition.

#### Share from the editor or shell

Paired-mode Neovim tabs load the bridge automatically. Your mappings and editor
configuration stay yours. Select saved lines and use `:SupertreeAsk <question>`;
without a question it prompts for one. `:SupertreePreview` captures a selection
into a temporary preview without delivery. `:SupertreeContext` captures only, and
`:SupertreeShareDiff <question>` shares the tracked working-tree diff. Modified
buffers must be saved explicitly before sharing; cancelling a question sends nothing.

From the usual shell tab:

```sh
st share --file internal/example.go --start-line 10 --end-line 30 --message 'Why does this fail?'
st share --diff --message 'Review this change'
st share --output --message 'Explain this test failure'
st share --file internal/example.go --preview
st share --context <reviewed-context-id> --message 'Investigate these lines'
```

Output sharing captures the current terminal's last 200 lines; `--process <id>`
selects another terminal explicitly. Preview creates a context ID and sends no
turn. Reusing that ID sends the reviewed snapshot and rejects it if the saved
source has changed. The latest open shared goal accompanies delivery. No action
automatically starts another agent. `--json` returns the receipt; structured
clients invoke the same `context.share` command with a workspace ID and source.
Actor/request IDs deduplicate delivery even when capture and sending are combined.
The editor bridge ships with the modules for source and release installations.

`task`, `context`, `agent`, and `question` accept `help`; `commands --json` describes
all argument/result schemas. Human and agent clients invoke the same commands.
Named terminals inherit the core's workspace/socket settings, so an agent calling
the CLI reaches the same workspace interface. Task and question updates use
revision checks to preserve concurrent human decisions.

File captures record a hash of the saved source, path, and line range. Diff
captures disable external diff/textconv commands and hash the tracked diff.
Changed sources are marked stale and rejected during export/delivery unless a
caller explicitly allows stale context. `context refresh <id>` retains the
identity and records a new revision. Notes and output are captured snapshots,
not continuously updated inputs. Context text is bounded to 32 KiB per item;
truncation is visible, and deliveries include source revisions and capture times.
Files must be regular text files up to 1 MiB within the selected checkout.

The first native adapter uses the installed `codex app-server --stdio` protocol
and your Codex login/configuration. It starts a worker as a named tmux process,
so daemon or TUI disconnects leave its provider and conversation running. Native
thread and turn IDs are exposed explicitly. Restarting the named worker resumes
its saved exact thread; it never uses a provider's implicit “last conversation”.
Native mode uses workspace-write sandboxing and on-request approvals. Command,
file-change approvals, and provider user-input questions are supported; unknown
provider requests are rejected visibly. Provider approvals require a `human` or
`human:<name>` actor; agent-labeled clients cannot approve them. Actor labels
remain cooperative attribution, not an authentication boundary. A corrupt
checkpoint prevents startup; a checkpoint write failure disables new sends until
storage is restored and the worker is restarted. Generic agent CLIs remain available as
ordinary named terminal processes, with no invented native send/resume capabilities.

Each delivery has a durable receipt containing the exact text/context sent.
`sent` means the provider accepted a turn, not that its task succeeded. If an
acknowledgement is lost, the receipt remains `pending`/`unknown`; inspect it to
reconcile against the worker's receipt. Reusing an actor/request ID returns the
previous delivery rather than sending twice. The TUI never automatically retries
mutations. Recent receipt lists omit large text; inspect an ID for the full payload.
Native conversation previews retain the latest 32 bounded messages; complete
provider history remains with Codex. This is cooperative file access: arbitrary
editors and agents can both write files, and the harness does not lock them out.

For Neovim, explicitly load the optional selection bridge:

```lua
dofile('/path/to/supertree/editor/supertree.lua').setup({
  command = { '/path/to/supertree/dev', 'next' },
})
```

Select saved lines and run `:'<,'>SupertreeContext`. It uses the same CLI capture
command, reports the context ID, and leaves delivery to you. Unsaved buffers must
be saved first; the bridge never silently writes them. Other editors can invoke
the CLI with a file path and line range directly. No symlinks or plugin install
are required to test this bridge locally.

### Releases

Pull requests to `main` run Go and shell tests on Ubuntu and macOS, cross-build
the release matrix, and verify the native binary and asset checksums. A merge
to `main` triggers the release workflow, which checks each released commit with
the shell suite, Go vet, and Go race tests before building its assets. It
publishes `install.sh`, `st.tar.gz`, a legacy upgrade entrypoint named `st`,
`supertree.conf`, four standalone core binaries, and `SHA256SUMS` as a GitHub
Release. The core asset names are `st-next-darwin-amd64`, `st-next-darwin-arm64`,
`st-next-linux-amd64`, and `st-next-linux-arm64`. All binaries are built with
CGO disabled and include the release version and commit. The shell archive
contains no platform binaries and remains compatible with older upgraders.
Historical commits predating core packaging produce shell assets only.
`st update` sees the new release automatically.

Build the same assets locally without tagging or uploading:

```sh
./dev package                         # working tree; version dev; .dev/releases
bash scripts/package-release.sh /tmp/st-release v0.0.0-test
(cd /tmp/st-release && shasum -a 256 -c SHA256SUMS)
```

These commands require Go and overwrite generated assets in the selected output
directory. Release publication builds from the exact archived Git commit.

Each merged pull request gets a patch version by default. Add the
`release:minor` or `release:major` label before merging to request a larger
version bump. If both are present, major takes precedence. Use squash or merge
commits, so each pull request produces one commit on the first-parent history
of `main`.

Add `release:skip` before merging to defer that commit's release; its changes
ship with the next non-skipped release. Consecutive skipped merges accumulate,
and a skipped latest commit remains unreleased on retries or manual workflow
runs. Any `release:minor` or `release:major` on a skipped merge carries forward
(with major taking precedence), but `release:skip` applies only to that merge.
Keep these labels on merged pull requests until their changes are released,
since the catch-up job reads them again on each run.

The release job catches up on any merges that happen while a prior release is
running. The first automated release includes changes since the last manual
release.

In GitHub repository settings, require pull requests and the `Shell tests
(ubuntu-latest)`, `Shell tests (macos-latest)`, `Go tests (ubuntu-latest)`, and
`Go tests (macos-latest)` checks for `main`. Disable
direct pushes and rebase merging to preserve one release per merged pull
request. Create the optional `release:minor`, `release:major`, and
`release:skip` labels in the repository. The release workflow uses the
repository's `GITHUB_TOKEN`; no separate release token is needed.

### Update

```sh
st update
```

`st update` checks GitHub for the latest release and installs it immediately when
a newer version is available. It verifies the release installer, then uses it to
refresh the command, modules, and tmux bindings together while keeping your
chosen harness, window layout, and optional core choice. Core downloads are
verified before replacement and stay paired with their shell release. A running
daemon keeps its loaded code; after upgrading the core, run `st next daemon stop`
then `st next daemon start` to load the new version without closing terminal programs.
Release assets are checked before the current
executable is replaced. The update reports its network steps immediately and
times out if a release request stalls. Run `st --version` to see the installed version.

Older single-file installations migrate on the first command after `st update`.
The compatibility entrypoint installs the module bundle and then runs that command.

Source installs made with the symlink instructions above are not overwritten;
update those with `git pull` instead.

---

## Quick start

```sh
cd ~/projects/webauth
st                     # reopen the last tree, or open the current tree/home list
st new feat-otp        # worktree + deps + env + session, drops you in the agent
# ... work ...
M-q                    # detach from tmux; the tree keeps running
st                     # from anywhere: resume the last tree
st home                # open the home shell and tree list explicitly
M-w                    # jump to any other tree
st down feat-otp       # close the tree session; agent history stays
st rm feat-otp         # done with it: session, worktree and branch go away
```

---

## Commands

| command | what it does |
|---|---|
| `st` | Reopen the most recent tree; otherwise open the current tree or focus the home tree list. |
| `st home` | Open the persistent Supertree home shell and sidebar. Registers the current repository if needed. |
| `st open` | Go directly to the current tree, or the most recently used tree outside a known checkout. |
| `st new <branch>` | Create the worktree, bootstrap it, build the tmux session, switch to it. Reuses the branch if it already exists, otherwise creates it. |
| `st trust [--show]` | Trust the current contents of this repository's reviewed `.supertree` file. Run again if the file changes. `--show` prints what changed since you last trusted it. |
| `st go [query]` | Switch to a tree. No query opens an fzf picker; a query picks the first match. Builds the session first if the tree is closed. |
| `st resume [query]` | Same as `go`. Named for the case where you closed a tree earlier and want it back. |
| `st roots` | Create a tree in any known repository: pick the repo, then a branch (what `M-r` runs). |
| `st last` | Switch to the tree you were in before (what `M-Tab` runs). |
| `st split [--horizontal\|--vertical]` | Split side by side or stacked (`M-v` / `M-V`). With other trees open, a picker asks for another copy of this tab, or one tab of another open tree. |
| `st down [branch]` | Close a tree's tmux session. Worktree, branch and agent history stay. No branch = the tree you are in. |
| `st down --all` | Close every tree session. Lists them and asks first. |
| `st down --subtrees` | Close linked worktree sessions and keep main sessions open. Lists them and asks first. |
| `st ls` | Every known tree grouped by repo: agent state, uncommitted (`±`) and unpushed (`↑`) counts, `⌂` for the main checkout. Trees waiting on you come first. Piped output is one tab-separated line per tree: `tree, session, agent, changes, type`. Use a tree name with `st go` to switch to it. |
| `st status [tree]` | Check one agent: `running`, `input`, `done`, or `closed`. Omit the tree name inside its tmux session. |
| `st rm <branch>` | Destructive: kills the session, removes the worktree, deletes the branch if merged. |
| `st remove all [--force]` | List and remove Git checkouts under `ST_WORKTREE_ROOT`, including standalone repositories. Asks before removal. |
| `st update` | Check for the latest release and install it automatically when available. |
| `st uninstall` | Remove the installed command, Supertree config, tmux source line, and state. |
| `st doctor` | Check dependencies, fzf (0.45+) and tmux (3.3+) versions, config syntax, generated bindings, and live tmux keys. Each problem comes with a `fix` line; in a terminal it ends with a problem count. |
| `st config` | Change the agent, the windows and their order, the waiting count on the status line, desktop notices, and the sidebar from an fzf screen. Writes the config file; open trees pick up agent and window changes when reopened. Piped, it prints the current settings. |
| `st repair` | Reinstall this release and reload its tmux bindings. |
| `st keys` | Print every tmux key binding (`M-/` shows the same list in a popup). |
| `st help` | List commands grouped by task (trees, setup), then keys. `st --version` prints the installed version. |

### Flags

```
st new <branch> --from <base>    branch off <base> instead of current HEAD
st new <branch> --bare           skip deps, env and the agent; just print the worktree path
st rm <branch> --force           remove even with uncommitted or unpushed work
st remove all --force            also remove standalone repositories and their history
st down --all -y                 skip the confirmation
```

### Internal

`st window`, `st agent`, `st toggle`, `st leave`, `st go --picker`, `st go --popup`,
`st roots --popup`, `st _run`, `st _agent`, `st _sessions`, `st _tree-next`, `st _tree-prev`,
the `st _split-*` commands and the `st _sidebar*` commands are called by the tmux bindings and the sidebar, not by hand.

---

## Keys

`M` is Meta — the Option/Alt key.

| key | does |
|---|---|
| `M-w` | tree picker: trees waiting on you first, then most recently opened, with a `+ new tree…` row |
| `M-e` | toggle between the first two configured windows |
| `M-Tab` | switch to the tree you were in before; press again to come back |
| `M-r` | new tree in any repo you have used `st new` in — pick the repo, then the branch; no `cd` needed |
| `M-1` … `M-9` | select a configured window (tab) by position |
| `M-n` / `M-p` | next / previous tab |
| `M-j` / `M-k` | next / previous open tree, in sidebar order |
| `M-h` / `M-l` | move to the pane on the left / right (the sidebar is leftmost) |
| `M-x` | close the focused pane after a y/n prompt (does nothing on the sidebar); a pane shown from another tree goes back to it instead |
| `M-v` | split side by side. With no other tree open, opens another copy of this tab's program (a fresh agent session in the agent tab). Otherwise a picker offers that copy first, then each open tree; picking a tree asks which of its tabs to show |
| `M-V` (Option-Shift-V) | same as `M-v`, above or below the current pane |
| `M-s` | focus the tree sidebar with the cursor on the tree you are in; Escape goes back to the tab |
| `M-B` (Option-Shift-B) | hide or show the sidebar in every tree window; showing it also focuses it |
| `M-q` | detach from tmux; keep the current tree running |
| `M-Q` (Option-Shift-Q) | leave tmux; keep the current tree session running |
| `M-/` | popup listing every key (`st keys` prints the same list) |

To change a key, set its variable in `~/.config/supertree/config` and re-run
the installer, which writes the keys into the installed tmux fragment:

```sh
ST_KEY_LAST=M-o         # M-Tab
ST_KEY_WINDOW_1=F1      # M-1   (also ST_KEY_WINDOW_2 … ST_KEY_WINDOW_9)
```

The others are `ST_KEY_PICKER` (`M-w`), `ST_KEY_TOGGLE` (`M-e`),
`ST_KEY_ROOTS` (`M-r`), `ST_KEY_NEXT_TAB` (`M-n`), `ST_KEY_PREV_TAB` (`M-p`),
`ST_KEY_NEXT_TREE` (`M-j`), `ST_KEY_PREV_TREE` (`M-k`), `ST_KEY_PANE_LEFT`
(`M-h`), `ST_KEY_PANE_RIGHT` (`M-l`), `ST_KEY_CLOSE_PANE` (`M-x`),
`ST_KEY_SPLIT` (`M-v`), `ST_KEY_SPLIT_VERTICAL` (`M-V`), `ST_KEY_SIDEBAR` (`M-s`), `ST_KEY_SIDEBAR_TOGGLE`
(`M-B`), `ST_KEY_HELP` (`M-/`), `ST_KEY_LEAVE` (`M-q`) and `ST_KEY_DETACH`
(`M-Q`). Use tmux key names made of letters, digits, `-`, `_` and `/`. Source installs load
`tmux/supertree.conf` as-is; add your own `unbind`/`bind` lines after it instead.
If an existing config uses `M-V` for `ST_KEY_SPLIT`, the installer assigns
`M-U` to the vertical split unless you set `ST_KEY_SPLIT_VERTICAL` yourself.

New trees (`M-r`, Ctrl-N in the picker, or its `+ new tree…` row) take two steps:
pick the repo (the one you are in comes first; skipped when there is only one), then
pick a local or remote branch or type a new name. Tab picks the branch to start
from, Ctrl-B toggles a bare tree (no deps, env, or agent). Names are checked with
`git check-ref-format`.

In the tree picker, press Enter to open a tree, Ctrl-N for a new tree, Ctrl-D to
delete the selected worktree without leaving the picker, Tab to mark several
trees for one Ctrl-D, Ctrl-R to pick a repo,
Ctrl-/ to show or hide the preview, or Escape to close. The preview shows the
tree's uncommitted files, unpushed commits, and the agent's last lines; it hides
itself in narrow terminals. The picker needs fzf 0.45 or newer. With fzf 0.73 or
newer it refreshes every two seconds while open and fills in the uncommitted
(`±`) and unpushed (`↑`) counts shortly after it opens.

Prefix stays `C-a`. `M-arrow` pane movement and `S-Enter` are untouched.
`status-left` shows the agent state and current repository/branch. Long tree names
keep their start and end with an ellipsis in the middle, so the tree you are
typing into stays identifiable.

Agent state appears in `st ls` and the `M-w` picker. `running` means the agent
process is active, `input` means its pane shows a prompt, `done` means the agent
exited, and `closed` means the tree session is not open. Prompt detection is a
best effort check of the pane; custom agents may need their own prompt pattern.

---

## Layout

```
~/projects/webauth                        main checkout
~/projects/.worktrees/webauth-2a305/feat-otp-53fa2  worktree
tmux session "webauth/feat-otp"                     windows: codex, nvim, shell
```

The 5-character IDs in worktree paths are the start of stable hashes of the main
checkout path and the exact branch name. They keep branches such as `feat/a-b`
and `feat-a-b`, and repositories with the same directory name, separate, while
keeping the path short enough to read in your prompt and in the agent's footer.
Trees created before this change keep their full 32-character paths and keep
working; so do worktrees from the even older path layout, when Git confirms the
exact repository and branch. State files (`idx`, `seen`, `trust`) still use the
full hash, so nothing is lost. Session names stay readable as `<repo>/<branch>`; a short hash is added
only when two trees would otherwise get the same name. Sessions opened under the
older hashed names are renamed the next time supertree lists trees. Windows are addressed
by **name**, not index, so your `base-index` setting is irrelevant.

Each program window drops to an interactive shell in the same worktree when its
program exits. Window order and selection are configurable.

### Trees side by side

`M-v` (or `M-V`) with another tree open asks what to put beside the current pane:
another copy of this tab, or one tab of another open tree. The other tree's
program moves over as it is, so its agent keeps the same conversation; that tree
shows a holder pane in its place until it goes back. Move between the halves
with `M-h` / `M-l`. Press `M-x` on either the moved pane or its holder, or Enter
in the holder, to send it home. Closing either tree also sends its pane home
first. The sidebar and tree status keep following the moved agent.

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
the configured harness name, such as `codex`; `vim` runs Neovim and labels its
tab `nvim`. At least one unique window is required.

Any other name is a custom tab. Give it a command with `ST_TAB_<name>`; names
use letters, digits and `_`. The command runs through `sh -c` in the worktree,
and the tab drops to a shell when it exits:

```sh
ST_WINDOWS='agent vim shell dev git'
ST_TAB_dev='npm run dev'
ST_TAB_git='lazygit'
```

Program panes use your terminal's background, without card padding or title
rows. Split panes get a quiet divider; a single program uses the full area.

Tree sessions show their windows as tabs at the top (`1. codex ●`, `2. nvim`,
`3. shell`). The active tab is underlined, and the current repository and branch
appear on the left after the agent's state (`●` running, `◆` waiting for you,
`○` exited). Other tmux sessions keep your own status line.

When an agent in another tree starts waiting for you, tmux shows a short notice
(`◆ webauth/feat-otp needs you · M-w`) to every terminal looking at a different
tree. Messages from the window keys (`M-e`, `M-1` … `M-9`, `M-Tab`) also appear
on tmux's message line. Set `ST_STATUS_RIGHT=1` in the config to add a running
count on the right of every tree's status line (`◆ 2 need you · M-w`). Set
`ST_NOTIFY=1` to also send a local desktop notice when an agent starts waiting
for you (via `osascript` on macOS or `notify-send` on Linux). Both settings are
available in `st config`.

Order controls startup focus, `M-1` through `M-9`, and the pair switched by
`M-e`. Reopening a tree adds newly configured windows and selects a newly
configured agent, while keeping other open programs running. Set `ST_SHELL`
in the config to override your login shell for new shell tabs and program
fallbacks. Re-run the installer once
after upgrading from v0.3.0 so the positional tmux bindings are installed.

## Sidebar

Every tree window gets a narrow pane on the left listing all trees, grouped by
repository:

```
webauth
  · main          closed
  ● feat-otp      agent running (bold: the tree you are in)
  ◆ crypto-mon    agent waiting for input

fe
  ○ old-branch    agent exited
```

Branches retain their full names. Long names are clipped at the sidebar edge;
the top bar and picker show the full repository and branch.

Double-click a tree to switch to it (a single click moves the highlight). The
agent's tab mirrors its state: `●` running, `◆` waiting for your input.

`M-j` / `M-k` step through open trees without touching the sidebar. Every switch
lands in the tab's program pane, never in the sidebar. In the sidebar, Enter
switches to the highlighted tree (`j`/`k` move, `g`/`G` jump to the ends, `l` opens, `h` or `q` go back), `n` opens the new branch flow,
`d` deletes the highlighted tree (same checks and y/n prompt as `st rm`, in a popup),
`m` marks trees so one `d` deletes them all after a single prompt (trees with unsaved work are skipped),
Escape returns to the tab, and Ctrl-R reloads by hand. `M-s` focuses the sidebar
and moves its cursor back to the tree you are in. `M-B` hides or shows it
everywhere; when it shows the sidebar it also focuses the one in the current
window.

One background refresher per tmux server keeps every sidebar current. It
re-reads agent states every 2 seconds and the tree list when sessions open or
close (or every 30 seconds), then pushes changes to each sidebar over fzf's
`--listen` socket under `~/.local/state/supertree/sidebar/`. The same refresher
drives the tab and status-line states and the "needs you" notices, so it also
runs with sidebars off; it stops once no sidebars and no tree sessions are left. It also closes a window whose only remaining pane is its
sidebar, for example after you `exit` the shell beside it; if that was the
tree's last window, you are moved to another session instead of being
disconnected. A sidebar closed by hand comes back within a few seconds; use
`M-B` to hide sidebars.

```sh
ST_SIDEBAR=0           # never add sidebars
ST_SIDEBAR_WIDTH=24    # columns
```

The sidebar needs `curl` and an fzf recent enough for `--listen`, `--id-nth`
and `--gutter`. Sessions opened before upgrading get their tabs and sidebar the
first time you switch into them (`st go`, `M-j`/`M-k`, the sidebar or `M-w`);
their running programs are left as they are.

## Agent harnesses

The installer creates `~/.config/supertree/config`. Change `ST_HARNESS`, then
close and resume a tree to rebuild it cleanly with the new agent window:

```sh
ST_HARNESS=codex       # claude | codex | openrouter
```

| value | terminal program | continuation command |
|---|---|---|
| `claude` | Claude Code (`claude`) | `claude --continue` |
| `codex` | Codex CLI (`codex --no-daemon`) | `codex --no-daemon resume --last` |
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
2. **Worktree** — `git worktree add` under `~/projects/.worktrees/<repo>-<id>/<branch>-<id>`.
3. **Deps** — symlinks each `ST_LINK_DIRS` entry from the main checkout, but only
   when the lockfile is byte-identical. If it differs, runs `ST_INSTALL_CMD`
   instead, so a branch that changed dependencies never silently runs main's
   `node_modules` or writes into it.
4. **Env** — copies each `ST_COPY_GLOBS` match from the main checkout. Copies,
   not symlinks, so a tree can diverge.
5. **Hook** — runs `ST_POST_CREATE`.

In a terminal, the install and the hook each show as one live line with the time
taken so far and their last three lines of output. If one fails, choose Enter to
open the tree anyway, `r` to run it again, or Escape to stop; the worktree is kept.

Build output (`.next`, `dist`) is never shared between trees.

### Per-repo config

Optional `.supertree` at the repo root, sourced by `st new` after you trust its
current contents:

```sh
cat .supertree    # review the commands it contains
st trust          # records this version for the repository
st new feat-otp
```

If `.supertree` changes, `st new` stops before creating a worktree. In a
terminal it shows what changed since you last trusted it (the whole file the
first time) and lets you press `t` to trust it, `v` to open it in your editor,
or Escape to stop; elsewhere it fails until you run `st trust` again.
`st trust --show` prints the same view. `st new --bare` does not execute it.

Example `.supertree`:

```sh
ST_LINK_DIRS=(node_modules)      # symlinked from main when the lockfile matches
ST_COPY_GLOBS=('.env*')          # copied from main
ST_INSTALL_CMD='npm ci'          # used when the lockfile differs (the default)
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
Trust covers only `.supertree` itself, not any script or file it sources or
runs; review those too.

The default `ST_INSTALL_CMD`, `npm ci`, runs the branch's own package install
scripts (such as `postinstall` and `prepare`) whenever its lockfile differs from
main's, so be careful creating trees from branches you do not trust.

---

## Closing, resuming, exiting

**Leave the agent, keep the tree open** — use the agent's exit command or
`Ctrl-D`. The window falls back to a shell in the worktree. `M-1` recreates the
configured agent window if it has been closed.

**Leave tmux** — `M-q` and `M-Q` detach your client and return to your terminal
shell. The tree session, agent, and editor keep running. Reattach with
`st` for the most recent tree, `st home` for home, `st resume [branch]` for a
specific tree, or `tmux attach`.

**Close the tree** — `st down`. Kills only the tmux session. Unsaved editor
buffers in that session are lost.

For `st down` and `st rm`, `st` moves attached clients off a session before
closing it — back to the session they came from when possible (remembered per
client tty under `~/.local/state/supertree/origin/`), otherwise to a remaining
session. `st down --all` can detach clients when it closes the last tmux
session. Tree sessions set `detach-on-destroy off` for themselves, so when the
last program in a tree exits you land in your most recent session instead of
leaving tmux; your global `detach-on-destroy` setting is left alone.

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
changes or unpushed commits unless you pass `--force`, lists what it will
remove, says up front whether the branch will be deleted (only if it is merged),
and asks. With `--force` and work that would be lost, you type the branch name
instead of `y`. In the `M-w` picker, highlight a tree and press Ctrl-D to run
the same guarded removal; the picker stays open afterwards. If the tree has
unsaved work, the picker lists it and offers Enter to open the tree, Ctrl-F to
force delete, or Escape to go back. The main checkout cannot be deleted.
Mark several trees with Tab and press Ctrl-D to delete them after one prompt;
trees with unsaved work are skipped and stay for a one-at-a-time delete.

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
| `~/projects/.worktrees/<repo>-<id>/<branch>-<id>` | new worktrees, 5-character IDs (old paths remain usable) |
| `~/.local/state/supertree/repos` | registered repository paths |
| `~/.local/state/supertree/recent` | trees in the order you last opened them; bare `st`, `st open`, and `M-w` read it |
| `~/.local/state/supertree/sidebar/` | sidebar row cache, refresher lock, and one fzf socket per sidebar |
| `~/.local/state/supertree/idx/<repo-id>/<branch-id>` | that tree's `ST_TREE_INDEX` |
| `~/.local/state/supertree/seen/<repo-id>/<branch-id>.<harness>` | marker meaning that agent has run here, drives continuation |
| `~/.local/state/supertree/trust/<repo-id>` | hash of the reviewed `.supertree` contents |
| `~/.local/state/supertree/trust/<repo-id>.sh` | copy of that version, used to show what changed |
| `~/.local/state/supertree/origin/<tty>` | which session a client came from, so closing a tree returns it there |

A repo only shows up in `st go` / `st ls` after `st new` or `st home` registers it. Override the
worktree root with `ST_WORKTREE_ROOT`, the state dir with `ST_STATE`.

---

## Output

In a terminal, messages are styled rows (`✓ removed …`, `! branch …`, `✗ …` with a
`try` hint). Set `NO_COLOR=1` to drop colour, or `ST_ASCII=1` for ASCII glyphs.
Some terminals can be set to draw symbols like `●` and `◆` two columns wide, which
shifts rows and tabs out of line; `ST_ASCII=1` fixes that.
Supertree uses explicit colours rather than your terminal's theme. Dark UI surfaces
use neutral charcoal (`#121212`) and matching RGB and 256-colour grays; accent
colours use approximations in 256-colour mode. Program panes keep your terminal's
background. Terminals limited to 16 colours use their own palette. Set
`ST_THEME=light` in the config for a light background, or `ST_THEME=ansi` to use
your terminal theme's own colours. `st _palette` prints every colour as your
terminal draws it. The pickers and the sidebar use the same colours and ignore
`FZF_DEFAULT_OPTS`, so your own fzf theme does not change them.
The Go controls and sharing messages use these same eight roles: cyan actions,
amber input/warnings, blue running, green verified success, red errors, muted
secondary text, and magenta repositories. Waiting questions receive bold
emphasis; a completed agent turn does not imply that the shared goal succeeded.
Existing tab/split/sidebar shortcuts remain unchanged. The local launcher forwards
theme and ASCII preferences into the controls, including popup launches. Terminal
fonts stay under your terminal's configuration.
When output is piped or redirected, `st` prints the plain `:: msg` and `st: msg`
lines it always has, so scripts keep working.

## Troubleshooting

**Agent colors are missing after updating** — Supertree now clears an inherited
`NO_COLOR` setting for new panes. Restart an already running agent to apply it.

**`M-w` does nothing** — your terminal is eating Option. In WezTerm, Option must
send Meta rather than composing characters. Your `M-arrow` pane binds are the
quick test: if those work, these do.

**Option-V does not split** — run `st split` from a Supertree pane. If that
works, run `st doctor` to check the generated and live `M-v` binding, then
`st repair` if either is missing. If the binding is present, set the terminal's
Option key to send Meta (or bind `ST_KEY_SPLIT` to a key your terminal sends).

**`M-Tab` or `M-1`–`M-9` do nothing on Linux** — the desktop or terminal
already uses them (Alt-Tab switches apps; Alt-digit switches terminal tabs).
Rebind them with `ST_KEY_*` in the config, as described under [Keys](#keys).

**`st go` says "no current client"** — run from a shell that is not attached to a
tmux client. Harmless from scripts; from a real pane it switches normally.

**A tree is missing from the picker** — its repo was never registered. Run
`st home` or `st new` once in that repo, or add the main checkout path to
`~/.local/state/supertree/repos`.

**`st new` ran a full install instead of linking** — the main checkout's lockfile
differs from the new tree's, usually because you have uncommitted lockfile
changes on main. Intended.

**The agent started fresh instead of continuing** — check `st doctor`, the
configured harness, and that tree's `seen` marker. Each agent keeps a separate
marker and its own native history.

**OpenRouter is selected but no models appear** — run `opencode`, enter
`/connect`, choose OpenRouter, then use `/models` to select a model.
