---
name: izumo
description: Work on multi-repo tasks with the izumo CLI, one folder per task with one git worktree per repo. Use when the current directory, or a parent, has a .izumo file and the user starts, resumes, extends, checks or finishes a piece of work ("start task X in A and B", "this also needs repo C", "how are my tasks?", "clean up what shipped").
---

# izumo

Installed as the Claude Code plugin, `izumo` is already on your Bash PATH. Otherwise it
must be on PATH; if it is missing, point the user to https://github.com/migueww/izumo#install.

## Rules

- **Never edit the source clones** at the root. All work happens in `tasks/<task>/<repo>`.
- **Starting work:** `izumo new <task> <repos...>`, with `--branch` when the user or the
  team convention names the branch. Write the user's request into the first section of
  `TASK.md` (the goal; the root's template may name its sections in another language)
  right away.
- **Existing worktree made by hand?** `izumo adopt <path> --task <task>` moves it into a
  task (creating the task if needed) instead of starting over.
- **Scope grows:** `izumo add <repo>` from inside the task folder, then add one line under
  Decisions saying why the repo joined.
- **TASK.md holds only what git can't tell:** goal, status / next step, decisions, open
  items. Never list repos, branches or PRs there; `izumo ls <task> --json` has them.
- **Before ending a session**, update "Status / next step" so the next session, or the
  user reading it in the morning, knows where things stand.
- **State:** `izumo ls <task> --json`. Add `--fetch` when freshness matters (after a
  merge or promotion). The `agent` field comes from this plugin's hooks; don't write
  `.agent` yourself.
- **Removing:** only when the user asks. Run `izumo done --dry-run` (or `rm <task>
  --dry-run`) first, show the result, then run it for real. Never work around a refusal
  by committing, pushing or deleting files on your own; report what blocked it. A
  removal that stopped halfway finishes by running the same command again.
- `TASK.md` is local. When opening a PR, carry the decisions reviewers need into its
  description.
