#!/usr/bin/env bash
# End-to-end check on real git repos in a temp dir. No network; gh is faked.
# shellcheck disable=SC2015,SC2155  # `a && b || fail` and export x=$(...) are fine in a test
set -euo pipefail

IZ=${IZUMO_BIN:-"$(cd "$(dirname "$0")/.." && pwd)/bin/izumo"}  # IZUMO_BIN: a binary built from this checkout
T=$(mktemp -d)
# bash 3.2 exits with the trap's last status, so hand the test's own back
cleanup() {
  local st=$?
  # shellcheck disable=SC2046  # job pids
  kill $(jobs -p) 2>/dev/null || true
  rm -rf "$T"
  exit "$st"
}
trap cleanup EXIT
exec </dev/null  # izumo asks before stopping servers only on a terminal

export IZUMO_GH=none GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$T/gitconfig"
printf '[user]\n name = t\n email = t@t\n[init]\n defaultBranch = main\n[commit]\n gpgsign = false\n' > "$T/gitconfig"

fail() { echo "FAIL: $*" >&2; exit 1; }
iz() { "$IZ" "$@"; }
says() {  # says <expected text> <izumo args...>; captured, since grep -q in a pipe can SIGPIPE izumo
  local msg=$1 out
  shift
  out=$("$IZ" "$@" 2>&1) || fail "izumo $*: exited $?: $out"
  grep -qF -- "$msg" <<<"$out" || fail "izumo $*: expected '$msg', got: $out"
}
refuses() {  # refuses <expected message> <izumo args...>
  local msg=$1 out
  shift
  if out=$("$IZ" "$@" 2>&1); then fail "expected refusal: izumo $*"; fi
  grep -qF -- "$msg" <<<"$out" || fail "izumo $*: expected '$msg', got: $out"
}
dev() { git -C "$T/dev/a" "$@"; }
promote() { dev checkout -q main && dev merge -q --no-ff hml -m promote && dev push -q origin main; }

PY=
for p in python3 python; do if "$p" -c '' 2>/dev/null; then PY=$p; break; fi; done
[ -n "$PY" ] || fail "the test needs python"
# jsays <expected text> <ls args...>: ls --json flattened to one line per task and per repo,
# as `task key=value...` and `task/repo key=value...`, values in JSON
jsays() {
  local msg=$1 out
  shift
  out=$("$IZ" ls --json "$@" 2>&1 | "$PY" -c 'import json, sys
for t in json.load(sys.stdin):
    print(t["task"], " ".join(k + "=" + json.dumps(v) for k, v in t.items() if k != "repos"))
    for r in t["repos"]:
        print(t["task"] + "/" + r["repo"], " ".join(k + "=" + json.dumps(v) for k, v in r.items()))') ||
    fail "izumo ls --json $*: $out"
  grep -qF -- "$msg" <<<"$out" || fail "izumo ls --json $*: expected '$msg', got: $out"
}

# fake gh: prints whatever the test puts in FAKE_PR; a .cmd runs it on Windows, as gh.exe would
mkdir -p "$T/bin"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "${FAKE_PR:-[]}"\n' > "$T/bin/gh"
chmod +x "$T/bin/gh"
GHFAKE=$T/bin/gh
case $(uname -s) in MINGW*|MSYS*|CYGWIN*)
  printf '@"%s" "%s" %%*\r\n' "$(cygpath -w "$(command -v bash)")" "$(cygpath -w "$T/bin/gh")" > "$T/bin/gh.cmd"
  GHFAKE=$T/bin/gh.cmd;;
esac
# pr <url> <state> <merge commit> <head> <review decision> <check conclusion>: gh pr list --json
pr() {
  local merge=null checks=
  if [ -n "$3" ]; then merge="{\"oid\":\"$3\"}"; fi
  if [ -n "$6" ]; then checks="{\"conclusion\":\"$6\"}"; fi
  printf '[{"url":"%s","state":"%s","mergeCommit":%s,"headRefOid":"%s","reviewDecision":"%s","statusCheckRollup":[%s]}]' \
    "$1" "$2" "$merge" "$4" "$5" "$checks"
}

# two repos with main + hml, cloned into the root
mkdir -p "$T/remote" "$T/dev" "$T/root"
for r in a b; do
  git init -q --bare "$T/remote/$r.git"
  git clone -q "$T/remote/$r.git" "$T/dev/$r" 2>/dev/null
  git -C "$T/dev/$r" commit -q --allow-empty -m init
  git -C "$T/dev/$r" push -q origin main main:hml
  git clone -q "$T/remote/$r.git" "$T/root/$r"
done
# c only has main: BASE=hml must fall back to origin's default branch
git init -q --bare "$T/remote/c.git"
git clone -q "$T/remote/c.git" "$T/dev/c" 2>/dev/null
git -C "$T/dev/c" commit -q --allow-empty -m init && git -C "$T/dev/c" push -q origin main
git clone -q "$T/remote/c.git" "$T/root/c"
dev fetch -q && dev checkout -q hml
cat > "$T/root/.izumo" <<'EOF'
BASE=hml
PROD=main
post_add() { echo "$TASK $REPO" >> "$SRC/../hook.log"; }
EOF
cd "$T/root"

# new + add (task inferred from cwd)
iz new feat-x a >/dev/null
[ -f tasks/feat-x/a/.git ] || fail "worktree a not created"
[ "$(git -C tasks/feat-x/a branch --show-current)" = feat-x ] || fail "branch name"
[ "$(git -C tasks/feat-x/a rev-parse HEAD)" = "$(git -C a rev-parse origin/hml)" ] || fail "not based on hml"
grep -q 'feat-x a' hook.log || fail "post_add hook"
for f in TASK.md AGENTS.md CLAUDE.md feat-x.code-workspace; do [ -f "tasks/feat-x/$f" ] || fail "missing $f"; done
(cd tasks/feat-x/a && iz add b >/dev/null)
grep -q '"path": "b"' tasks/feat-x/feat-x.code-workspace || fail "workspace misses b"
refuses 'a is already in task' add a --task feat-x

# nothing that would lose work gets removed
echo x > tasks/feat-x/a/f
refuses 'a has uncommitted changes (0 modified, 1 untracked)' rm feat-x
says '1-untracked' ls feat-x
git -C tasks/feat-x/a add f && git -C tasks/feat-x/a commit -qm f
refuses 'a has 1 unpushed commit(s)' rm feat-x
git -C tasks/feat-x/a push -q origin feat-x
jsays 'feat-x/a repo="a" branch="feat-x" stage="wip"'
jsays 'feat-x/b repo="b" branch="feat-x" stage="empty"'
# colors only when asked (the shim asks on a terminal), and NO_COLOR wins
out=$(IZUMO_COLOR=1 "$IZ" ls feat-x) && grep -qF $'\e[33mwip' <<<"$out" || fail "no color with IZUMO_COLOR=1: $out"
out=$(IZUMO_COLOR=1 NO_COLOR=1 "$IZ" ls feat-x) && ! grep -qF $'\e[' <<<"$out" || fail "color despite NO_COLOR: $out"

# done waits for prod: merge-commit flow
dev fetch -q && dev merge -q --no-ff origin/feat-x -m merge && dev push -q origin hml
refuses 'a is not in prod (base)' done feat-x
out=$("$IZ" ls feat-x) && ! grep -q 'ready' <<<"$out" || fail "ready before prod: $out"
promote
says 'feat-x  [ready: izumo done]' ls --fetch feat-x
jsays 'ready=true' feat-x
says 'would remove feat-x' done feat-x --dry-run
[ -d tasks/feat-x ] || fail "dry-run removed"
iz done >/dev/null
[ ! -e tasks/feat-x ] || fail "task folder still there"
ls tasks/.archive/*-feat-x.md >/dev/null || fail "TASK.md not archived"
[ "$(git -C a worktree list | wc -l)" -eq 1 ] || fail "worktree metadata left"
git -C a show-ref --quiet refs/heads/feat-x && fail "branch not deleted"

# squash flow: only gh knows the PR shipped; remote branch deleted and pruned
iz new feat-y a >/dev/null
echo y > tasks/feat-y/a/y && git -C tasks/feat-y/a add y && git -C tasks/feat-y/a commit -qm y
git -C tasks/feat-y/a push -q origin feat-y
head=$(git -C tasks/feat-y/a rev-parse HEAD)
FAKE_PR=$(pr https://x/pull/1 OPEN "" "$head" CHANGES_REQUESTED FAILURE) IZUMO_GH=$GHFAKE \
  says 'pr     changes-requested ci:failing https://x/pull/1' ls feat-y
FAKE_PR=$(pr https://x/pull/1 OPEN "" "$head" "" "") IZUMO_GH=$GHFAKE \
  jsays 'stage="pr" dirty=false modified=0 untracked=0 unpushed=0 pr="https://x/pull/1" review="" ci=""' feat-y
dev checkout -q hml && dev fetch -q && dev merge -q --squash origin/feat-y >/dev/null && dev commit -qm "y (#1)"
dev push -q origin hml && dev push -q origin --delete feat-y
git -C a update-ref -d refs/remotes/origin/feat-y
FAKE_PR=$(pr https://x/pull/1 MERGED "$(dev rev-parse HEAD)" "$head" "" "")
export FAKE_PR
refuses 'a has 1 unpushed commit(s)' rm feat-y  # without gh, squashed work looks unpushed
IZUMO_GH=$GHFAKE refuses 'a is not in prod (base)' done feat-y
promote
IZUMO_GH=$GHFAKE says 'base' ls feat-y          # ls reads the last fetch...
IZUMO_GH=$GHFAKE says 'prod' ls --fetch feat-y  # ...unless asked to fetch
IZUMO_GH=$GHFAKE iz done feat-y >/dev/null || fail "squash: done after prod"

# review: existing remote branch is checked out, never shipped, removed with rm
git -C "$T/dev/b" checkout -q -b their-pr && git -C "$T/dev/b" commit -q --allow-empty -m pr
git -C "$T/dev/b" push -q origin their-pr
iz new review b --branch their-pr >/dev/null
[ "$(git -C tasks/review/b rev-parse HEAD)" = "$(git -C "$T/dev/b" rev-parse HEAD)" ] || fail "review branch"
refuses 'nothing reached prod' done review
touch tasks/review/notes.txt
refuses 'unknown file notes.txt' rm review
rm tasks/review/notes.txt
(cd tasks/review/b && refuses 'you are inside it' rm review)
iz rm review >/dev/null || fail "rm review"
[ ! -e tasks/review ] || fail "review folder still there"

# Windows: a folder some program has open keeps the task whole instead of stopping halfway
case $(uname -s) in MINGW*|MSYS*|CYGWIN*)
  iz new busy a b >/dev/null
  (cd tasks/busy/b && exec sleep 30) &
  busy=$!
  sleep 1
  refuses 'a program has the folder open' rm busy
  [ -f tasks/busy/a/.git ] && [ ! -e tasks/busy/.removing ] || fail "busy: removal started"
  kill $busy && wait $busy 2>/dev/null || true
  iz rm busy >/dev/null || fail "rm busy";;
esac

# drop: a repo leaves the task when nothing would be lost; the task stays
iz new shrink a b >/dev/null
echo z > tasks/shrink/b/z
refuses 'b: kept (uncommitted changes: 0 modified, 1 untracked)' drop b --task shrink
rm tasks/shrink/b/z
git -C tasks/shrink/a commit -q --allow-empty -m z
refuses 'a: kept (1 unpushed commit(s))' drop a --task shrink
git -C tasks/shrink/a push -q origin shrink
(cd tasks/shrink/b && refuses 'you are inside it' drop b)
(cd tasks/shrink && iz drop b >/dev/null) || fail "drop b"
[ ! -e tasks/shrink/b ] && [ -f tasks/shrink/a/.git ] || fail "drop removed the wrong thing"
git -C b show-ref --quiet refs/heads/shrink && fail "drop left the branch"
grep -q '"path": "b"' tasks/shrink/shrink.code-workspace && fail "workspace still lists b"
refuses 'b is not in task' drop b --task shrink
iz rm shrink >/dev/null || fail "rm shrink"

# mixed root: a repo without BASE starts from its default branch
iz new mixed c >/dev/null
[ "$(git -C tasks/mixed/c rev-parse HEAD)" = "$(git -C c rev-parse origin/main)" ] || fail "fallback to default branch"
iz rm mixed >/dev/null || fail "rm mixed"

# bare izumo: ls inside a root, usage outside
iz new bare a >/dev/null
out=$("$IZ") && grep -q '^bare' <<<"$out" || fail "bare izumo inside a root: $out"
out=$(cd "$T" && "$IZ") && grep -q 'izumo new' <<<"$out" || fail "bare izumo outside a root: $out"

# agent state from the Claude Code hooks: written inside a task, silent anywhere else
(cd tasks/bare/a && iz _agent waiting)
says '[agent waiting' ls bare
jsays 'agent={"state": "waiting"' bare
(cd "$T" && iz _agent working) || fail "_agent outside a task must exit 0"
CLAUDE_PROJECT_DIR="$T/root/tasks/bare" iz _agent working
says '[agent working' ls bare
(cd tasks/bare && iz _agent end)
[ ! -e tasks/bare/.agent ] || fail "_agent end left .agent"

# a removal that stops halfway resumes, and only deletes what it had listed
(cd tasks/bare && iz add b >/dev/null)
git -C b worktree lock "$T/root/tasks/bare/b"  # a goes first, then removal stops at b
refuses 'run the same command to finish' rm bare
refuses 'a removal stopped halfway' add c --task bare  # it would be left out of the list
git -C b worktree unlock "$T/root/tasks/bare/b"
mkdir tasks/bare/a  # leftover of a, as Windows leaves a locked cwd behind
says 'removal stopped halfway' ls bare
mkdir tasks/bare/notes
refuses 'unknown file notes' rm bare  # resuming deletes only what it had listed
rmdir tasks/bare/notes
iz rm bare >/dev/null || fail "resume rm"
[ ! -e tasks/bare ] || fail "resumed rm left the folder"
git -C a show-ref --quiet refs/heads/bare && fail "resumed rm left branch in a"
git -C b show-ref --quiet refs/heads/bare && fail "resumed rm left branch in b"

# adopt: a hand-made worktree moves into a new task named after nothing but its branch
git -C a worktree add -q "$T/root/old-wt" -b old-branch origin/hml
iz adopt old-wt --task adopted >/dev/null
[ -f tasks/adopted/a/.git ] && [ ! -e old-wt ] || fail "adopt did not move the worktree"
[ "$(git -C tasks/adopted/a branch --show-current)" = old-branch ] || fail "adopt branch"
grep -q 'T_BRANCH=old-branch' tasks/adopted/.task || fail "adopted task branch"
git -C a worktree list | grep -q 'tasks/adopted/a' || fail "git does not know the new path"
refuses 'not a git worktree' adopt tasks --task adopted
iz rm adopted >/dev/null || fail "rm adopted"

# adopt from another task: the task it left stops listing it
iz new from-t a b >/dev/null
iz adopt tasks/from-t/b --task to-t >/dev/null
grep -q '"path": "b"' tasks/from-t/from-t.code-workspace && fail "old task's workspace still lists b"
grep -q '"path": "b"' tasks/to-t/to-t.code-workspace || fail "new task's workspace misses b"
iz rm to-t >/dev/null || fail "rm to-t"

# mv: git follows the worktrees to the new folder; the branch keeps its name
iz mv from-t moved >/dev/null
[ ! -e tasks/from-t ] && [ -f tasks/moved/moved.code-workspace ] && [ ! -e tasks/moved/from-t.code-workspace ] || fail "mv folder"
head -1 tasks/moved/TASK.md | grep -qx '# moved' || fail "mv TASK.md heading"
git -C a worktree list | grep -q 'tasks/moved/a' || fail "git does not know the new path"
[ "$(git -C tasks/moved/a branch --show-current)" = from-t ] || fail "mv renamed the branch"
iz new taken >/dev/null
refuses "task 'taken' already exists" mv moved taken

# base: repos added later start from it
iz base main --task taken >/dev/null
grep -q 'T_BASE=main' tasks/taken/.task || fail "base not saved"
iz add a --task taken >/dev/null
[ "$(git -C tasks/taken/a rev-parse HEAD)" = "$(git -C a rev-parse origin/main)" ] || fail "add ignored the new base"
refuses 'no repo has origin/nope' base nope --task taken  # a typo or an unpushed branch
grep -q 'T_BASE=main' tasks/taken/.task || fail "refused base was saved"
refuses 'invalid branch name' base 'a..b' --task taken

# stacked task: starts from another task's pushed branch; repos without it say they fall back
refuses 'no repo has origin/taken' new stacked a --base taken
[ ! -e tasks/stacked ] || fail "refused new left a task behind"
git -C tasks/taken/a push -q origin taken
says 'b: no origin/taken, starting from origin/main' new stacked a b --base taken
[ "$(git -C tasks/stacked/a rev-parse HEAD)" = "$(git -C a rev-parse origin/taken)" ] || fail "stacked base"
iz rm moved taken stacked >/dev/null || fail "rm after mv"

# TASK_TEMPLATE: the first section is the goal, whatever its heading
printf '# {{task}}\n\n## Objetivo\nfazer x\n\n## Decisoes\n' > tpl.md
echo 'TASK_TEMPLATE=tpl.md' >> .izumo
iz new tpl >/dev/null
head -1 tasks/tpl/TASK.md | grep -qx '# tpl' || fail "template not filled"
says 'fazer x' ls tpl
iz rm tpl >/dev/null || fail "rm tpl"

# help: -h works after any command, but after run it belongs to the command
says 'izumo new <task>' new --help
says 'izumo ls' ls x -h

# run: in each repo, in order, with the hook variables; fails if any repo failed
iz new runner a b >/dev/null
out=$(cd tasks/runner && "$IZ" run -- sh -c 'echo "$TASK/$REPO on $BRANCH"') || fail "run: $out"
[ "$out" = "$(printf '== a\nrunner/a on runner\n== b\nrunner/b on runner')" ] || fail "run output: $out"
refuses 'failed in: b' run --task runner -- sh -c '[ "$REPO" = a ]'
out=$("$IZ" run --task runner git -h 2>&1) || true
grep -q '== a' <<<"$out" || fail "run took git's -h as its own: $out"
iz rm runner >/dev/null || fail "rm runner"

# pre_remove: runs in each worktree before anything is removed; failing keeps the task whole
cat >> .izumo <<'EOF'
pre_remove() { [ ! -e "$SRC/../keep-$TASK" ] && echo "$TASK $REPO" >> "$SRC/../rm.log"; }
EOF
iz new hooked a b >/dev/null
touch keep-hooked
refuses 'pre_remove failed in a' rm hooked
[ -f tasks/hooked/a/.git ] && [ ! -e tasks/hooked/.removing ] && ! ls tasks/.archive/*-hooked.md >/dev/null 2>&1 ||
  fail "a failed pre_remove still removed something"
refuses 'pre_remove failed in b' drop b --task hooked
rm keep-hooked
iz drop b --task hooked >/dev/null || fail "drop with pre_remove"
grep -qx 'hooked b' rm.log || fail "drop skipped pre_remove"
iz rm hooked >/dev/null || fail "rm with pre_remove"
grep -qx 'hooked a' rm.log || fail "rm skipped pre_remove"
echo 'pre_remove() { touch left-behind; }' >> .izumo
iz new messy a >/dev/null
refuses 'pre_remove left changes in a' rm messy
echo 'pre_remove() { :; }' >> .izumo
rm tasks/messy/a/left-behind
iz rm messy >/dev/null || fail "rm messy"

# rm --discard: work given up on goes anyway, after saying what is lost; done never discards
iz new abandoned a b >/dev/null
echo x > tasks/abandoned/a/scratch
git -C tasks/abandoned/b commit -q --allow-empty -m wip
touch tasks/abandoned/notes.txt
refuses 'a has uncommitted changes' rm abandoned
says 'would remove abandoned, discarding: a has uncommitted changes (0 modified, 1 untracked); b has 1 unpushed commit(s); unknown file notes.txt' \
  rm abandoned --dry-run --discard
[ -f tasks/abandoned/a/scratch ] || fail "discard dry-run removed"
refuses '--discard only goes with rm' done abandoned --discard
iz rm abandoned --discard >/dev/null 2>&1 || fail "rm --discard"
[ ! -e tasks/abandoned ] || fail "discard left the folder"
git -C b show-ref --quiet refs/heads/abandoned && fail "discard left the branch"

# a branch deleted on GitHub (PR closed unmerged) stops counting as pushed once fetched
iz new closed a >/dev/null
git -C tasks/closed/a commit -q --allow-empty -m c && git -C tasks/closed/a push -q origin closed
git -C "$T/dev/a" push -q origin --delete closed
refuses 'a has 1 unpushed commit(s)' rm closed
iz rm closed --discard >/dev/null 2>&1 || fail "rm closed"

# a merged PR of an earlier task with the same branch name says nothing about this one
iz new reused a >/dev/null
git -C tasks/reused/a commit -q --allow-empty -m r && git -C tasks/reused/a push -q origin reused
FAKE_PR=$(pr https://x/pull/9 MERGED "$(git -C a rev-parse origin/main)" "$(git -C a rev-parse origin/main)" "" "") IZUMO_GH=$GHFAKE \
  says 'wip' ls reused
iz rm reused >/dev/null || fail "rm reused"

# a commit made while the removal runs keeps its branch; the same name archived twice a day keeps both
echo 'pre_remove() { git commit -q --allow-empty -m late; }' >> .izumo
iz new late a >/dev/null
says 'branch late moved since the checks' rm late
git -C a show-ref --quiet refs/heads/late || fail "a commit made during the removal was deleted"
git -C a branch -D -q late
echo 'pre_remove() { :; }' >> .izumo
iz new late >/dev/null && iz rm late >/dev/null || fail "rm late again"
ls tasks/.archive/*-late.md tasks/.archive/*-late-2.md >/dev/null || fail "second archive of late overwrote the first"

# pull: clones fast-forward; untracked files don't block, local commits and changes do
echo 1 > "$T/dev/c/f" && git -C "$T/dev/c" add f && git -C "$T/dev/c" commit -qm f && git -C "$T/dev/c" push -q origin main
touch a/.env
git -C b commit -q --allow-empty -m local
out=$("$IZ" pull) || fail "pull: $out"
grep -q '^a  *updated main' <<<"$out" && grep -q '^b  *fetched (main is ahead of origin/main)' <<<"$out" &&
  grep -q '^c  *updated main' <<<"$out" && ! grep -q '^tasks' <<<"$out" || fail "pull output: $out"
[ "$(cat c/f)" = 1 ] || fail "pull did not update c's files"
echo 2 > c/f
git -C "$T/dev/c" commit -q --allow-empty -m g && git -C "$T/dev/c" push -q origin main
says 'fetched (main has local changes)' pull c
says 'up to date' pull a
refuses 'no git clone' pull nope

# servers: done, rm and drop stop the dev servers running from the task, once told to
serve() {  # serve <task> <repo>: a server running from that worktree, like pnpm dev; sets PORT
  PORT=$((20000 + RANDOM % 20000))
  # a bare socket: http.server looks up DNS before listening, which hangs on macOS runners
  (cd "tasks/$1/$2" && exec "$PY" -c 'import socket, sys, time; s = socket.socket(); s.bind(("127.0.0.1", int(sys.argv[1]))); s.listen(); time.sleep(300)' "$PORT" "$(pwd -P)") &
  for _ in 1 2 3 4 5 6 7 8 9 10; do up "$PORT" && break; sleep 1; done
  says "serving:$PORT" ls "$1"
}
up() { "$PY" -c "import socket; socket.create_connection(('127.0.0.1', $1), 2)" 2>/dev/null; }
iz new served a b >/dev/null
serve served a
jsays "serving=[$PORT]" served
says ":$PORT in a (pid" rm served --dry-run
refuses 'pass --stop to stop it, or --no-stop' rm served
up "$PORT" || fail "a refusal stopped the server"
iz drop b --task served >/dev/null || fail "drop of a repo serving nothing asked about servers"
says "stopped" drop a --task served --stop
up "$PORT" && fail "drop --stop left the server running"
iz add a --task served >/dev/null
serve served a
case $(uname -s) in
  MINGW*|MSYS*|CYGWIN*)  # Windows won't remove a folder a running server has open
    refuses 'a program has the folder open' rm served --no-stop
    iz rm served --stop >/dev/null 2>&1 || fail "rm --stop";;
  *)
    iz rm served --no-stop >/dev/null || fail "rm --no-stop"
    up "$PORT" || fail "rm --no-stop stopped the server"
    kill %% 2>/dev/null || true;;
esac
[ ! -e tasks/served ] || fail "served not removed"

echo ok
