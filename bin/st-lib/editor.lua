local M = {}

function M.setup(opts)
  opts = opts or {}
  local command = opts.command or { 'st', 'next' }
  local function run(args, cwd, sharing)
    local argv, errors = vim.deepcopy(command), {}
    vim.list_extend(argv, args)
    local job = vim.fn.jobstart(argv, {
      cwd = cwd, stdout_buffered = true, stderr_buffered = true,
      on_stdout = function(_, lines)
        local ok, result = pcall(vim.json.decode, table.concat(lines, '\n'))
        if not ok or not result then return end
        vim.schedule(function()
          if not sharing and result.id then
            vim.notify('Captured ' .. result.id .. '; select it before sending')
          elseif sharing and result.status == 'sent' and result.delivery then
            vim.notify('Shared with this worktree\'s agent · ' .. result.delivery.id)
          elseif sharing and result.status == 'requested' and result.question then
            vim.notify('Saved ' .. result.question.id .. '; ask your agent: Check my Supertree questions')
          elseif sharing and result.status == 'preview' then
            vim.cmd('botright new')
            vim.bo.buftype, vim.bo.bufhidden, vim.bo.swapfile = 'nofile', 'wipe', false
            vim.api.nvim_buf_set_lines(0, 0, -1, false, vim.split(result.text .. '\n\nContext IDs: ' .. table.concat(result.context_ids, ', '), '\n', { plain = true }))
            vim.bo.modified, vim.bo.modifiable = false, false
          elseif sharing then
            vim.notify('Delivery is ' .. tostring(result.status) .. '; inspect its receipt before retrying', vim.log.levels.WARN)
          end
        end)
      end,
      on_stderr = function(_, lines)
        for _, line in ipairs(lines) do if line ~= '' then table.insert(errors, line) end end
      end,
      on_exit = function(_, code)
        if code ~= 0 then vim.schedule(function() vim.notify(table.concat(errors, '\n'), vim.log.levels.ERROR) end) end
      end,
    })
    if job <= 0 then vim.notify('Could not start the Supertree command', vim.log.levels.ERROR) end
  end

  local function action(range, mode)
    local buffer = vim.api.nvim_get_current_buf()
    local function submit(message)
      if not vim.api.nvim_buf_is_valid(buffer) then return end
      local path = vim.api.nvim_buf_get_name(buffer)
      if mode ~= 'diff' and path == '' then
        vim.notify('Save this buffer before capturing context', vim.log.levels.WARN); return
      end
      if vim.bo[buffer].modified then
        vim.notify('Save this buffer first; context captures files on disk', vim.log.levels.WARN); return
      end
      local args = mode == 'capture' and { 'context', 'add' } or { 'share', '--json' }
      if mode == 'diff' then vim.list_extend(args, { '--diff' })
      else vim.list_extend(args, { '--file', path, '--start-line', tostring(range.line1), '--end-line', tostring(range.line2) }) end
      if mode == 'preview' then table.insert(args, '--preview') end
      if message and message:match('%S') then vim.list_extend(args, { '--message', message })
      elseif mode == 'ask' or mode == 'diff' then return end
      run(args, path ~= '' and vim.fn.fnamemodify(path, ':h') or vim.fn.getcwd(), mode ~= 'capture')
    end
    if mode == 'capture' or mode == 'preview' or range.args:match('%S') then submit(range.args)
    else vim.ui.input({ prompt = 'Ask this worktree\'s agent: ' }, function(message) if message then submit(message) end end) end
  end
  vim.api.nvim_create_user_command('SupertreeContext', function(range) action(range, 'capture') end,
    { range = true, force = true, desc = 'Capture saved file lines without sending' })
  vim.api.nvim_create_user_command('SupertreeAsk', function(range) action(range, 'ask') end,
    { range = true, nargs = '*', force = true, desc = 'Share selected saved lines and a question with this worktree agent' })
  vim.api.nvim_create_user_command('SupertreePreview', function(range) action(range, 'preview') end,
    { range = true, nargs = '*', force = true, desc = 'Preview captured file lines without sending' })
  vim.api.nvim_create_user_command('SupertreeShareDiff', function(range) action(range, 'diff') end,
    { nargs = '*', force = true, desc = 'Share the tracked diff and a question with this worktree agent' })
end

return M
