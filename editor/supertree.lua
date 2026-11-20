-- Load explicitly with: dofile('/path/to/supertree/editor/supertree.lua').setup()
local M = {}

function M.setup(opts)
  opts = opts or {}
  local command = opts.command or { 'st', 'next' }
  vim.api.nvim_create_user_command('SupertreeContext', function(range)
    local path = vim.api.nvim_buf_get_name(0)
    if path == '' then
      vim.notify('Save this buffer before capturing context', vim.log.levels.WARN)
      return
    end
    if vim.bo.modified then
      vim.notify('Save this buffer first; context captures the file on disk', vim.log.levels.WARN)
      return
    end
    local args = vim.deepcopy(command)
    vim.list_extend(args, {
      'context', 'add', '--file', path,
      '--start-line', tostring(range.line1), '--end-line', tostring(range.line2),
    })
    local errors = {}
    vim.fn.jobstart(args, {
      cwd = vim.fn.fnamemodify(path, ':h'),
      stdout_buffered = true,
      stderr_buffered = true,
      on_stdout = function(_, lines)
        local ok, item = pcall(vim.json.decode, table.concat(lines, '\n'))
        if ok and item and item.id then
          vim.schedule(function()
            vim.notify('Captured ' .. item.id .. '; select it in Supertree before sending')
          end)
        end
      end,
      on_stderr = function(_, lines) errors = lines end,
      on_exit = function(_, code)
        if code ~= 0 then
          vim.schedule(function()
            vim.notify(table.concat(errors, '\n'), vim.log.levels.ERROR)
          end)
        end
      end,
    })
  end, { range = true, force = true, desc = 'Capture selected file lines into shared Supertree context' })
end

return M
