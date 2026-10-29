---
name: izumo
description: Work on multi-repo tasks with the izumo CLI, one folder per task with one git worktree per repo. Use when the current directory, or a parent, has a .izumo file and the user starts, resumes, extends, checks or finishes a piece of work ("start task X in A and B", "this also needs repo C", "how are my tasks?", "clean up what shipped").
---

# izumo

Installed as the Claude Code plugin, `izumo` is already on your Bash PATH. Otherwise it
must be on PATH; if it is missing, point the user to https://github.com/migueww/izumo#install.

## Rules

- **Never edit the source clones** at the root. All work happens in `tasks/<task>/<repo>`.
  Before reading code in a root clone (another service's contract, a grep across repos),
  run `izumo pull <repo>` so you don't read stale code; if it says the clone is on some
  other branch, tell the user what you read may not be the base branch.
- **Starting work:** `izumo new <task> <repos...>`, with `--branch` when the user or the
  team convention names the branch. Write the user's request into the first section of
  `TASK.md` (the goal; the root's template may name its sections in another language)
  right away.
- **Existing worktree made by hand?** `izumo adopt <path> --task <task>` moves it into a
  task (creating the task if needed) instead of starting over.
- **Scope grows:** `izumo add <repo>` from inside the task folder, then add one line under
  Decisions saying why the repo joined.
- **Scope shrinks:** `izumo drop <repo>` from inside the task folder, never `git worktree
  remove` or hand edits to the workspace file. Add one line under Decisions saying why the
  repo left. A repo that belongs to another task moves with `izumo adopt
  tasks/<from>/<repo> --task <to>`.
- **Renaming a task or changing its base:** `izumo mv <task> <new-name>`, `izumo base
  <branch>`. Never rename task folders or edit `.task` by hand: git tracks the worktree
  paths.
- **TASK.md holds only what git can't tell:** goal, status / next step, decisions, open
  items. Never list repos, branches or PRs there; `izumo ls <task> --json` has them.
- **Before ending a session**, update "Status / next step" with the next thing a person
  does ("test a handover in hml", "decide the template item"), never PR or merge state:
  that changes after your session ends, and `izumo ls` always has it right.
- **Same command in every repo** (tests, lint, `git status`): `izumo run -- <command>` from
  inside the task folder, instead of a loop. It fails if any repo failed and names them.
- **State:** `izumo ls <task> --json`. Add `--fetch` when freshness matters (after a
  merge or promotion). The `agent` field comes from this plugin's hooks; don't write
  `.agent` yourself.
- **Removing:** only when the user asks. Name the task: `izumo done <task> --dry-run` (or
  `rm <task> --dry-run`) first, show the result, then run it for real. Bare `izumo done`
  sweeps every task; use it only when the user asks to clean up everything that shipped. Never work around a refusal
  by committing, pushing or deleting files on your own; report what blocked it. Use `rm
  --discard` only when the user said to throw that work away; run it with `--dry-run`
  first and show what would be lost. A
  removal that stopped halfway finishes by running the same command again.
- `TASK.md` is local. When opening a PR, carry the decisions reviewers need into its
  description.
