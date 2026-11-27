# Terminal stack feasibility experiment

This is a disposable native-terminal feasibility driver. It is a separate Go module and
imports none of Supertree's core. Normal `st`, tmux, fzf, the installer and the
workspace daemon do not use it. The canonical plan records findings and remaining
gates; this README describes how to reproduce them.

The driver owns real PTYs in a separate process, interprets child output with
`x/vt`, and places cells in three independent Ultraviolet rectangles. The client
can detach or die while the host continues draining output and answering terminal
queries. Reattachment reads the current screen; it does not replay a raw ANSI log
or launch replacement programs.

The experiment is **not an approved production stack**. Run `probe` as well as the
tests: the pinned emulator currently loses combining/ZWJ graphemes across output
chunk boundaries. Enhanced child keyboard encoding is also unavailable. This
driver has no durable recovery, version negotiation, multi-client input lease,
worktree integration, production process-tree shutdown, or release migration.
An explicit, reproducible repair trial now passes the chunk-boundary regressions
and the upstream tests; see below. The published dependency remains unchanged so
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

The trial amends the preceding visible cell when a subsequent output chunk
extends its grapheme. It displays ordinary text immediately, preserves cursor
and wrap state, and respects intervening terminal operations. Tests split at
every byte boundary and deliver bytes individually; a real PTY helper requires
an acknowledgement between chunks so scheduling cannot conceal the defect.

It remains an **upstream repair trial**, not a selected maintained fork. Very
long clusters delivered one combining mark at a time require repeated copying
and segmentation; the measured 2,000-mark case is not a linear-time guarantee.
Bounded hostile-output policy and upstream review remain part of the decision.

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

Use an authenticated **real** harness for the remaining manual check: enter
multiline input, paste, submit a bounded task, interrupt, scroll, answer its own
approval, focus another terminal, resize, detach, and reopen the same conversation.
Repeat in the actual daily terminal and a conventional xterm-compatible terminal,
then through plain SSH. Record terminal/version/OS and observations in the plan.
Neither onboarding startup nor a model stub is evidence for this acceptance gate.

## Pins and limitations

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

The parser currently allocates 4 MiB per emulator. The plan records measured
memory and exact Unicode reproduction. Resolve that compatibility failure and
finish the manual gates before choosing this stack for production.

Emacs-style workspace macros are planned in Stage 11, with action-catalog/config
groundwork in Stages 7/8. They will record/replay named workspace actions and
parameterized workflows. This terminal proof does not claim a full macro engine
or record terminal text and approval answers as workspace actions.
