#!/usr/bin/env bash
# End-to-end check on real git repos in a temp dir. No network; gh is faked.
set -euo pipefail

IZ="$(cd "$(dirname "$0")/.." && pwd)/bin/izumo"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

export IZUMO_GH=none GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$T/gitconfig"
printf '[user]\n name = t\n email = t@t\n[init]\n defaultBranch = main\n[commit]\n gpgsign = false\n' > "$T/gitconfig"

fail() { echo "FAIL: $*" >&2; exit 1; }
iz() { "$IZ" "$@"; }
says() {  # says <expected text> <izumo args...>; captured, since grep -q in a pipe can SIGPIPE izumo
  local msg=$1 out
  shift
  out=$("$IZ" "$@" 2>&1) || fail "izumo $*: exited $?: $out"
  grep -qF "$msg" <<<"$out" || fail "izumo $*: expected '$msg', got: $out"
}
refuses() {  # refuses <expected message> <izumo args...>
  local msg=$1 out
  shift
  if out=$("$IZ" "$@" 2>&1); then fail "expected refusal: izumo $*"; fi
  grep -qF "$msg" <<<"$out" || fail "izumo $*: expected '$msg', got: $out"
}
dev() { git -C "$T/dev/a" "$@"; }
promote() { dev checkout -q main && dev merge -q --no-ff hml -m promote && dev push -q origin main; }

# fake gh: prints whatever the test puts in FAKE_PR
mkdir -p "$T/bin"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$FAKE_PR"\n' > "$T/bin/gh"
chmod +x "$T/bin/gh"

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
says '"repo": "a", "branch": "feat-x", "stage": "wip"' ls --json
says '"repo": "b", "branch": "feat-x", "stage": "empty"' ls --json

# done waits for prod: merge-commit flow
dev fetch -q && dev merge -q --no-ff origin/feat-x -m merge && dev push -q origin hml
refuses 'a is not in prod (base)' done feat-x
out=$("$IZ" ls feat-x) && ! grep -q 'ready' <<<"$out" || fail "ready before prod: $out"
promote
says 'feat-x  [ready: izumo done]' ls --fetch feat-x
says '"ready": true' ls --json feat-x
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
FAKE_PR="https://x/pull/1	OPEN	-	$head	CHANGES_REQUESTED	failing" IZUMO_GH="$T/bin/gh" \
  says 'pr     changes-requested ci:failing https://x/pull/1' ls feat-y
FAKE_PR="https://x/pull/1	OPEN	-	$head	-	-" IZUMO_GH="$T/bin/gh" \
  says '"stage": "pr", "dirty": false, "modified": 0, "untracked": 0, "unpushed": 0, "pr": "https://x/pull/1", "review": "", "ci": ""' ls --json feat-y
dev checkout -q hml && dev fetch -q && dev merge -q --squash origin/feat-y >/dev/null && dev commit -qm "y (#1)"
dev push -q origin hml && dev push -q origin --delete feat-y
git -C a update-ref -d refs/remotes/origin/feat-y
export FAKE_PR="https://x/pull/1	MERGED	$(dev rev-parse HEAD)	$head	-	-"
refuses 'a has 1 unpushed commit(s)' rm feat-y  # without gh, squashed work looks unpushed
IZUMO_GH="$T/bin/gh" refuses 'a is not in prod (base)' done feat-y
promote
IZUMO_GH="$T/bin/gh" says 'base' ls feat-y          # ls reads the last fetch...
IZUMO_GH="$T/bin/gh" says 'prod' ls --fetch feat-y  # ...unless asked to fetch
IZUMO_GH="$T/bin/gh" iz done feat-y >/dev/null || fail "squash: done after prod"

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
says '"agent": { "state": "waiting"' ls bare --json
(cd "$T" && iz _agent working) || fail "_agent outside a task must exit 0"
CLAUDE_PROJECT_DIR="$T/root/tasks/bare" iz _agent working
says '[agent working' ls bare
(cd tasks/bare && iz _agent end)
[ ! -e tasks/bare/.agent ] || fail "_agent end left .agent"

# a removal that stops halfway resumes, and only deletes what it had listed
(cd tasks/bare && iz add b >/dev/null)
git -C b worktree lock "$T/root/tasks/bare/b"  # a goes first, then removal stops at b
refuses 'run the same command to finish' rm bare
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

# TASK_TEMPLATE: the first section is the goal, whatever its heading
printf '# {{task}}\n\n## Objetivo\nfazer x\n\n## Decisoes\n' > tpl.md
echo 'TASK_TEMPLATE=tpl.md' >> .izumo
iz new tpl >/dev/null
head -1 tasks/tpl/TASK.md | grep -qx '# tpl' || fail "template not filled"
says 'fazer x' ls tpl
iz rm tpl >/dev/null || fail "rm tpl"

echo ok
