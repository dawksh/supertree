# Desired workspace layout: incremental foundation

`layout.inspect` and `layout.save` are available through the command registry and daemon RPC. They store **desired metadata only**: saving does not rearrange, focus, start, stop, adopt, or otherwise change tmux panes. There is no user-facing layout editor yet. Existing workflows remain unchanged.

Each workspace has an independent saved layout. Inspect with `{"workspace_id":"<id>"}`. A never-saved workspace returns revision 0 and no root. Save with `{"workspace_id":"<id>","expected_revision":0,"root":{...}}`; omit root to save an empty layout. Subsequent saves require the revision returned by inspect/save; a stale revision is a conflict. The root is a pane `{"type":"pane","resource_id":"<process-id>"}` or a split such as:

```json
{"type":"split","direction":"horizontal","ratio":0.6,"children":[{"type":"pane","resource_id":"<editor-process-id>"},{"type":"pane","resource_id":"<shell-process-id>"}]}
```

Process IDs must already be known in the selected workspace, including processes that are no longer running. A resource may appear only once; splits have exactly two children, horizontal/vertical direction and a ratio between 0.05 and 0.95 inclusive. Layouts are limited to 255 nodes, 32 levels and 64 KiB. The resource identifiers are Supertree process IDs, not tmux pane IDs. These commands do not automatically remove stale references after a save.

## Live capture identity checks

Capture rejects selected process IDs with duplicate tmux tags anywhere on the
server, including other windows. It checks global identities before and after
reading the selected window. Tmux state and the saved metadata are not one
transaction; another client can still change tags after the final check.

## Explicit live restore

`layout.restore` is opt-in; saving remains metadata-only. Invoke it with
`{"workspace_id":"<id>","expected_revision":1,"expected_layout":"<tmux window_layout>"}`.
Obtain the token using `layout.live` with `{"workspace_id":"<id>","process_id":"<anchor>"}`; it requires a running managed process in the saved tree and returns `revision`, `window_id`, and `expected_layout`. Alternatively inspect tmux on the same socket. The live value is an explicit compare-and-apply token,
not the desired layout. The command rejects a stale saved revision or live token;
it does not silently overwrite a layout changed since the caller inspected it.
The returned `observed_layout` is the verified tmux layout and can be used as the
next live token.

A pane or nested binary split tree is supported when all referenced resources are
live, uniquely tagged managed/registered processes in one existing window, in the
same pane order as the saved tree, and that window contains no other panes. It
validates dimensions before applying the layout. It never starts, stops, joins,
breaks, adopts or reorders panes; layouts spanning windows, reversed pane order,
zoomed windows, observed-only panes, and layouts that do not fit are unsupported
and refused. Restore does not change the saved revision or deliberately focus a
pane. Tmux assigns layout geometry by pane order, so pane IDs alone cannot make
reordering safe.

Tmux and the core are not a single transaction. The final check and apply run in
the same tmux command queue. The check compares layout, zoom, and the complete
ordered window pane IDs, process tags, dead flags, and pane PIDs against preflight;
a changed value refuses the apply. Preflight also requires each pane ID to match
the process record, not just its tag. The guard accepts process IDs containing
only ASCII letters, digits, underscores and hyphens; tmux must support pane-loop
formats (unsupported formats fail closed). External clients may still
change state before or after the guarded operation, including tags in other windows,
focus and the programs running inside a pane. An error with code `partial_failure`
means an apply or its verification may have occurred: inspect actual tmux state
before retrying. No rollback is attempted. Interactive focus, multiple clients,
macOS and SSH have not been acceptance-tested.

## Still needed

The TUI offers explicit capture and restore controls, but this is not the full workspace-buffer specification. A normal attached tmux client and workspace switcher, split/move/resize/focus/close interactions, resource roles and explicit close behavior, general reconciliation and recovery, layout events/snapshot integration, and focus persistence remain to be implemented. Process launch security and daemon startup are separate stabilization work.
