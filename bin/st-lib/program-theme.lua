-- A launch-local overlay: keep the user's plugins, mappings and buffers while
-- matching Supertree's workspace. Never write a Neovim configuration file.
local M = {}
local applying = false

local palettes = {
  dark = {
    mode = 'dark', text = '#d4d8e0', muted = '#a0aabd', panel = '#0f131b', bar = '#151a24',
    selected = '#232b3a', border = '#3a4458', accent = '#5ccfe6', input = '#ffb454',
    blue = '#73b8ff', green = '#9fdc7c', red = '#ff7a7a', repo = '#d2a6ff',
  },
  light = {
    mode = 'light', text = '#1f2733', muted = '#566170', panel = '#f6f7f9', bar = '#e9ecf1',
    selected = '#d5dbe5', border = '#9aa4b2', accent = '#0b6680', input = '#9a4a08',
    blue = '#1d4ed8', green = '#126c34', red = '#b91c1c', repo = '#7e22ce',
  },
}

function M.apply(theme)
  local p = palettes[theme or 'dark']
  if not p or applying then return end
  applying = true
  vim.o.termguicolors = true
  vim.o.background = p.mode
  local function set(group, fg, bg, extra)
    local attrs = { fg = fg, bg = bg }
    for key, value in pairs(extra or {}) do attrs[key] = value end
    vim.api.nvim_set_hl(0, group, attrs)
  end
  for _, group in ipairs({ 'Normal', 'NormalNC', 'MsgArea', 'SignColumn' }) do set(group, p.text, p.panel) end
  for _, group in ipairs({ 'EndOfBuffer', 'NonText', 'Whitespace', 'LineNr', 'FoldColumn' }) do set(group, p.muted, p.panel) end
  set('CursorLineNr', p.accent, p.panel)
  set('CursorLine', nil, p.bar)
  set('ColorColumn', nil, p.bar)
  set('Folded', p.muted, p.bar)
  set('StatusLine', p.text, p.bar)
  set('StatusLineNC', p.muted, p.bar)
  set('WinSeparator', p.border, p.panel)
  set('VertSplit', p.border, p.panel)
  for _, group in ipairs({ 'NormalFloat', 'Pmenu', 'TabLine', 'TabLineFill' }) do set(group, p.text, p.bar) end
  set('FloatBorder', p.border, p.bar)
  set('PmenuSel', p.accent, p.selected)
  set('TabLineSel', p.accent, p.selected)
  set('Visual', nil, p.selected)
  set('Search', p.panel, p.input)
  set('IncSearch', p.panel, p.accent)
  set('MatchParen', p.accent, p.selected)
  set('Comment', p.muted)
  set('Identifier', p.text)
  set('Operator', p.text)
  set('Delimiter', p.muted)
  set('Function', p.accent)
  for _, group in ipairs({ 'Type', 'StorageClass', 'Structure', 'Typedef' }) do set(group, p.blue) end
  set('Special', p.blue)
  for _, group in ipairs({ 'Statement', 'Conditional', 'Repeat', 'Label', 'Keyword', 'Exception',
    'PreProc', 'Include', 'Define', 'Macro', 'PreCondit' }) do set(group, p.repo) end
  set('String', p.green)
  set('Character', p.green)
  for _, group in ipairs({ 'Number', 'Float', 'Boolean', 'Constant' }) do set(group, p.input) end
  for name, role in pairs({ Error = 'red', Warn = 'input', Info = 'blue', Hint = 'muted', Ok = 'green' }) do
    set('Diagnostic' .. name, p[role])
    set('DiagnosticUnderline' .. name, nil, nil, { sp = p[role], undercurl = true })
  end
  for group, target in pairs({
    ['@keyword'] = 'Statement', ['@function'] = 'Function', ['@string'] = 'String',
    ['@number'] = 'Number', ['@boolean'] = 'Boolean', ['@type'] = 'Type',
    ['@variable'] = 'Identifier', ['@comment'] = 'Comment', ['@operator'] = 'Operator',
    ['@punctuation.delimiter'] = 'Delimiter',
  }) do vim.api.nvim_set_hl(0, group, { link = target }) end
  applying = false
end

function M.setup(opts)
  opts = opts or {}
  local theme = opts.theme or vim.env.ST_THEME or 'dark'
  local mode = opts.mode or vim.env.ST_PROGRAM_THEME or 'workspace'
  local group = vim.api.nvim_create_augroup('SupertreeProgramTheme', { clear = true })
  if mode == 'native' or not palettes[theme] then return end
  local function apply() M.apply(theme) end
  -- --cmd runs before the user's init. Apply after its initial colourscheme and
  -- reapply when a lazy-loaded colourscheme changes the highlights later.
  vim.api.nvim_create_autocmd('VimEnter', { group = group, callback = apply })
  vim.api.nvim_create_autocmd('ColorScheme', { group = group, callback = function() vim.schedule(apply) end })
  if vim.v.vim_did_enter == 1 then apply() end
end

return M
