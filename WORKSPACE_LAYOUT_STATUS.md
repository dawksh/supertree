# Desired workspace layout: incremental foundation

`layout.inspect` and `layout.save` are available through the command registry and daemon RPC. They store **desired metadata only**: saving does not rearrange, focus, start, stop, adopt, or otherwise change tmux panes. There is no user-facing layout editor yet. Existing workflows remain unchanged.

Each workspace has an independent saved layout. Inspect with `{"workspace_id":"<id>"}`. A never-saved workspace returns revision 0 and no root. Save with `{"workspace_id":"<id>","expected_revision":0,"root":{...}}`; omit root to save an empty layout. Subsequent saves require the revision returned by inspect/save; a stale revision is a conflict. The root is a pane `{"type":"pane","resource_id":"<process-id>"}` or a split such as:

```json
{"type":"split","direction":"horizontal","ratio":0.6,"children":[{"type":"pane","resource_id":"<editor-process-id>"},{"type":"pane","resource_id":"<shell-process-id>"}]}
```

Process IDs must already be known in the selected workspace, including processes that are no longer running. A resource may appear only once; splits have exactly two children, horizontal/vertical direction and a ratio between 0.05 and 0.95 inclusive. Layouts are limited to 255 nodes, 32 levels and 64 KiB. The resource identifiers are Supertree process IDs, not tmux pane IDs. These commands do not automatically remove stale references after a save.

## Still needed

This is not the interactive MVP or full workspace-buffer specification. A normal attached tmux client and workspace switcher, split/move/resize/focus/close interactions, resource roles and explicit close behavior, live-layout observation/application, reconciliation and recovery, layout events/snapshot integration, focus persistence, partial failures and terminal-size handling, and external-change policy remain to be implemented. No command here guarantees that a saved layout is currently displayed or can fit. Process launch security and daemon startup are separate stabilization work. Linux tests and a macOS cross-compilation are not a substitute for running interactive acceptance tests on macOS and over SSH.
