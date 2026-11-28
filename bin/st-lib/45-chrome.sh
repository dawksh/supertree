# shellcheck shell=bash
# Native tmux formats keep workspace chrome independent of redraw subprocesses;
# the one exception is the bottom bar's _bar-info, which tmux runs every 5s.

# tmux expands a substitution's replacement twice. Eight hashes produce the
# doubled hash its status renderer needs to display untrusted text literally.
# Do not use T: on these values: branch names may contain strftime directives.
chrome_text() { printf '#{s|#|########|:#{s|[\001-\037\177]| |:%s}}' "$1"; }

chrome_middle() { # format, terminal cells
  local text=$1 width=$2 left right
  left=$((width / 2))
  right=$((width - 1 - left))
  printf '#{?#{==:%s,#{=%s:%s}},%s,#{=%s:%s}%s#{=-%s:%s}}' \
    "$text" "$width" "$text" "$text" "$left" "$text" "$G_MORE" "$right" "$text"
}

chrome_formats() {
  local width compact text tabs signature self
  self=$(st_self)
  signature="$UI_REPO_T|$UI_TEXT_T|$UI_DIM_T|$UI_A_T|$UI_BAR_T|$UI_TAB_T|$UI_IN_T|$UI_RUN_T|$UI_INPUT_BG_T|$G_MORE|$G_DOWN|$G_IN|$G_RUN|${ST_KEY_PICKER:-M-w}|${ST_KEY_HELP:-M-/}|$self"
  [ "${CHROME_SIGNATURE:-}" != "$signature" ] || return 0
  CHROME_SIGNATURE=$signature
  # The top strip holds tabs only; everything else is in each pane's bottom border.
  CHROME_LEFT=''
  CHROME_RIGHT=''
  width='#{?client_width,#{client_width},#{window_width}}'
  compact="#{e|<:$width,100}"

  # Display logical roles without renaming real tmux windows or their programs.
  text="#{?@st_tab_label,#{@st_tab_label},#{window_name}}"
  tabs="#{?${compact},#{?@st_slot,#{@st_slot},#{=12:$(chrome_text "$text")}},#{?@st_slot,#{@st_slot} ,}$(chrome_text "$text")}"
  CHROME_TAB="#[fg=$UI_DIM_T,bg=$UI_BAR_T] $tabs #[default]"
  CHROME_TAB_CURRENT="#[fg=$UI_A_T,bg=$UI_TAB_T] $tabs #[default]"

  chrome_bottom "$self"

  # Keep the active tab visible when custom windows exceed the remaining width.
  # E: expands our formats without interpreting % in repository/window names.
  CHROME_STRIP='#[align=left range=left]#{E:status-left}#[norange default]#[list=on align=left]#[list=left-marker]<#[list=right-marker]>#[list=on]#{W:#[range=window|#{window_index}]#{E:window-status-format}#[norange default],#[range=window|#{window_index} list=focus]#{E:window-status-current-format}#[norange list=on default]}#[nolist align=right range=right]#{E:status-right}#[norange default]'
}

# Pane borders are expanded one time fewer than the status strip, so hash
# doubling does not apply. q/e doubles #s at draw time (tmux 3.2+, CHANGES).
chrome_border_text() { printf '#{s|[\001-\037\177]| |;q/e:%s}' "$1"; }

# Truncation runs before q/e on the raw value, so a cut never splits a ##.
chrome_border_label() { # terminal cells
  local width=$1 left right text
  left=$((width / 2))
  right=$((width - 1 - left))
  text=$(printf 's|[\001-\037\177]| |;q/e:@supertree_label')
  printf '#{?#{==:#{=%s:@supertree_label},#{@supertree_label}},#{%s},#{=%s;%s}%s#{=-%s;%s}}' \
    "$width" "$text" "$left" "$text" "$G_MORE" "$right" "$text"
}

# Drawn under every pane, so sizes follow the pane. Inactive panes are dimmed.
chrome_bottom() {
  local self=$1 width compact fg plain label info state need mode last hint left right
  width='#{pane_width}'
  compact="#{e|<:$width,100}"
  fg="#{?pane_active,$UI_TEXT_T,$UI_DIM_T}"
  plain="#{?#{e|<:$width,60},$(chrome_border_label 14),#{?${compact},$(chrome_border_label 22),#{?#{e|<:$width,150},$(chrome_border_label 36),$(chrome_border_label 52)}}}"
  label="#{?pane_active,#{s|^([^/]*/)|#[fg=$UI_REPO_T]\\1#[fg=$UI_TEXT_T]|:$plain},#[fg=$UI_DIM_T]$plain}"
  # #() runs asynchronously and tmux reuses the last result until it finishes.
  info="#[fg=$UI_DIM_T]#($(tmux_cmd_word "$self") _bar-info #{q:pane_current_path} #{q:session_name})"
  state="#{?#{==:#{@st_tree_state},input},#[fg=#{?pane_active,$UI_IN_T,$UI_DIM_T}]$G_IN needs input ,#{?#{==:#{@st_tree_state},running},#[fg=#{?pane_active,$UI_RUN_T,$UI_DIM_T}]$G_RUN #[fg=$UI_DIM_T]agent active ,}}"
  need=$(chrome_border_text '@st_need_text')
  state="#{?@st_need_text,#[fg=#{?pane_active,$UI_IN_T,$UI_DIM_T}]#[bg=#{?pane_active,$UI_INPUT_BG_T,default}] #{?${compact},$G_IN,$need} #[default] ,#{?${compact},,$state}}"
  mode="#{?client_prefix,#[fg=#{?pane_active,$UI_IN_T,$UI_DIM_T}]PREFIX ,#{?#{!=:#{client_key_table},root},#[fg=#{?pane_active,$UI_IN_T,$UI_DIM_T}]#{client_key_table} ,}}"
  last="#{?@st_last_key,#[fg=#{?pane_active,$UI_A_T,$UI_DIM_T}]$G_INFO $(chrome_border_text '@st_last_key') ,}"
  hint="#[fg=$UI_DIM_T]#{?${compact},${ST_KEY_PICKER:-M-w} ${ST_KEY_HELP:-M-/},${ST_KEY_PICKER:-M-w} worktrees  ${ST_KEY_HELP:-M-/} keys}"
  left="#[fg=$fg] $label  $info  $state"
  right="$mode$last$hint "
  CHROME_BOTTOM="#{?@st_sidebar,,#[align=left]$left#[align=right]$right#[default]}"
}

style_chrome() {
  local target="=$1:" hook
  chrome_formats
  tmux set-option -t "$target" status-left-length 200 &&
  tmux set-option -t "$target" status-right-length 100 &&
  tmux set-option -t "$target" status-left "$CHROME_LEFT" &&
  tmux set-option -t "$target" status-right "$CHROME_RIGHT" &&
  tmux set-option -t "$target" 'status-format[0]' "$CHROME_STRIP" &&
  tmux set-option -ut "$target" 'status-format[1]' &&
  tmux set-option -ut "$target" 'status-format[2]' &&
  tmux set-option -t "$target" status-interval 5 &&
  tmux set-option -t "$target" status on || return 1

  # Remove only Supertree's old padding hooks. Width-dependent labels remain
  # native formats; resizing never needs to add blank rows or run a command.
  for hook in client-attached client-resized client-session-changed window-resized; do
    tmux set-hook -u -t "$target" "$hook[730]" || return 1
  done
}

# Printed into every pane's bottom bar by #(); must stay fast and never fail loudly.
cmd_bar_info() {
  local dir=${1:-} sess=${2:-} branch dirty='' out='' binary count
  [ -d "$dir" ] || return 0
  if branch=$(git -C "$dir" symbolic-ref --short -q HEAD 2>/dev/null || git -C "$dir" rev-parse --short HEAD 2>/dev/null); then
    # Untracked files are skipped: scanning them every refresh is slow in large repos.
    [ -z "$(git -C "$dir" status --porcelain --untracked-files=no 2>/dev/null | head -n 1)" ] || dirty=$G_DIRTY
    out="$branch$dirty"
  fi
  binary=$st_bin_dir/st-next
  [ "$ST_VERSION" = dev ] || binary=$st_lib_dir/st-next
  if [ -x "$binary" ]; then
    export ST_STATE ST_WORKTREE_ROOT
    if "$binary" daemon status --json 2>/dev/null | grep -q '"running": *true'; then
      count=$(tmux list-panes -s -t "=$sess" -F '#{@st_next_process}' 2>/dev/null | grep -c . || true)
      out="$out  core on · ${count:-0} procs"
    else
      out="$out  core off"
    fi
  fi
  # #() output is drawn as a format; double # so branch names stay literal.
  printf '%s' "${out//#/##}"
}

st_register_command '_bar-info' cmd_bar_info ''
