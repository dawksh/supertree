# shellcheck shell=bash
# supertree tmux module

ST_LAYOUT_VERSION=7

layout_signature() {
  printf '%s|%s|%s' "$ST_HARNESS" "$ST_WINDOWS" "${ST_SHELL:-${SHELL:-/bin/zsh}}"
}

tree_shell() {
  local shell=${ST_SHELL:-${SHELL:-/bin/zsh}}
  [ -x "$shell" ] || die "shell is not executable: $shell"
  printf '%s' "$shell"
}

shell_command() {
  printf 'env -u NO_COLOR HOME=%q SHELL=%q %q -l' "$HOME" "$(tree_shell)" "$(tree_shell)"
}

# tmux starts panes from its server environment, which can differ from the
# command that created the session. Pin the resolved choices on this session.
session_environment() {
  local target="=$1" signature
  signature="$HOME|$(tree_shell)|$ST_CONFIG|$ST_STATE|$ST_WORKTREE_ROOT|$ST_HARNESS|$ST_WINDOWS|$ST_HARNESS_COMMAND|$ST_HARNESS_RESUME_COMMAND|${ST_SHELL:-}|color"
  [ "$(tmux show-options -qv -t "$target:" @st_environment 2>/dev/null)" != "$signature" ] || return 0
  tmux set-environment -t "$target" HOME "$HOME"
  tmux set-environment -t "$target" SHELL "$(tree_shell)"
  tmux set-environment -t "$target" ST_CONFIG "$ST_CONFIG"
  tmux set-environment -t "$target" ST_STATE "$ST_STATE"
  tmux set-environment -t "$target" ST_WORKTREE_ROOT "$ST_WORKTREE_ROOT"
  tmux set-environment -t "$target" ST_HARNESS "$ST_HARNESS"
  tmux set-environment -t "$target" ST_WINDOWS "$ST_WINDOWS"
  tmux set-environment -t "$target" ST_HARNESS_COMMAND "$ST_HARNESS_COMMAND"
  tmux set-environment -t "$target" ST_HARNESS_RESUME_COMMAND "$ST_HARNESS_RESUME_COMMAND"
  tmux set-environment -t "$target" ST_SHELL "${ST_SHELL:-}"
  tmux set-environment -t "$target" -r NO_COLOR
  tmux set-option -t "$target:" @st_environment "$signature"
}

tab_command() {
  local var="ST_TAB_$1"
  printf '%s' "${!var:-}"
}

window_name() {
  # In UTF-8 locales A-Z can match accented letters, which are not valid in ST_TAB_<name>.
  local LC_ALL=C
  case $1 in
    agent) harness_window;;
    vim) printf 'nvim';;
    shell) printf 'shell';;
    *[!A-Za-z0-9_]*) die "invalid window type '$1' (letters, digits and _ only)";;
    *) [ -n "$(tab_command "$1")" ] ||
         die "unknown window type '$1' (choose from: agent vim shell, or set ST_TAB_$1)"
       printf '%s' "$1";;
  esac
}

configured_windows() {
  local type name seen_types=' ' seen_names=' '
  [ -n "$ST_WINDOWS" ] || die "ST_WINDOWS must contain at least one of: agent vim shell"
  for type in $ST_WINDOWS; do
    case $seen_types in
      *" $type "*) die "duplicate window type '$type' in ST_WINDOWS";;
    esac
    name=$(window_name "$type")
    case $seen_names in
      *" $name "*) die "configured windows resolve to duplicate tmux name '$name'";;
    esac
    seen_types="$seen_types$type "
    seen_names="$seen_names$name "
    printf '%s\n' "$type"
  done
}

window_enabled() {
  local wanted=$1 windows=${2:-} type
  [ -n "$windows" ] || windows=$(configured_windows)
  while read -r type; do [ "$type" = "$wanted" ] && return 0; done <<EOF
$windows
EOF
  return 1
}

st_self() { command -v st 2>/dev/null || printf '%s' "$HOME/.local/bin/st"; }

ST_HOME_SESSION=supertree

home_is_open() {
  [ "$(tmux show-options -qv -t "=$ST_HOME_SESSION:" @supertree_home 2>/dev/null || true)" = 1 ]
}

cmd_home() {
  local picker=0 main pane
  case ${1:-} in
    '') [ $# -eq 0 ] || die "usage: st home";;
    --picker) [ $# -eq 1 ] || die "usage: st home"; picker=1;;
    *) die "usage: st home";;
  esac
  main=$(main_worktree .) || main=''
  [ -z "$main" ] || register_repo "$main"
  if tmux has-session -t "=$ST_HOME_SESSION" 2>/dev/null; then
    home_is_open || die "tmux session '$ST_HOME_SESSION' already exists outside Supertree"
    session_environment "$ST_HOME_SESSION"
    if [ "$(tmux show-options -qv -t "=$ST_HOME_SESSION:" @st_layout 2>/dev/null)" != "$ST_LAYOUT_VERSION" ]; then
      style_session "$ST_HOME_SESSION"
      style_tab "=$ST_HOME_SESSION:shell" ''
      if declare -F sidebar_session >/dev/null; then sidebar_session "$ST_HOME_SESSION" "$PWD"; fi
      tmux set-option -t "=$ST_HOME_SESSION:" @st_layout "$ST_LAYOUT_VERSION"
    fi
  else
    tmux new-session -d -s "$ST_HOME_SESSION" -c "$PWD" -n shell "$(shell_command)" ||
      die "could not create Supertree home session"
    session_environment "$ST_HOME_SESSION"
    # Mark it as ours only after setup succeeds. On failure, remove the new
    # session so the next `st` can retry instead of seeing a broken home.
    if ! {
      tmux set-option -t "=$ST_HOME_SESSION:" @supertree_label 'Supertree' &&
      style_session "$ST_HOME_SESSION" &&
      style_tab "=$ST_HOME_SESSION:shell" '' &&
      { ! declare -F sidebar_session >/dev/null || sidebar_session "$ST_HOME_SESSION" "$PWD"; } &&
      tmux set-option -t "=$ST_HOME_SESSION:" @supertree_home 1 &&
      tmux set-option -t "=$ST_HOME_SESSION:" @st_layout "$ST_LAYOUT_VERSION"
    }; then
      tmux kill-session -t "=$ST_HOME_SESSION" >/dev/null 2>&1 || true
      die "could not initialize Supertree home session"
    fi
  fi
  if declare -F sidebar_poll_start >/dev/null; then sidebar_poll_start; fi
  if [ "$picker" = 1 ] && declare -F window_sidebar >/dev/null; then
    pane=$(window_sidebar "=$ST_HOME_SESSION:shell")
    [ -z "$pane" ] || tmux select-pane -t "$pane"
  fi
  if [ -n "${TMUX:-}" ]; then
    tmux switch-client -t "=$ST_HOME_SESSION"
  else
    tmux attach-session -t "=$ST_HOME_SESSION"
  fi
}

# run-shell and hook commands expand #{} formats before the shell runs them, so # is doubled.
# "dq": the word also sits inside tmux double quotes, where \ " and $ are special.
tmux_cmd_word() {
  local q="'" escaped_q="'\\''" word
  word="'${1//$q/$escaped_q}'"
  if [ "${2:-}" = dq ]; then
    word=${word//\\/\\\\}
    word=${word//\"/\\\"}
    word=${word//\$/\\\$}
  fi
  printf '%s' "${word//#/##}"
}

is_tree_session() {
  [ "$1" != "$ST_HOME_SESSION" ] &&
    [ -n "$(tmux show-options -qv -t "=$1:" @supertree_label 2>/dev/null)" ]
}

create_window() {
  local sess=$1 dir=$2 type=$3 initial=$4 self name run
  local cmd=()
  self=$(st_self)
  name=$(window_name "$type")
  # The first pane starts before session_environment can remove NO_COLOR from
  # tmux's server environment. Keep the user's UI preference out of programs.
  printf -v run 'env -u NO_COLOR HOME=%q SHELL=%q ST_CONFIG=%q ST_STATE=%q ST_WORKTREE_ROOT=%q ST_HARNESS=%q ST_WINDOWS=%q ST_HARNESS_COMMAND=%q ST_HARNESS_RESUME_COMMAND=%q ST_SHELL=%q %q _run' \
    "$HOME" "$(tree_shell)" "$ST_CONFIG" "$ST_STATE" "$ST_WORKTREE_ROOT" \
    "$ST_HARNESS" "$ST_WINDOWS" "$ST_HARNESS_COMMAND" "$ST_HARNESS_RESUME_COMMAND" "${ST_SHELL:-}" "$self"
  case $type in
    agent) cmd=("$run _agent");;
    vim) cmd=("$run nvim");;
    shell) cmd=("$(shell_command)");;
    # Argument form skips tmux's own `sh -c`, so the configured command is parsed once.
    *) cmd=(env -u NO_COLOR "$self" _run /bin/sh -c "$(tab_command "$type")");;
  esac
  if [ "$initial" = 1 ]; then
    tmux new-session -d -s "$sess" -c "$dir" -n "$name" ${cmd[@]+"${cmd[@]}"} || return 1
  else
    tmux new-window -d -t "=$sess" -c "$dir" -n "$name" ${cmd[@]+"${cmd[@]}"} || return 1
  fi
  style_tab "=$sess:$name" "$(configured_windows | grep -nxF -- "$type" | cut -d: -f1)"
}

# Tab look is set per session and window so other tmux sessions keep the user's status bar.
style_session() {
  local width=$(( ${ST_SIDEBAR_WIDTH:-24} + 1 )) label_width left_half right_half left
  label_width=$((width - 4))
  left_half=$(((label_width - 1) / 2))
  right_half=$((label_width - 1 - left_half))
  # Keep the tab strip aligned with the configured sidebar width. A long name
  # keeps both ends so its repo and branch suffix stay identifiable.
  printf -v left ' #{?@supertree_home,#{p%d:@supertree_label},#{?@st_state_glyph,#{@st_state_glyph},%s} #{p%d:#{?#{==:#{@supertree_label},#{=%d:@supertree_label}},#{@supertree_label},#{=%d:@supertree_label}…#{=-%d:@supertree_label}}}} ' \
    "$((width - 2))" "${G_OFF:-·}" "$label_width" "$label_width" "$left_half" "$right_half"
  tmux set-option -t "=$1:" default-shell "$(tree_shell)" &&
  tmux set-option -t "=$1:" status-position top &&
  tmux set-option -t "=$1:" status-justify left &&
  tmux set-option -t "=$1:" status-style 'fg=default,bg=default' &&
  tmux set-option -t "=$1:" status-left-length "$width" &&
  tmux set-option -t "=$1:" status-left "$left" &&
  tmux set-option -t "=$1:" status-right-length 80 &&
  # @st_need_text (global, opt-in) and @st_state_glyph (per tree) come from the sidebar refresher.
  tmux set-option -t "=$1:" status-right '#{?@st_need_text,#[fg=yellow]#{@st_need_text}#[default]  ,}' &&
  tmux set-option -t "=$1:" status on &&
  # When a tree's last program exits, switch to the most recent session instead of
  # leaving tmux. Session-scoped, so the global setting is untouched.
  tmux set-option -t "=$1:" detach-on-destroy off
}

style_tab() {
  local target=$1 slot=$2 label
  # @st_tab_state comes from the sidebar refresher (running/input); without it, fall back to the pane state.
  label='#{?@st_slot,#{@st_slot}. ,}#W'
  label="$label"'#{?@st_tab_state,#{?#{==:#{@st_tab_state},input}, ◐,#{?#{==:#{@st_tab_state},running}, ●,}},#{?#{==:#{@st_agent_state},running}, ●,}}'
  tmux set-option -w -t "$target" @st_slot "$slot" &&
  tmux set-option -w -t "$target" window-status-separator '  ' &&
  tmux set-option -w -t "$target" window-status-format "#[dim] $label #[default]" &&
  tmux set-option -w -t "$target" window-status-current-format "#[bold,underscore] $label #[default]" &&
  tmux set-option -w -t "$target" window-status-current-style default &&
  style_panes "$target"
}

# Keep program panes on the user's terminal background. Only split panes need a
# divider; a single program gets the full height without an empty title row.
style_panes() {
  local target=$1
  tmux set-option -w -t "$target" pane-border-status off
  tmux set-option -w -t "$target" pane-border-style 'fg=default,bg=default,dim'
  tmux set-option -w -t "$target" pane-active-border-style 'fg=default,bg=default'
  tmux set-option -w -t "$target" window-style 'bg=default'
  tmux set-option -w -t "$target" window-active-style 'bg=default'
  tmux set-option -w -t "$target" pane-scrollbars off 2>/dev/null || true
}

cmd_split() {
  local pane win dir self tab orientation=${1:---horizontal} split_flag
  local cmd=()
  [ $# -le 1 ] || die "usage: st split [--horizontal|--vertical]"
  case $orientation in
    --horizontal) split_flag=-h;;
    --vertical) split_flag=-v;;
    *) die "usage: st split [--horizontal|--vertical]";;
  esac
  self=$(st_self)
  pane=${TMUX_PANE:-$(tmux display-message -p '#{pane_id}')}
  # Only redirect a sidebar split to a program pane. Keep the focused pane
  # when there are already several program panes in the window.
  if [ "$(tmux display-message -p -t "$pane" '#{@st_sidebar}')" = 1 ]; then
    pane=$(tmux list-panes -t "$pane" -f '#{!=:#{@st_sidebar},1}' -F '#{pane_id}' | tail -1)
  fi
  [ -n "$pane" ] || die "no program pane to split"
  local sess
  sess=$(tmux display-message -p -t "$pane" '#S')
  is_tree_session "$sess" || { ui_toast "not a supertree session"; return 0; }
  session_environment "$sess"
  win=$(tmux display-message -p -t "$pane" '#W')
  dir=$(tmux display-message -p -t "$pane" '#{pane_current_path}')
  case $win in
    "$(harness_window)")
      # A fresh session, not the resume command: two panes continuing one conversation would collide.
      case $ST_HARNESS in
        codex) cmd=("$self" _run codex --no-daemon);;
        claude|openrouter) cmd=("$self" _run "$(harness_bin)");;
        *) cmd=("$self" _run /bin/sh -c "$ST_HARNESS_COMMAND");;
      esac;;
    nvim) cmd=("$self" _run nvim);;
    shell) ;;
    *) tab=$(tab_command "$win" 2>/dev/null || true)
       [ -z "$tab" ] || cmd=("$self" _run /bin/sh -c "$tab");;
  esac
  if declare -F sidebar_poll_start >/dev/null; then sidebar_poll_start; fi
  # Pin the sidebar first so the split halves the program pane evenly.
  if declare -F sidebar_fit >/dev/null; then sidebar_fit "$(tmux display-message -p -t "$pane" '#S')"; fi
  tmux split-window "$split_flag" -t "$pane" -c "$dir" ${cmd[@]+"${cmd[@]}"}
}

build_session() {
  local sess=$1 dir=$2 windows type first_name='' initial=1 branch main label signature
  signature=$(layout_signature)
  # A current Supertree session already has its label, windows, and sidebar.
  # Switching to it should not inspect the Git checkout again.
  if tmux has-session -t "=$sess" 2>/dev/null &&
     [ "$(tmux show-options -qv -t "=$sess:" @st_layout 2>/dev/null)" = "$ST_LAYOUT_VERSION" ] &&
     [ "$(tmux show-options -qv -t "=$sess:" @st_config 2>/dev/null)" = "$signature" ]; then
    session_environment "$sess"
    return 0
  fi
  branch=$(tree_branch "$dir")
  main=$(main_worktree "$dir") || main=''
  label="$(basename "$main")/${branch:-(detached)}"
  [ -z "$main" ] || adopt_hashed_session "$(repo_key "$main")" "${branch:-(detached)}" "$sess"
  if tmux has-session -t "=$sess" 2>/dev/null; then
    session_environment "$sess"
    tmux set-option -t "=$sess:" @supertree_label "$label"
    # Bump ST_LAYOUT_VERSION when the look changes so open sessions restyle on the next switch.
    # "=name:" not "=name": tmux 3.6 rejects "=name" as a set/show-options target.
    if [ "$(tmux show-options -qv -t "=$sess:" @st_layout)" != "$ST_LAYOUT_VERSION" ] ||
       [ "$(tmux show-options -qv -t "=$sess:" @st_config)" != "$signature" ]; then
      upgrade_session "$sess" "$dir"
    fi
    return 0
  fi
  windows=$(configured_windows)
  while read -r type; do
    [ -n "$type" ] || continue
    [ -n "$first_name" ] || first_name=$(window_name "$type")
    if ! create_window "$sess" "$dir" "$type" "$initial"; then
      # A concurrent `st go` on the same tree created the session first.
      [ "$initial" = 1 ] && tmux has-session -t "=$sess" 2>/dev/null || return 1
      tmux set-option -t "=$sess:" @supertree_label "$label"
      return 0
    fi
    [ "$initial" != 1 ] || session_environment "$sess"
    if [ "$type" = agent ]; then
      tmux set-option -t "=$sess:" @st_agent_window "$(window_name agent)"
    fi
    initial=0
  done <<EOF
$windows
EOF
  style_session "$sess"
  if declare -F sidebar_session >/dev/null; then sidebar_session "$sess" "$dir"; fi
  tmux set-option -t "=$sess:" @st_layout "$ST_LAYOUT_VERSION"
  tmux set-option -t "=$sess:" @st_config "$signature"
  tmux set-option -t "=$sess:" @supertree_label "$label"
  tmux select-window -t "=$sess:$first_name"
}

# Sessions opened before tabs and sidebars get them in place, without restarting their programs.
upgrade_session() {
  local sess=$1 dir=$2 win name type slot desired created_agent=0
  session_environment "$sess"
  style_session "$sess"
  # The logical window type is still "vim" in user configs, but the program
  # and tab are Neovim. Rename an existing tab without restarting its editor.
  if tmux list-windows -t "=$sess" -F '#W' | grep -qxF vim &&
     ! tmux list-windows -t "=$sess" -F '#W' | grep -qxF nvim; then
    tmux rename-window -t "=$sess:vim" nvim
  fi
  tmux list-windows -t "=$sess" -F '#{window_id} #{window_name}' | while read -r win name; do
    slot=''
    local index=0
    while read -r type; do
      index=$((index + 1))
      if [ "$(window_name "$type")" = "$name" ]; then slot=$index; break; fi
    done <<EOF
$(configured_windows)
EOF
    style_tab "$win" "$slot"
  done
  # A session can predate the current window layout or harness choice. Add only
  # missing configured windows so running programs and extra user tabs survive.
  while read -r type; do
    [ -n "$type" ] || continue
    desired=$(window_name "$type")
    if ! tmux list-windows -t "=$sess" -F '#W' | grep -qxF -- "$desired"; then
      create_window "$sess" "$dir" "$type" 0
      [ "$type" != agent ] || created_agent=1
    fi
    if [ "$type" = agent ]; then
      tmux set-option -t "=$sess:" @st_agent_window "$desired"
    fi
  done <<EOF
$(configured_windows)
EOF
  if declare -F sidebar_session >/dev/null; then sidebar_session "$sess" "$dir"; fi
  tmux set-option -t "=$sess:" @st_layout "$ST_LAYOUT_VERSION"
  tmux set-option -t "=$sess:" @st_config "$(layout_signature)"
  if [ "$created_agent" = 1 ]; then tmux select-window -t "=$sess:$(harness_window)"; fi
}

rewrite_recent() {
  local sess=$1 put_first=$2 tmp
  mkdir -p "$ST_STATE"
  tmp=$(mktemp "$ST_STATE/.recent.XXXXXX")
  {
    [ "$put_first" = 0 ] || printf '%s\n' "$sess"
    if [ -f "$ST_STATE/recent" ]; then
      grep -vxF -- "$sess" "$ST_STATE/recent" || true
    fi
  } > "$tmp"
  mv "$tmp" "$ST_STATE/recent"
}

remember_recent() { rewrite_recent "$1" 1; }

sort_recent() {
  local recent="$ST_STATE/recent"
  [ -f "$recent" ] || recent=/dev/null
  awk -F '\t' 'FILENAME == ARGV[1] { rank[$0] = ++n; next }
    { key = $3; print (key in rank ? rank[key] : 100000 + FNR) "\t" $0 }' \
    "$recent" - | sort -n -k1,1 | cut -f2-
}

tty_key() { printf '%s' "$1" | tr '/' '-'; }

current_session() {
  tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#S' 2>/dev/null || true
}

remember_origin() {
  local tty cur
  tty=$(tmux display-message -p ${TMUX_PANE:+-t "$TMUX_PANE"} '#{client_tty}' 2>/dev/null) || return 0
  cur=$(current_session)
  [ -n "$tty" ] && [ -n "$cur" ] && [ "$cur" != "$1" ] || return 0
  if home_is_open; then cur=$ST_HOME_SESSION; fi
  # Hops between trees keep home as their return point.
  ! is_tree_session "$cur" || return 0
  mkdir -p "$ST_STATE/origin"
  printf '%s\n' "$cur" > "$ST_STATE/origin/$(tty_key "$tty")"
}

evacuate_clients() {
  local sess=$1 ttys tty origin origin_file other
  ttys=$(tmux list-clients -t "=$sess" -F '#{client_tty}' 2>/dev/null) || return 0
  [ -n "$ttys" ] || return 0
  while read -r tty; do
    [ -n "$tty" ] || continue
    origin_file="$ST_STATE/origin/$(tty_key "$tty")"
    origin=$(cat "$origin_file" 2>/dev/null) || origin=""
    if [ -n "$origin" ] && [ "$origin" != "$sess" ] && tmux has-session -t "=$origin" 2>/dev/null; then
      tmux switch-client -c "$tty" -t "=$origin" 2>/dev/null && { rm -f "$origin_file"; continue; }
    fi
    # fall back to the most recently used other session
    other=$(tmux list-sessions -F '#{session_last_attached}|#{session_name}' 2>/dev/null |
      sort -rn | cut -d'|' -f2- | grep -vxF -- "$sess" | head -1) || other=""
    [ -n "$other" ] && tmux switch-client -c "$tty" -t "=$other" 2>/dev/null || true
  done <<EOF
$ttys
EOF
}

attach() {
  local sess=$1 prev
  remember_recent "$sess"
  if declare -F focus_content >/dev/null; then focus_content "$sess"; fi
  # The refresher only starts with a sidebar pane; restart it here in case it died.
  if declare -F sidebar_poll_start >/dev/null; then sidebar_poll_start; fi
  if [ -n "${TMUX:-}" ]; then
    prev=$(current_session)
    remember_origin "$sess"
    tmux switch-client -t "=$sess"
    if declare -F sidebar_fit >/dev/null; then sidebar_fit "$sess"; fi
    if declare -F sidebar_recenter >/dev/null; then sidebar_recenter "$sess" "$prev"; fi
  else
    tmux attach-session -t "=$sess"
  fi
}

known_sessions() {
  list_trees | cut -f4
}

subtree_sessions() {
  local repos repo branch path sess
  repos=$(main_checkouts)
  while IFS=$'\t' read -r repo branch path sess; do
    [ -n "$path" ] || continue
    case $'\n'"$repos"$'\n' in *$'\n'"$path"$'\n'*) continue;; esac
    printf '%s\n' "$sess"
  done
}

live_only() {
  local s
  while read -r s; do
    [ -n "$s" ] || continue
    if tmux has-session -t "=$s" 2>/dev/null; then printf '%s\n' "$s"; fi
  done
}

ensure_main_fallbacks() {
  local trees=$1 repos repo branch path subtree main main_row main_branch main_sess
  repos=$(main_checkouts)
  while IFS=$'\t' read -r repo branch path subtree; do
    [ -n "$path" ] || continue
    case $'\n'"$repos"$'\n' in *$'\n'"$path"$'\n'*) continue;; esac
    tmux has-session -t "=$subtree" 2>/dev/null || continue
    main_row=$(printf '%s\n' "$trees" | ST_MAINS=$repos awk -F '\t' -v repo="$repo" '
      BEGIN { n = split(ENVIRON["ST_MAINS"], m, "\n"); for (i = 1; i <= n; i++) is_main[m[i]] = 1 }
      $1 == repo && ($3 in is_main) { print $2 "\t" $3 "\t" $4; exit }')
    IFS=$'\t' read -r main_branch main main_sess <<< "$main_row"
    [ -n "$main_sess" ] || continue
    adopt_hashed_session "$repo" "$main_branch" "$main_sess"
    if ! tmux has-session -t "=$main_sess" 2>/dev/null; then
      tmux new-session -d -s "$main_sess" -c "$main" -n shell "$(shell_command)"
      session_environment "$main_sess"
      tmux set-option -t "=$main_sess:" @supertree_label "$(basename "$main")/$main_branch"
    fi
  done <<EOF
$trees
EOF
}

cmd_down() {
  local yes=0 target="" s live trees label current='' close_current=0
  while [ $# -gt 0 ]; do
    case $1 in
      -y|--yes) yes=1; shift;;
      --all) target=--all; shift;;
      --subtrees) target=--subtrees; shift;;
      *) target=$1; shift;;
    esac
  done

  trees=$(list_trees)
  if [ "$target" = --all ] || [ "$target" = --subtrees ]; then
    if [ "$target" = --all ]; then live=$(printf '%s\n' "$trees" | cut -f4 | live_only)
    else live=$(printf '%s\n' "$trees" | subtree_sessions | live_only); fi
    [ -n "$live" ] || { info "no matching tree sessions open"; return 0; }
    printf 'will close:\n' >&2
    while IFS= read -r s; do printf '  %s\n' "$(label_for_session "$s" "$trees")" >&2; done <<EOF
$live
EOF
    if [ "$yes" = 0 ]; then
      ui_ask 'unsaved editor buffers are lost. proceed?' n || die "aborted"
    fi
    [ "$target" != --subtrees ] || ensure_main_fallbacks "$trees"
    [ -z "${TMUX:-}" ] || current=$(current_session)
    # Killing our own session kills this process, so close it last.
    while IFS= read -r s; do
      evacuate_clients "$s"
      if [ "$s" = "$current" ]; then close_current=1; else tmux kill-session -t "=$s"; fi
    done <<EOF
$live
EOF
    ui_ok closed "selected tree sessions (worktrees kept)"
    [ "$close_current" = 0 ] || tmux kill-session -t "=$current"
    return 0
  fi

  local all
  all=$(printf '%s\n' "$trees" | cut -f4)

  if [ -n "$target" ]; then
    s=$(resolve_session "$target" "$trees") || ui_fail "no tree matching: $target" "st ls  ·  M-w"
  elif [ -n "${TMUX:-}" ]; then
    s=$(current_session)
  else
    die "usage: st down [branch|--all]  (no branch only works inside a tree session)"
  fi

  # guard: only ever kill sessions that belong to a known worktree
  case $'\n'"$all"$'\n' in
    *$'\n'"$s"$'\n'*) ;;
    *) die "this is not a supertree session; refusing to close it";;
  esac

  label=$(label_for_session "$s" "$trees")
  tmux has-session -t "=$s" 2>/dev/null || { info "$label already closed"; return 0; }
  evacuate_clients "$s"
  tmux kill-session -t "=$s"
  ui_ok closed "$label (worktree kept — 'st resume' brings it back)"
}

cmd_toggle() {
  local pane cur windows first second first_name target
  pane=${TMUX_PANE:-}
  if [ -n "$pane" ]; then
    cur=$(tmux display-message -p -t "$pane" '#W')
  else
    cur=$(tmux display-message -p '#W')
  fi
  windows=$(configured_windows)
  first=$(printf '%s\n' "$windows" | sed -n '1p')
  second=$(printf '%s\n' "$windows" | sed -n '2p')
  [ -n "$second" ] || { ui_toast "toggle needs at least two configured windows"; return 0; }
  first_name=$(window_name "$first")
  if [ "$cur" = "$first_name" ]; then target=$second; else target=$first; fi
  select_window_type "$target" "$windows"
}

select_window_type() {
  local type=$1 windows=${2:-} pane sess name dir
  window_enabled "$type" "$windows" || { ui_toast "$type window is not enabled in ST_WINDOWS"; return 0; }
  pane=${TMUX_PANE:-}
  sess=$(current_session)
  is_tree_session "$sess" || { ui_toast "not a supertree session"; return 0; }
  session_environment "$sess"
  name=$(window_name "$type")
  tmux select-window -t "=$sess:$name" 2>/dev/null && return 0
  dir=$(tmux display-message -p -t "${pane:-$sess}" '#{pane_current_path}')
  create_window "$sess" "$dir" "$type" 0
  tmux select-window -t "=$sess:$name"
}

cmd_window() {
  local slot=${1:-} windows type
  case $slot in ''|*[!0-9]*|0) die "usage: st window <positive slot>";; esac
  windows=$(configured_windows)
  type=$(printf '%s\n' "$windows" | sed -n "${slot}p")
  [ -n "$type" ] || { ui_toast "no window configured for slot $slot"; return 0; }
  select_window_type "$type" "$windows"
}

cmd_last() {
  local current trees sess path
  [ -n "${TMUX:-}" ] || die "st last only works inside tmux"
  [ -f "$ST_STATE/recent" ] || { ui_toast "no other tree opened yet"; return 0; }
  current=$(current_session)
  trees=$(list_trees)
  # Record the current tree so the next M-Tab comes straight back.
  if printf '%s\n' "$trees" | cut -f4 | grep -qxF -- "$current"; then
    remember_recent "$current"
  fi
  while IFS= read -r sess; do
    [ -n "$sess" ] && [ "$sess" != "$current" ] || continue
    path=$(printf '%s\n' "$trees" | awk -F '\t' -v s="$sess" '$4 == s && !found { print $3; found = 1 }')
    [ -n "$path" ] || continue
    build_session "$sess" "$path"
    attach "$sess"
    return 0
  done < "$ST_STATE/recent"
  ui_toast "no other tree opened yet"
}

cmd_agent() {
  select_window_type agent
}

cmd_leave() {
  local tty=${1:-} origin_file
  [ -n "${TMUX:-}" ] || die "st leave only works inside tmux"
  [ -n "$tty" ] || tty=$(tmux display-message -p '#{client_tty}')
  origin_file="$ST_STATE/origin/$(tty_key "$tty")"
  rm -f "$origin_file"
  tmux detach-client -t "$tty"
}

st_register_command 'down' cmd_down 'close sessions; keep worktrees' 'down [branch|--subtrees|--all]'
st_register_command 'home' cmd_home 'open the Supertree home shell'
st_register_command 'toggle' cmd_toggle ''
st_register_command 'window' cmd_window ''
st_register_command 'last' cmd_last 'switch to the previous tree'
st_register_command 'agent' cmd_agent ''
st_register_command 'split' cmd_split 'open another copy of this tab' 'split [--horizontal|--vertical]'
st_register_command 'leave' cmd_leave ''
st_register_command '_sessions' known_sessions ''
