# Terminal stack feasibility experiment

This is a disposable native-terminal feasibility driver. It is a separate Go module and
imports none of Supertree's core. Normal `st`, tmux, fzf, the installer and the
workspace daemon do not use it. This README records the compatibility profile,
findings, remaining gates and reproduction commands.

The driver owns real PTYs in a separate process, interprets child output with
`x/vt`, and places cells in three independent Ultraviolet rectangles. The client
can detach or die while the host continues draining output and answering terminal
queries. Reattachment reads the current screen; it does not replay a raw ANSI log
or launch replacement programs.

Stage 2 selected this **pinned stack with the bounded Unicode repair** for the
native implementation. The driver remains a feasibility experiment. Its launcher
applies the repair to the checksum-pinned emulator in a disposable copy.
The unmodified published emulator remains separately reproducible and loses
combining/ZWJ graphemes across output chunks. Enhanced child keyboard encoding
is unavailable in this profile. This driver has no durable recovery,
version negotiation, multi-client input lease,
worktree integration, production process-tree shutdown, or release migration.
The repair passes the chunk-boundary and resource-bound regressions and upstream
tests; see below. The published dependency remains unchanged so
its failure and the repair can both be reproduced.

## Run in your existing terminal

Needs macOS/Linux, Go 1.24.2+, and Neovim for the default middle terminal. Builds
stay under ignored `.dev/terminal-stack/`. Start creates a private temporary host
namespace; save the printed path to reattach or stop it later.

```sh
state=$(./experiments/terminal-stack/run start)
./experiments/terminal-stack/run attach -state "$state"
```

Defaults are Bash, real Neovim, Bash. For the actual Codex/Vim/shell combination,
explicitly choose the programs (these are shell commands, not command discovery):

```sh
state=$(./experiments/terminal-stack/run start \
  -command 'exec codex --no-daemon' \
  -command 'exec nvim --clean' \
  -command 'exec /bin/bash --noprofile --norc -i')
./experiments/terminal-stack/run attach -state "$state"
```

The actual harness owns login, conversation, input and approvals. Starting it
does not fabricate a transcript or turn on the normal Supertree agent tools.
Custom commands and interactive configuration use your normal environment unless
you supply an isolated environment explicitly. Automated tests use scratch homes.

| Experiment key | Action |
| --- | --- |
| `Option-1` / `Option-2` / `Option-3` | Restore or focus the corresponding terminal |
| `Option-0` | Hide the focused view; its program continues and remaining views expand |
| `Option-Shift-H` / `Option-Shift-L` | Move the focused view left/right |
| `Option-h` / `Option-l`, `Option-p` / `Option-n` | Focus previous/next visible terminal |
| `Option-z` | Toggle a focused-only view; other terminals continue |
| `Option-q` | Detach the client; host continues |

No prefix is active by default: Ctrl-a belongs to the child program. For a
terminal without usable Option/Meta, `attach -legacy-prefix -state "$state"`
explicitly enables the previous proof's Ctrl-a fallback (numbers, 0, H/L, n/p,
z and d; Ctrl-a twice passes it through). The final native default also has no
reserved prefix. Key configuration belongs to the later native configuration.

Example: Option-3, then Option-0 hides shell and expands Codex/Vim. Option-2,
then Option-Shift-H moves existing Vim left; Option-3 restores the same shell.
Even all views can be hidden: Option-1/2/3 restores one, with no replacement
program. The numbered selectors identify original terminals in this proof,
not the product's future tab model. This client-only order/visibility resets on
reattach; native per-worktree layout persistence remains in Stages 3/6.

Ordinary keys, Ctrl-C, Ctrl-Z and Escape go to the focused child. Bracketed paste
is decoded as paste and re-encoded for that child's mode; shortcut-shaped pasted
text does not detach. These proof keys do not change the product's bindings.
Narrow terminals show the focused program. Mouse input is not routed by this
keyboard proof; SGR mouse encoding has a separate emulator fixture.

After detaching or closing the outer terminal:

```sh
./experiments/terminal-stack/run attach -state "$state"
./experiments/terminal-stack/run inspect -state "$state"   # JSON cells, PIDs, counters
./experiments/terminal-stack/run stop -state "$state"      # ends only this experiment
```

`stop` terminates the experiment's programs, including unsaved editor contents.
Host loss does not preserve PTYs. A stale namespace is an inspection case, never
permission to silently replace its programs; `start` refuses an existing host log
or lock. Stop before recreating an experiment after changing its protocol/code.

## Reproduce the checks

Use the repository's isolated runner from the repository root:

```sh
bash scripts/test-local.sh -- go -C experiments/terminal-stack vet ./...
bash scripts/test-local.sh -- go -C experiments/terminal-stack test -race -timeout 180s -v ./...
bash scripts/test-local.sh -- go -C experiments/terminal-stack run ./cmd/spike probe
bash scripts/test-local.sh -- go -C experiments/terminal-stack run ./cmd/spike measure -terminals 16 -duration 2s
```

The real Neovim cases skip if it is unavailable; that does not satisfy the editor
gate. The opt-in Codex smoke uses an empty scratch `CODEX_HOME`, no credentials,
`--no-daemon`, and no model request. It verifies onboarding startup only:

```sh
bash scripts/test-local.sh -- env ST_STACK_HARNESS_PROBE=1 \
  go -C experiments/terminal-stack test -race -run TestOptionalCodexScratchStartup -v
```

`probe` reports each dependency behavior with `Passed`, `Expected`, and `Actual`;
its successful execution is not approval of failed probes. The driver correctness
suite and compatibility decision are deliberately distinct. `measure` launches
2–64 real PTYs, one continuously noisy, with 256 scrollback lines and 80x24 cells.
It reports retained Go heap/System-memory deltas, output volume and quiet-screen
snapshot acquisition times. It excludes child RSS, outer-terminal rendering and
actual input-to-paint latency. The 10 ms observation interval in responsiveness
tests is also not a typing-latency measurement.

The standalone CI workflow exercises this module on macOS/Linux and reports the
dependency probes. It does not certify authenticated harness, terminal-vendor,
SSH, font, or daily-use acceptance.

## Explicit Unicode repair trial

`trial` copies the checksum-pinned `x/vt v0.1.0` sources to a temporary directory,
applies [a two-file patch](patches/x-vt-unicode.patch), and uses a temporary Go
module replacement. It changes neither Go module file and never writes to the
original module cache. The [upstream MIT notice](patches/LICENSE.x-vt) is retained.

```sh
bash scripts/test-local.sh -- experiments/terminal-stack/trial upstream-test -race -timeout 180s
bash scripts/test-local.sh -- experiments/terminal-stack/trial test -race -timeout 180s -v ./...
bash scripts/test-local.sh -- experiments/terminal-stack/trial run ./cmd/spike probe
```

The repair amends the preceding visible cell when another rune extends its
grapheme, both within and across writes. It displays ordinary text immediately,
preserves cursor and wrap state, and respects intervening terminal operations. Tests split at
every byte boundary and deliver bytes individually; a real PTY helper requires
an acknowledgement between chunks so scheduling cannot conceal the defect.

Each displayed cluster retains at most **256 UTF-8 bytes**. If a joining rune
would exceed that limit, the renderer consumes it and emits a visible U+FFFD
replacement as a new cluster. Subsequent runes start from that replacement.
Inspection exposes `GraphemeOverflows`; the policy does not depend on PTY read
boundaries. Each segmentation/copy sees at most 260 bytes, bounding work per rune
and retained cell content even for a never-ending cluster. This is an explicit
resource limit, rather than a claim to preserve arbitrarily long Unicode clusters.
Control sequences still reach the parser in order. The repair retains the
upstream MIT notice and is exercised by the upstream suite on each supported OS.

Review covered cursor/phantom wrap, scrolling, width changes, style/control
boundaries, character sets, REP, resize, external cell mutation, regional
indicators, Hangul, Indic and Prepend-before-ASCII cases. Three hundred mixed
strings in three delivery patterns also match the pinned library's whole-string
segmentation reference. Ordinary clusters use the pinned segmenter's Unicode 17
implementation of [grapheme rules](https://www.unicode.org/reports/tr29/);
the byte limit is a deliberate application policy.

On the reference macOS machine, 10,000 and 100,000 separately written marks took
about 52 ms and 529 ms with race instrumentation. These are stress measurements,
not typing latency. The real-PTY stress check proves that inspection reports the
overflow and that normal output still works afterward.

For the user's cmux/WezTerm + Oh My Zsh manual check, build the repaired driver:

```sh
./experiments/terminal-stack/trial build -o "$PWD/.dev/terminal-stack/unicode-spike" ./cmd/spike
```

Then run these commands one at a time in the terminal being checked:

```sh
state=$(.dev/terminal-stack/unicode-spike start \
  -command 'exec codex --no-daemon' \
  -command 'exec nvim --clean' \
  -command 'exec /bin/zsh -l')
.dev/terminal-stack/unicode-spike attach -state "$state"
```

This explicit manual launch uses the user's actual Codex login and Zsh setup.
Type multiline input, paste, interrupt/scroll, and answer a real approval in
Codex; inspect the editor/shell, detach with `Option-q`, and run the attach
command again. Record actual observations in the plan. When finished, stop this
test host with `.dev/terminal-stack/unicode-spike stop -state "$state"`.

An optional desktop smoke check starts two dedicated WezTerm GUI instances in
sequence with scratch configuration. It tests Unicode paste, retained editor
contents/PIDs after reattach, and stock Zsh; it does not load Oh My Zsh or submit
a model request. It requires an already installed WezTerm and a graphical login:

```sh
./experiments/terminal-stack/run probe
bash scripts/test-local.sh -- python3 experiments/terminal-stack/verify_wezterm.py
```

The helper addresses only its own WezTerm socket/pane, tests hide/reveal/reorder,
closes its test windows,
and stops its own PTY host. It does not join an existing WezTerm GUI instance.
Default CI omits this desktop-only check. Automated screen text is not human font,
comfort, plugin, or approval-flow acceptance.

## Plain SSH and manual acceptance

Build/start the same driver in a private namespace on a disposable Linux host.
Use the full remote paths printed by setup, without starting a normal `st` daemon.
Then connect from your existing terminal:

```sh
ssh -t user@host 'TERM=xterm-256color /path/to/spike attach -state /tmp/st-terminal-stack.EXAMPLE/ssh-state'
```

The opt-in automated SSH case uses an already prepared host with Bash, Neovim and
a third terminal producing output. It kills only its own local SSH client, then
verifies unchanged remote PIDs, continued hidden output, unsaved editor contents,
resize and graceful detach. Pass explicit parameters after the isolation runner:

```sh
bash scripts/test-local.sh -- env \
  ST_STACK_SSH_TARGET=user@host \
  ST_STACK_SSH_STATE=/tmp/st-terminal-stack.EXAMPLE/ssh-state \
  ST_STACK_SSH_BIN=/tmp/st-terminal-stack.EXAMPLE/spike \
  go -C experiments/terminal-stack test -race -run TestOptionalPlainSSHDisconnectAndReattach -v
```

Use an authenticated **real** harness for live acceptance: enter
multiline input, paste, submit a bounded task, interrupt, scroll, answer its own
approval, focus another terminal, resize, detach, and reopen the same conversation.
Repeat in the actual daily terminal and a conventional xterm-compatible terminal,
then through plain SSH. Record terminal/version/OS and observations in the plan.
Neither onboarding startup nor a model stub is evidence for this acceptance gate.

## Pins and limitations

### Stack profile and acceptance status

The selected Stage 4 stack is **creack/pty + the pinned x/vt with the bounded
repair + matching Ultraviolet**. Keep the repair as a small versioned patch over
the checksummed upstream source, retain its license, and rerun both upstream and
driver suites on every update. The experimental launcher now uses this repair;
the main Go module and installed product have not adopted it. Stage 2's live
functional acceptance completed on November 28, 2026 (IST). Production host,
recovery, compatibility completion and release gates remain in later stages.
The bounded patch's SHA-256 is
`7b96db5fde9808313b2d1aaa6bde7452c9f8998a3d3d464caf15d98474461af2`.

| Surface | Verified with the bounded repair | Remaining acceptance |
| --- | --- | --- |
| macOS arm64 | Upstream and driver race tests; real Bash/Zsh/Neovim, Unicode/paste/query handling, client death/reconnect, view controls and full authenticated GUI harness workflow | Subjective visual review and broader daily use |
| Linux amd64 | Upstream and driver race tests; real shell/job control, editor, Unicode, output/resource tests and full authenticated SSH harness workflow | Broader distribution/configuration coverage |
| WezTerm `20240203-110809-5046fc22` | Full real GUI Codex `0.172.0` sequence: multiline/Unicode paste, model submission, transcript paging, interruption, client death/reconnect, approval UI and declined write | Subjective font/comfort review and arbitrary user configuration |
| kitty `0.42.2` fallback | Full real GUI Codex `0.172.0` sequence, including all eight observations and graceful detach | Subjective font/comfort review and arbitrary user configuration |
| Local real Codex `0.172.0` in an allocated PTY | Actual model reply, multiline/paste, transcript paging, interruption, same conversation/PIDs after client death, real approval UI and declined write | Functional sequence also passed in both GUI profiles |
| Plain SSH to Linux | Disconnect/reattach, unchanged PIDs, hidden output, resize and unsaved Unicode editor contents; complete authenticated Codex `0.171.0` sequence including scroll, interrupt and declined approval | Other SSH clients, latency profiles and distributions |
| Oh My Zsh | Installed framework `c0140703a53eb0d22be44f7bac318d0baacc927c`, user's `amuse` theme and `git` plugin, Unicode paste, suspend/fg/interrupt and resize in an isolated home | Arbitrary user dotfile customizations are not covered by that fixture |
| cmux | Not installed on the reference machine | Unverified; no compatibility claim |

Live checks encountered low-quota prompts. One automated Enter selected a model
in a disposable session; the normal `gpt-6.1-sol` / high configuration was checked
and remained unchanged. The helper now stops before writing any input when a
low/exhausted-quota notice appears. It never navigates model choosers. A blocked
run exits 2 and does not count as passing acceptance. After quota reset, both GUI
profiles and plain SSH passed the complete sequence without model overrides or
quota dialogs. These are live functional checks through real harness/terminal
UIs; they do not substitute for human font/comfort preferences or release soak.

The Linux host initially had a standalone Codex binary without its terminal-tool
host executable. The final run used a private copy of the same `0.171.0` CLI,
with SHA-256-verified matching code-mode host and exec-server assets from the
[official release](https://github.com/openai/codex/releases/tag/rust-v0.171.0).
The system installation and account configuration were unchanged. This fixture
requirement belongs to the external harness, not to Supertree's terminal engine.

This profile advertises child `TERM=xterm-256color`, true color and legacy
key/paste encoding. Ctrl-j is the portable Codex newline binding. Kitty/CSI-u/
modifyOtherKeys negotiation and modified Enter are not supported child protocols;
outer-event decoding alone does not enable them. Mouse routing, graphics,
clipboard passthrough, and a client scrollback browser are absent. Use Codex's
own transcript browser for its history. See the native stages for those later
features; do not advertise them as working from these tests.

Run the guarded live verifier explicitly, using an existing authenticated
`CODEX_HOME` and the repaired binary. It makes bounded real model requests and
creates disposable Codex sessions/folder-trust records through the real UI.
Credentials are not copied. Test commands run read-only; the requested marker
write is declined. GUI instances have private configuration, sockets and panes.

```sh
./experiments/terminal-stack/trial build -o "$PWD/.dev/terminal-stack/bounded-spike" ./cmd/spike
bash scripts/test-local.sh -- python3 experiments/terminal-stack/verify_harness.py \
  --authenticated --codex-home "$HOME/.codex" \
  --binary "$PWD/.dev/terminal-stack/bounded-spike" --outer wezterm
# Use --outer kitty --outer-bin /path/to/kitty for the installed fallback.
# Omit --outer for the local PTY check.
```

SSH additionally requires `--ssh user@host`, `--remote-directory` naming an
operator-created private `/tmp/st-stack-*` namespace, and remote `--binary`/
`--nvim` paths. `--codex-bin` can select an existing complete remote Codex bundle;
its matching helper executables must be installed beside the CLI. It uses the
remote account's existing login, disables SSH agent forwarding and copies no
credentials. The operator owns remote namespace cleanup after the programs are
stopped. The verifier continuously drains its outer PTY during SSH inspections,
so inspection latency cannot block the client's output/input loop. The kitty
fixture explicitly quits its private app instance when its last window closes.
Default CI runs model-free input-guard and PTY-draining tests, not live auth.
An operator can explicitly choose `--model` for a disposable test session.
`--allow-low-quota` additionally requires that model choice; it permits warning
banners but still stops on exhausted quota or a model chooser. Neither option
changes the normal model configuration. Without these flags, low quota blocks
further test input.

For the installed Oh My Zsh framework check without reading user dotfiles:

```sh
bash scripts/test-local.sh -- env ST_STACK_OMZ_ROOT="$HOME/.oh-my-zsh" \
  experiments/terminal-stack/trial test -race -run TestOptionalOhMyZshThemeAndGitPlugin -v
```

| Component | Exact trial version | License / toolchain |
| --- | --- | --- |
| `creack/pty` | `v1.1.24` (`edfbf75025b0ba4ee17c19f52d9b600fad80a787`) | MIT; module Go 1.18 |
| `charmbracelet/x/vt` | `v0.1.0` (`43427fdbad12fde220b737fc36303291a5ce4602`) | MIT; Go 1.24.2 |
| Ultraviolet | `v0.0.0-20260303162955-0b88c25f3fff` | MIT; Go 1.24.2; matches the emulator's dependency |
| `x/sys`, `x/term` | `v0.41.0`, `v0.36.0` | BSD-3-Clause |

`go.mod`/`go.sum` freeze the complete graph. The trial does not upgrade the main
module's Go or Charm dependencies. Latest Ultraviolet was inspected separately;
its newer toolchain requirement does not justify changing the main project.

Known bounds: at most 3 displayed terminals; 2–240 columns by 2–100 rows per
child; 8 MiB wire-message limit; 30 Hz full-snapshot polling; 64 KiB per text
request; 128 bounded input/reply chunks; 250 ms PTY write deadline. Saturated
input/replies can be lost and are reported in inspection counters/errors; this
is not production input delivery. Child title, clipboard, links, graphics and
passthrough escape side effects are not forwarded. No enhanced keyboard protocol
is advertised. Scrollback is retained for measurement but has no client browser.

The parser currently allocates 4 MiB per emulator. The reference 80x24 workload
retains about 4.5 MiB Go heap per terminal; this excludes child RSS and is not a
namespace memory ceiling. The Unicode failure is repaired in the launcher.
The stack choice authorizes the planned native implementation; the disposable
host protocol and its resource/input limits are not production acceptance.

Emacs-style workspace macros are planned in Stage 11, with action-catalog/config
groundwork in Stages 7/8. They will record/replay named workspace actions and
parameterized workflows. This terminal proof does not claim a full macro engine
or record terminal text and approval answers as workspace actions.
