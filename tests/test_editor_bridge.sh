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
if [ "$1" = share ]; then printf '{"status":"sent","delivery":{"id":"delivery-editor"}}\n'
else printf '{"id":"ctx-editor"}\n'; fi
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
vim.cmd('2,3SupertreeAsk Explain this selected code')
assert(vim.wait(2000, function() return vim.fn.filereadable(base .. '/captured') == 1 end), 'sharing did not invoke CLI')
actual = vim.fn.readfile(base .. '/captured')
assert(actual[1] == 'share' and actual[2] == '--json', 'editor bypassed shared delivery action')
assert(actual[4] == base .. '/checkout with spaces/file.lua' and actual[6] == '2' and actual[8] == '3', 'sharing changed the selected range')
assert(actual[9] == '--message' and actual[10] == 'Explain this selected code', 'question was split or changed')
vim.fn.delete(base .. '/captured')
vim.ui.input = function(_, callback) callback(nil) end
vim.cmd('2,3SupertreeAsk')
vim.wait(100)
assert(vim.fn.filereadable(base .. '/captured') == 0, 'cancelling a question still sent context')
vim.api.nvim_buf_set_lines(0, 0, 1, false, {'unsaved human edit'})
vim.cmd('2,3SupertreeContext')
vim.cmd('2,3SupertreeAsk Unsaved text must stay private')
vim.wait(100)
assert(vim.fn.filereadable(base .. '/captured') == 0, 'unsaved editor state was silently treated as disk content')
vim.cmd('qa!')
LUA
nvim --headless -u NONE -c 'lua dofile(vim.env.SUPERTREE_EDITOR_TMP .. "/check.lua")' > "$task_tmp/output" 2>&1 || {
  cat "$task_tmp/output" >&2; exit 1;
}
printf 'ok: Neovim capture/share preserve selected lines, questions, cancellation and unsaved buffers\n'
