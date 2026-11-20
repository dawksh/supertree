-- Source-checkout entrypoint; release bundles ship the same bridge with st-lib.
local source = debug.getinfo(1, 'S').source:sub(2)
local root = vim.fn.fnamemodify(source, ':p:h:h')
return dofile(root .. '/bin/st-lib/editor.lua')
