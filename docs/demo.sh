#!/usr/bin/env bash
# Records docs/demo.svg from real izumo runs: a scratch root with local remotes, a fake gh and
# two dev servers, the scenes run one by one, then their output drawn as an animated SVG.
#   bash docs/demo.sh                      # records the izumo on PATH
#   IZUMO_BIN=/tmp/izumo bash docs/demo.sh # or a build
# Needs git, node (the dev servers) and python 3 (the drawing).
# shellcheck disable=SC2016  # the scenes are single-quoted on purpose: they run later, in the root
set -euo pipefail

IZ=${IZUMO_BIN:-izumo}
OUT=$(cd "$(dirname "$0")" && pwd)/demo.svg
PY=
for p in python3 python; do if "$p" -c '' 2>/dev/null; then PY=$p; break; fi; done
[ -n "$PY" ] || { echo "demo: needs python 3" >&2; exit 1; }
command -v node >/dev/null || { echo "demo: needs node" >&2; exit 1; }

T=$(mktemp -d)
native() { cygpath -m "$1" 2>/dev/null || echo "$1"; }  # python on Windows reads C:/ paths
cleanup() {
  # shellcheck disable=SC2046  # job pids
  kill $(jobs -p) 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT
exec </dev/null
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$T/gitconfig" DEMO=$T
printf '[user]\n name = demo\n email = demo@example.com\n[init]\n defaultBranch = main\n[commit]\n gpgsign = false\n' > "$T/gitconfig"
# a terminal 160 columns wide, someone at it to answer, colors on
export IZUMO_COLOR=1 IZUMO_TTY=1 COLUMNS=160

# GitHub, faked: web-front's PR is whatever $T/pr-web-front.json says; the rest have none.
# It takes a moment, as gh does, so the bar has something to show.
mkdir -p "$T/bin"
cat > "$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
sleep 0.3
f="$DEMO/pr-$(basename "$PWD").json"
if [ -f "$f" ]; then cat "$f"; else echo '[]'; fi
EOF
chmod +x "$T/bin/gh"
export IZUMO_GH=$T/bin/gh
case $(uname -s) in MINGW*|MSYS*|CYGWIN*)
  printf '@"%s" "%s" %%*\r\n' "$(cygpath -w "$(command -v bash)")" "$(cygpath -w "$T/bin/gh")" > "$T/bin/gh.cmd"
  IZUMO_GH=$T/bin/gh.cmd;;
esac

# the repos, each with main (prod) and hml (base), cloned into the root
code=$T/code
mkdir -p "$T/remote" "$T/dev" "$code"
for r in billing-service web-front infra auth-service; do
  git init -q --bare "$T/remote/$r.git"
  git clone -q "$T/remote/$r.git" "$T/dev/$r" 2>/dev/null
  git -C "$T/dev/$r" commit -q --allow-empty -m init
  git -C "$T/dev/$r" push -q origin main main:hml
  git clone -q "$T/remote/$r.git" "$code/$r"
done
printf 'BASE=hml\nPROD=main\n' > "$code/.izumo"
cd "$code"

work() {  # work <task> <repo>: a commit on the task's branch, pushed
  git -C "tasks/$1/$2" commit -q --allow-empty -m "$1" && git -C "tasks/$1/$2" push -q origin "$1"
}
ship() {  # ship <task> <repo>: merged into hml, then promoted to main
  git -C "$T/dev/$2" fetch -q
  git -C "$T/dev/$2" checkout -q hml && git -C "$T/dev/$2" merge -q --no-ff "origin/$1" -m "merge $1"
  git -C "$T/dev/$2" checkout -q main && git -C "$T/dev/$2" merge -q --no-ff hml -m promote
  git -C "$T/dev/$2" push -q origin hml main
  git -C "$2" fetch -q
}
serve() {  # serve <task> <repo> <port>: a dev server from that worktree
  (cd "tasks/$1/$2" && exec node -e "require('net').createServer(s => s.on('error', () => {})).listen($3, '127.0.0.1'); setInterval(() => {}, 1e9)" 2>"$T/serve-$3.log") &
  for _ in 1 2 3 4 5 6 7 8 9 10; do node -e "require('net').connect($3, '127.0.0.1').on('connect', () => process.exit(0)).on('error', () => process.exit(1))" 2>/dev/null && return; sleep 1; done
}
pr42() {  # pr42 <state>: web-front's PR as gh pr list --json prints it
  printf '[{"number":42,"url":"https://github.com/acme/web-front/pull/42","state":"%s","mergeCommit":null,"headRefOid":"%s","reviewDecision":"APPROVED","mergeable":"MERGEABLE","statusCheckRollup":[{"conclusion":"SUCCESS"}]}]' \
    "$1" "$(git -C tasks/rollout-flag/web-front rev-parse HEAD)" > "$T/pr-web-front.json"
}

# two tasks already under way: one shipped, one with a commit only here and its agent asking
"$IZ" new login-copy web-front >/dev/null 2>&1
work login-copy web-front && ship login-copy web-front
"$IZ" new audit-log auth-service web-front >/dev/null 2>&1
printf '# audit-log\n\n## Goal\nKeep who changed what in the admin\n' > tasks/audit-log/TASK.md
git -C tasks/audit-log/auth-service commit -q --allow-empty -m audit
(cd tasks/audit-log/auth-service && printf '{"session_id":"demo"}' | "$IZ" _agent waiting)

# scenes: the folder the prompt shows, the command typed, the seconds the screen holds after it
scenes=$T/scenes
: > "$scenes"
n=0
scene() {  # scene <dir under the root> <hold> <command>: runs it there, output kept for the drawing
  n=$((n + 1))
  local dir=$1 hold=$2 cmd=$3
  printf '%s\t%s\t%s\t%s\n' "$dir" "$cmd" "$(native "$T/scene$n.out")" "$hold" >> "$scenes"
  (cd "$code/$dir" && eval "${cmd//izumo/\"\$IZ\"}" > "$T/scene$n.out" 2>&1 <<<"y") || true
}

scene . 1.2 'izumo new rollout-flag billing-service web-front'
printf '# rollout-flag\n\n## Goal\nShip the rollout flag: billing decides, web shows it\n' > tasks/rollout-flag/TASK.md
scene . 0.6 'cd tasks/rollout-flag/web-front'
scene tasks/rollout-flag/web-front 1.4 'izumo add infra'
# the work happens: billing ships, web-front's PR is approved; both run their dev servers
work rollout-flag billing-service && ship rollout-flag billing-service
work rollout-flag web-front && pr42 OPEN
serve rollout-flag billing-service 8080
serve rollout-flag web-front 5173
sleep 4
scene tasks/rollout-flag/web-front 5.5 'izumo ls'
scene tasks/rollout-flag/web-front 1.6 'izumo stop web-front'
scene tasks/rollout-flag/web-front 0.6 'cd ~/code'
scene . 3 'izumo done'
ship rollout-flag web-front && pr42 MERGED
scene . 0.8 '# web-front#42 merged and promoted'
scene . 5 'izumo done'

# the root's path, however izumo wrote it, reads as ~/code
paths=$(cd "$code" && { pwd -W 2>/dev/null || true; pwd -P; pwd; })
"$PY" -I - "$(native "$scenes")" "$(native "$OUT")" "$paths" <<'EOF'
import re, sys

scenes, out, paths = sys.argv[1], sys.argv[2], [p for p in sys.argv[3].splitlines() if p]
W, H, X0, Y0, LH, CW, ROWS = 1385, 469, 18, 66, 19, 8.43, 21
FG, DIM, PROMPT = "#e6edf3", "#7d8590", "#7ee787"
SGR = {"31": "#ff7b72", "32": "#7ee787", "33": "#d29922", "34": "#79c0ff", "35": "#d2a8ff", "36": "#76e3ea",
       "38;5;202": "#f0603c", "2": DIM}
WIDE = "⛩"

def clean(s):
    for p in sorted(paths, key=len, reverse=True):
        s = s.replace(p, "~/code").replace(p.replace("/", "\\"), "~/code")
    s = re.sub(r"\x1b\]8;;[^\x1b]*\x1b\\", "", s)           # links: their text stays
    return s.replace("[Y/n] ", "[Y/n] \x00y\n", 1)             # the answer someone types

frames = []  # (lines, seconds); a line is [(text, color, bold)]
screen = []

def frame(extra, secs):
    frames.append(((screen + extra)[-ROWS:], secs))

def prompt(d):
    return [("~/code" if d == "." else "~/code/" + d, PROMPT, False), (" $ ", FG, False)]

for row in open(scenes, encoding="utf-8").read().splitlines():
    d, cmd, path, hold = row.split("\t")
    p = prompt(d)
    frame([p], 0.7)
    for k in range(3, len(cmd) + 3, 3):  # typed, a few characters at a time
        frame([p + [(cmd[:k], FG, False)]], 0.07)
    frame([p + [(cmd, FG, False)]], 0.35)
    screen.append(p + [(cmd, FG, False)])
    text = clean(open(path, encoding="utf-8", errors="replace", newline="").read())  # keep the bar's \r
    line, color, bold, i = [], FG, False, 0
    while i < len(text):
        c = text[i]
        if c == "\x1b":
            m = re.match(r"\x1b\[([0-9;]*)([mK])", text[i:])
            if m and m.group(2) == "K":
                line = []
            elif m:
                code = m.group(1)
                if code in ("", "0"):
                    color, bold = FG, False
                elif code == "1":
                    bold = True
                else:
                    color = SGR.get(code, FG)
            i += len(m.group(0)) if m else 1
            continue
        if c == "\r":  # the bar draws again: each drawing is a frame
            if line:
                frame([line], 0.1)
            line = []
        elif c == "\x00":
            frame([line], 1.1)
        elif c == "\n":
            screen.append(line)
            line = []
        else:
            if line and line[-1][1:] == (color, bold):
                line[-1] = (line[-1][0] + c, color, bold)
            else:
                line.append((c, color, bold))
        i += 1
    if line:
        screen.append(line)
    frame([], float(hold))
frame([prompt(".")], 2.5)

def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

def draw(lines, top):
    out = []
    for r, segs in enumerate(lines):
        y, col = top + Y0 + r * LH, 0
        for text, color, bold in segs:
            for part in re.split("(" + WIDE + ")", text):
                if not part:
                    continue
                x = X0 + col * CW
                w = ' font-weight="bold"' if bold else ""
                if part == WIDE:
                    out.append(f'<text x="{x:.1f}" y="{y}" fill="{color}">{WIDE}</text>')
                    col += 2
                    continue
                if part.strip():
                    out.append(f'<text x="{x:.1f}" y="{y}" xml:space="preserve" textLength="{len(part) * CW:.1f}" '
                               f'lengthAdjust="spacingAndGlyphs" fill="{color}"{w}>{esc(part)}</text>')
                col += len(part)
    return "<g>" + "".join(out) + "</g>"

total = sum(s for _, s in frames)
keys, t = [], 0.0
for i, (_, s) in enumerate(frames):
    keys.append(f"{t / total * 100:.3f}%{{transform:translateY(-{i * H}px)}}")
    t += s
svg = f'''<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" role="img" aria-label="izumo: a task across three repos, from new to done, among two others">
<style>
text {{ font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace; font-size: 14px; }}
.title {{ font-family: -apple-system, "Segoe UI", system-ui, sans-serif; font-size: 13px; fill: #7d8590; }}
#strip {{ animation: play {total:.2f}s steps(1, end) infinite; }}
@keyframes play {{ {" ".join(keys)} }}
</style>
<rect width="{W}" height="{H}" rx="10" fill="#0d1117"/>
<rect x="0.5" y="0.5" width="{W - 1}" height="{H - 1}" rx="10" fill="none" stroke="#30363d"/>
<circle cx="22" cy="17" r="6" fill="#ff5f57"/><circle cx="42" cy="17" r="6" fill="#febc2e"/><circle cx="62" cy="17" r="6" fill="#28c840"/>
<text class="title" x="{W / 2}" y="22" text-anchor="middle">izumo — ~/code</text>
<svg x="0" y="0" width="{W}" height="{H}"><g id="strip">{"".join(draw(l, i * H) for i, (l, _) in enumerate(frames))}</g></svg>
</svg>
'''
open(out, "w", encoding="utf-8", newline="\n").write(svg)
print(f"{out}: {len(frames)} frames, {total:.1f}s, {len(svg) // 1024} KB")
EOF
