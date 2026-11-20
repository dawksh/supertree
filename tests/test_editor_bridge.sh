#!/usr/bin/env bash
set -euo pipefail
task_repo=$(cd "$(dirname "$0")/.." && pwd)
command -v nvim >/dev/null || { printf 'skip: editor bridge needs Neovim\n'; exit 0; }
task_tmp=$(mktemp -d)
task_tmp=$(cd "$task_tmp" && pwd -P)
trap 'rm -rf "$task_tmp"' EXIT
export SUPERTREE_EDITOR_ROOT=$task_repo SUPERTREE_EDITOR_TMP=$task_tmp
mkdir -p "$task_tmp/checkout with spaces"
printf 'first\nselected\nthird\n' > "$task_tmp/checkout with spaces/file.lua"
cat > "$task_tmp/client" <<'CLIENT'
#!/bin/sh
printf '%s\n' "$@" > "$SUPERTREE_EDITOR_TMP/captured"
printf '{"id":"ctx-editor"}\n'
CLIENT
chmod +x "$task_tmp/client"
cat > "$task_tmp/check.lua" <<'LUA'
local base = vim.env.SUPERTREE_EDITOR_TMP
local bridge = dofile(vim.env.SUPERTREE_EDITOR_ROOT .. '/editor/supertree.lua')
bridge.setup({command={base .. '/client'}})
vim.cmd.edit(vim.fn.fnameescape(base .. '/checkout with spaces/file.lua'))
vim.cmd('2,3SupertreeContext')
assert(vim.wait(2000, function() return vim.fn.filereadable(base .. '/captured') == 1 end), 'bridge did not invoke CLI')
local actual = vim.fn.readfile(base .. '/captured')
assert(actual[1] == 'context' and actual[2] == 'add', 'bridge bypassed shared context command')
assert(actual[4] == base .. '/checkout with spaces/file.lua', 'file path was split')
assert(actual[6] == '2' and actual[8] == '3', 'selection range changed')
vim.fn.delete(base .. '/captured')
vim.api.nvim_buf_set_lines(0, 0, 1, false, {'unsaved human edit'})
vim.cmd('2,3SupertreeContext')
vim.wait(100)
assert(vim.fn.filereadable(base .. '/captured') == 0, 'unsaved editor state was silently treated as disk content')
vim.cmd('qa!')
LUA
nvim --headless -u NONE -c 'lua dofile(vim.env.SUPERTREE_EDITOR_TMP .. "/check.lua")' > "$task_tmp/output" 2>&1 || {
  cat "$task_tmp/output" >&2; exit 1;
}
printf 'ok: Neovim bridge captures saved selection through CLI and preserves unsaved buffers\n'
