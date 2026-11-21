# shellcheck shell=bash
# Native tmux formats keep workspace chrome independent of redraw subprocesses.

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
  local width compact text label small normal large tabs hint state need signature
  signature="$UI_REPO_T|$UI_TEXT_T|$UI_DIM_T|$UI_A_T|$UI_BAR_T|$UI_TAB_T|$UI_IN_T|$UI_RUN_T|$UI_INPUT_BG_T|$G_MORE|$G_DOWN|$G_IN|$G_RUN|${ST_KEY_PICKER:-M-w}"
  [ "${CHROME_SIGNATURE:-}" != "$signature" ] || return 0
  CHROME_SIGNATURE=$signature
  width='#{?client_width,#{client_width},#{window_width}}'
  compact="#{e|<:$width,100}"
  text=$(chrome_text '@supertree_label')
  small=$(chrome_middle "$text" 22)
  normal=$(chrome_middle "$text" 36)
  large=$(chrome_middle "$text" 52)
  label="#{?#{e|<:$width,60},$(chrome_middle "$text" 14),#{?${compact},$small,#{?#{e|<:$width,150},$normal,$large}}}"
  label="#{s|^([^/]*/)|#[fg=$UI_REPO_T]\\1#[fg=$UI_TEXT_T]|:$label}"
  CHROME_LEFT="#{?#{e|<:$width,40},, $label#[fg=$UI_DIM_T]#{?${compact}, , $G_DOWN  }#[default]}"

  # Display logical roles without renaming real tmux windows or their programs.
  text="#{?@st_tab_label,#{@st_tab_label},#{window_name}}"
  tabs="#{?${compact},#{?@st_slot,#{@st_slot},#{=12:$(chrome_text "$text")}},#{?@st_slot,#{@st_slot} ,}$(chrome_text "$text")}"
  CHROME_TAB="#[fg=$UI_DIM_T,bg=$UI_BAR_T] $tabs #[default]"
  CHROME_TAB_CURRENT="#[fg=$UI_A_T,bg=$UI_TAB_T] $tabs #[default]"

  hint="#[fg=$UI_DIM_T]#{?${compact},${ST_KEY_PICKER:-M-w},${ST_KEY_PICKER:-M-w} worktrees} "
  state="#{?#{==:#{@st_tree_state},input},#[fg=$UI_IN_T]$G_IN needs input  ,#{?#{==:#{@st_tree_state},running},#[fg=$UI_RUN_T]$G_RUN #[fg=$UI_DIM_T]agent active  ,}}"
  need=$(chrome_text '@st_need_text')
  CHROME_RIGHT="#{?@st_need_text,#[fg=$UI_IN_T]#[bg=$UI_INPUT_BG_T] #{?${compact},$G_IN,$need} #[default] ,#{?${compact},,$state}}$hint"

  # Keep the active tab visible when custom windows exceed the remaining width.
  # E: expands our formats without interpreting % in repository/window names.
  CHROME_STRIP='#[align=left range=left]#{E:status-left}#[norange default]#[list=on align=left]#[list=left-marker]<#[list=right-marker]>#[list=on]#{W:#[range=window|#{window_index}]#{E:window-status-format}#[norange default],#[range=window|#{window_index} list=focus]#{E:window-status-current-format}#[norange list=on default]}#[nolist align=right range=right]#{E:status-right}#[norange default]'
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
  tmux set-option -t "$target" status on || return 1

  # Remove only Supertree's old padding hooks. Width-dependent labels remain
  # native formats; resizing never needs to add blank rows or run a command.
  for hook in client-attached client-resized client-session-changed window-resized; do
    tmux set-hook -u -t "$target" "$hook[730]" || return 1
  done
}
