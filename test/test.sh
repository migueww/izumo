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
refuses 'a has uncommitted changes' rm feat-x
git -C tasks/feat-x/a add f && git -C tasks/feat-x/a commit -qm f
refuses 'a has 1 unpushed commit(s)' rm feat-x
git -C tasks/feat-x/a push -q origin feat-x
says '"repo": "a", "branch": "feat-x", "stage": "wip"' ls --json
says '"repo": "b", "branch": "feat-x", "stage": "empty"' ls --json

# done waits for prod: merge-commit flow
dev fetch -q && dev merge -q --no-ff origin/feat-x -m merge && dev push -q origin hml
refuses 'a is not in prod (base)' done feat-x
promote
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
dev checkout -q hml && dev fetch -q && dev merge -q --squash origin/feat-y >/dev/null && dev commit -qm "y (#1)"
dev push -q origin hml && dev push -q origin --delete feat-y
git -C a update-ref -d refs/remotes/origin/feat-y
export FAKE_PR="https://x/pull/1	MERGED	$(dev rev-parse HEAD)	$head"
refuses 'a has 1 unpushed commit(s)' rm feat-y  # without gh, squashed work looks unpushed
IZUMO_GH="$T/bin/gh" refuses 'a is not in prod (base)' done feat-y
promote
git -C a fetch -q  # ls reads the last fetch; done fetches by itself
IZUMO_GH="$T/bin/gh" says 'prod' ls feat-y
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

# mixed root: a repo without BASE starts from its default branch
iz new mixed c >/dev/null
[ "$(git -C tasks/mixed/c rev-parse HEAD)" = "$(git -C c rev-parse origin/main)" ] || fail "fallback to default branch"
iz rm mixed >/dev/null || fail "rm mixed"

echo ok
