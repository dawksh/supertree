#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
command -v nvim >/dev/null || { printf 'skip: program theme needs Neovim\n'; exit 0; }
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
export ST_CONFIG="$TEST_ROOT/config" ST_STATE="$TEST_ROOT/state" ST_THEME_TEST_ROOT="$TEST_ROOT"
printf '#!/bin/sh\nexit 0\n' > "$TEST_ROOT/shell"
chmod +x "$TEST_ROOT/shell"
printf 'ST_SHELL=%q\n' "$TEST_ROOT/shell" > "$ST_CONFIG"
cat > "$TEST_ROOT/init.lua" <<'LUA'
vim.api.nvim_set_hl(0, 'Normal', { fg = '#abcdef', bg = '#112233' })
vim.api.nvim_set_keymap('n', '<F12>', ':echo "keep mapping"<CR>', {})
vim.api.nvim_buf_set_lines(0, 0, -1, false, { 'unsaved human content' })
vim.bo.modified = true
-- A user's colourscheme callback can run after the launch overlay's callback.
vim.api.nvim_create_autocmd('ColorScheme', { callback = function()
  vim.api.nvim_set_hl(0, 'Normal', { fg = '#abcdef', bg = '#445566' })
end })
LUA
cat > "$TEST_ROOT/check.lua" <<'LUA'
vim.defer_fn(function()
  local ok, err = pcall(function()
    local theme = vim.env.ST_THEME or 'dark'
    local native = vim.env.ST_PROGRAM_THEME == 'native' or theme == 'ansi'
    local expected = native and 0x112233 or (theme == 'light' and 0xf6f7f9 or 0x0f131b)
    assert(vim.api.nvim_get_hl(0, { name = 'Normal' }).bg == expected, 'initial background did not follow launch theme')
    assert(vim.api.nvim_get_current_line() == 'unsaved human content' and vim.bo.modified, 'theme changed the buffer')
    assert(vim.fn.maparg('<F12>', 'n'):find('keep mapping', 1, true), 'theme replaced a mapping')
    vim.api.nvim_exec_autocmds('ColorScheme', { pattern = 'late-plugin-theme' })
    vim.wait(50)
    local after = native and 0x445566 or expected
    assert(vim.api.nvim_get_hl(0, { name = 'Normal' }).bg == after, 'late colourscheme displaced workspace background')
    if not native then
      for _, name in ipairs({ 'NormalNC', 'EndOfBuffer', 'SignColumn', 'LineNr', 'MsgArea' }) do
        assert(vim.api.nvim_get_hl(0, { name = name }).bg == expected, name .. ' left a different surface')
      end
      assert(vim.api.nvim_get_hl(0, { name = 'String' }).fg == (theme == 'light' and 0x126c34 or 0x9fdc7c), 'syntax palette differs')
    end
  end)
  if not ok then
    vim.fn.writefile({ tostring(err) }, vim.env.ST_THEME_TEST_ROOT .. '/failed')
  else
    vim.fn.writefile({ 'ok' }, vim.env.ST_THEME_TEST_ROOT .. '/passed')
  end
  vim.cmd('qa!')
end, 30)
LUA
for spec in 'dark workspace' 'light workspace' 'dark native' 'ansi workspace'; do
  read -r theme mode <<< "$spec"
  rm -f "$TEST_ROOT/passed" "$TEST_ROOT/failed"
  ST_THEME=$theme ST_PROGRAM_THEME=$mode "$ROOT/bin/st" _run nvim --headless -u "$TEST_ROOT/init.lua" \
    -c 'lua dofile(vim.env.ST_THEME_TEST_ROOT .. "/check.lua")' > "$TEST_ROOT/output" 2>&1
  if [ ! -f "$TEST_ROOT/passed" ]; then
    cat "$TEST_ROOT/output" "$TEST_ROOT/failed" >&2; exit 1
  fi
done
# Nothing was persisted in the user's init; native Neovim retains its colours.
ST_THEME=dark ST_PROGRAM_THEME=native nvim --headless -u "$TEST_ROOT/init.lua" \
  -c 'lua dofile(vim.env.ST_THEME_TEST_ROOT .. "/check.lua")' > "$TEST_ROOT/output" 2>&1
if [ -f "$TEST_ROOT/failed" ]; then cat "$TEST_ROOT/failed" >&2; exit 1; fi
printf 'ok: launch-local Vim palette, delayed colourschemes, native/ANSI opt-out, and preserved buffers/mappings\n'
