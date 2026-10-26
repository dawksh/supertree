#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

export HOME="$TEST_ROOT/home"
export ST_STATE="$TEST_ROOT/state"
export ST_WORKTREE_ROOT="$TEST_ROOT/trees"
export ST_CONFIG="$TEST_ROOT/missing-config"
export ST_WINDOWS=shell
export ST_TMUX_LOG="$TEST_ROOT/tmux.log"
export ST_FZF_LOG="$TEST_ROOT/fzf.log"
mkdir -p "$HOME/.local/bin" "$ST_STATE" "$TEST_ROOT/fzf-bin" "$TEST_ROOT/one/demo" "$TEST_ROOT/two/demo"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# st lives at a path with a space, and without the hard-coded Homebrew PATH
# entry so a missing fzf can be simulated. fzf may sit in /usr/bin (Linux), so
# no_fzf runs on a PATH of system-tool links minus fzf.
st_dir="$TEST_ROOT/st dir"
mkdir -p "$st_dir" "$TEST_ROOT/no-fzf-bin"
cp "$ROOT/bin/st" "$st_dir/st"
cp -R "$ROOT/bin/st-lib" "$st_dir/st-lib"
sed -i.bak 's#:/opt/homebrew/bin##' "$st_dir/st"
rm "$st_dir/st.bak"
! grep -q '/opt/homebrew/bin' "$st_dir/st" || fail 'could not drop Homebrew from the st PATH'
ln -s /usr/bin/* /bin/* "$TEST_ROOT/no-fzf-bin/" 2>/dev/null || true
rm -f "$TEST_ROOT/no-fzf-bin/fzf"
! PATH="$HOME/.local/bin:$TEST_ROOT/no-fzf-bin" command -v fzf >/dev/null || fail 'fzf is still on the no-fzf PATH'
ST="$st_dir/st"
no_fzf() { PATH="$TEST_ROOT/no-fzf-bin" "$ST" "$@"; }
with_fzf() { PATH="$TEST_ROOT/fzf-bin:/usr/bin:/bin" "$ST" "$@"; }

for repo in "$TEST_ROOT/one/demo" "$TEST_ROOT/two/demo"; do
  git -C "$repo" init -q
  git -C "$repo" -c user.name=Reviewer -c user.email=reviewer@example.invalid \
    commit -q --allow-empty -m init
done

printf '%s\n' \
  '#!/usr/bin/env bash' \
  '{ printf "%s\n" "$@"; printf -- "--\n"; } >> "$ST_TMUX_LOG"' \
  '[ "${1:-}" = has-session ] && exit 1' \
  '[ "${1:-}" = list-panes ] && printf "%s\n" "${ST_TEST_PANES:-}"' \
  'if [ "${1:-}" = capture-pane ]; then' \
  '  case " ${ST_TEST_INPUT_PANES:-} " in *" $4 "*) printf "❯ \n";; *) printf "Working...\n";; esac' \
  'fi' \
  'exit 0' > "$HOME/.local/bin/tmux"
chmod +x "$HOME/.local/bin/tmux"

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'input=$(cat)' \
  '{ printf "args: %s\n" "$*"; printf "%s\n" "$input"; } >> "$ST_FZF_LOG"' \
  'case "$*" in' \
  '  *--expect*) printf "\n"; printf "%s\n" "$input" | grep -m1 -- "$ST_FZF_PICK";;' \
  '  *) printf "%s\n" "$input" | head -1;;' \
  'esac' > "$TEST_ROOT/fzf-bin/fzf"
chmod +x "$TEST_ROOT/fzf-bin/fzf"

mkdir -p "$TEST_ROOT/main-dir" "$TEST_ROOT/feat-dir" "$TEST_ROOT/uniqpath"
rows=$(printf '%s\t%s\t%s\n' \
  'demo/Main      running' "$TEST_ROOT/main-dir" 'demo/Main' \
  'demo/feat[x]   closed' "$TEST_ROOT/feat-dir" 'demo/feat[x]' \
  'demo/other     done' "$TEST_ROOT/uniqpath" 'demo/other')
export ST_PICKER_ROWS="$rows"

# Popups keep st's exit visible (-EE) and quote a path with a space.
quoted_self=$(printf '%q' "$ST")
no_fzf go --popup client-1
grep -qx -- '-EE' "$ST_TMUX_LOG" || fail 'tree popup did not use display-popup -EE'
grep -qxF -- "$quoted_self go --picker" "$ST_TMUX_LOG" || fail 'tree popup command did not quote st path'
mkdir -p "$ST_STATE"
printf '%s\n' "$TEST_ROOT/one/demo" > "$ST_STATE/repos"
: > "$ST_TMUX_LOG"
no_fzf roots --popup client-1
grep -qx -- '-EE' "$ST_TMUX_LOG" || fail 'roots popup did not use display-popup -EE'
grep -qxF -- "$quoted_self roots" "$ST_TMUX_LOG" || fail 'roots popup command did not quote st path'

# Missing fzf fails with a clear message instead of an empty picker.
if no_fzf go > "$TEST_ROOT/out" 2> "$TEST_ROOT/err"; then fail 'tree picker succeeded without fzf'; fi
grep -q 'fzf is required' "$TEST_ROOT/err" || fail 'tree picker did not say fzf is required'
[ ! -s "$TEST_ROOT/out" ] || fail 'tree picker wrote its error to stdout'
if no_fzf roots > "$TEST_ROOT/out" 2> "$TEST_ROOT/err"; then fail 'root picker succeeded without fzf'; fi
grep -q 'fzf is required' "$TEST_ROOT/err" || fail 'root picker did not say fzf is required'
[ ! -s "$TEST_ROOT/out" ] || fail 'root picker wrote its error to stdout'

# Queries match the tree label only, literally and case-insensitively.
if no_fzf go running >/dev/null 2>&1; then fail 'query matched the status column'; fi
if no_fzf go uniqpath >/dev/null 2>&1; then fail 'query matched the path column'; fi
: > "$ST_TMUX_LOG"
no_fzf go 'feat[' 2> "$TEST_ROOT/err" || fail "query 'feat[' failed: $(cat "$TEST_ROOT/err")"
! grep -qi 'regex\|bracket\|unmatched' "$TEST_ROOT/err" || fail "query 'feat[' raised a regex error"
grep -qxF '=demo/feat[x]' "$ST_TMUX_LOG" || fail "query 'feat[' did not attach demo/feat[x]"
: > "$ST_TMUX_LOG"
no_fzf go main 2>/dev/null || fail 'lowercase query did not match demo/Main'
grep -qxF '=demo/Main' "$ST_TMUX_LOG" || fail 'lowercase query did not attach demo/Main'

# "+ new branch" offers only repos that still exist.
export ST_FZF_PICK='+ new branch'
printf '%s\n' "$TEST_ROOT/one/demo" "$TEST_ROOT/gone/demo" "$TEST_ROOT/two/demo" > "$ST_STATE/repos"
printf 'picked\n' | with_fzf go >/dev/null 2>&1 || fail '+ new branch failed with one missing repo'
repo_choices=$(awk '/^args: --prompt=repo> /{on=1; next} /^args: /{on=0} on' "$ST_FZF_LOG")
[ -n "$repo_choices" ] || fail 'repo chooser was not shown for two live repos'
if printf '%s\n' "$repo_choices" | grep -qF "$TEST_ROOT/gone/demo"; then
  fail 'repo chooser offered a repo whose directory is gone'
fi
git -C "$TEST_ROOT/one/demo" show-ref --verify --quiet refs/heads/picked ||
  fail '+ new branch did not create the branch in the chosen repo'

printf '%s\n' "$TEST_ROOT/gone/demo" "$TEST_ROOT/also-gone/demo" > "$ST_STATE/repos"
if printf 'nothing\n' | with_fzf go > /dev/null 2> "$TEST_ROOT/err"; then
  fail '+ new branch succeeded with no existing repos'
fi
grep -q 'no roots known yet' "$TEST_ROOT/err" || fail 'all-missing repos did not say no roots known yet'

# Agent status comes from one tmux snapshot; an empty @st_agent_state leaves
# consecutive tabs that must not shift the other fields.
unset ST_PICKER_ROWS
printf '%s\n' "$TEST_ROOT/one/demo" > "$ST_STATE/repos"
for b in plain harness prompt custom; do
  git -C "$TEST_ROOT/one/demo" worktree add -q -b "$b" "$ST_WORKTREE_ROOT/demo/$b"
done
sess_of() { "$ST" _sessions | grep -x "demo/$1"; }
panes=$(printf '%s\n' \
  "$(sess_of plain)"$'\tclaude\t%1\t\t\tbash' \
  "$(sess_of harness)"$'\tclaude\t%2\t\t\tclaude' \
  "$(sess_of prompt)"$'\tclaude\t%3\trunning\t\tnode' \
  "$(sess_of custom)"$'\tshell\t%4\tdone\tagent\tbash' \
  "$(sess_of custom)"$'\tagent\t%5\trunning\tagent\tbash')
: > "$ST_TMUX_LOG"
: > "$ST_FZF_LOG"
ST_TEST_PANES=$panes ST_TEST_INPUT_PANES=%3 ST_FZF_PICK=no-such-row with_fzf go >/dev/null 2>&1 ||
  fail 'picker failed while reading agent statuses'
for want in 'plain +done' 'harness +running' 'prompt +input' 'custom +running' 'picked +closed'; do
  grep -Eq "^demo/$want"$'\t' "$ST_FZF_LOG" || fail "picker row missing: demo/$want"
done
[ "$(grep -cx 'list-panes' "$ST_TMUX_LOG")" = 1 ] || fail 'agent statuses did not use one list-panes call'
captured=$(grep -A3 -x 'capture-pane' "$ST_TMUX_LOG" | grep '^%' | sort | tr '\n' ' ')
[ "$captured" = '%2 %3 %5 ' ] || fail "screens captured for non-running panes: [$captured]"

printf 'ok: picker popups, fzf check, query matching, and repo chooser\n'
