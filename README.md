<p align="center"><img src="docs/icon.svg" width="112" alt=""></p>

<h1 align="center">izumo</h1>

**One folder per task, one git worktree per repo.** For people (and agents) who work
across many repos at once, and whose tasks grow mid-way: you start in two services,
find out a third needs a change, and finish in a fifth.

![A task starts in billing-service and web-front, takes in infra, and is removed only once web-front ships](docs/demo.svg)

```
root/                     ← your clones: fetch only, nobody works here
├── .izumo
├── billing-service/
├── web-front/
└── tasks/
    └── rollout-flag/     ← one task: everything it touches, nothing else
        ├── TASK.md       ← goal, status, decisions (what git can't tell)
        ├── rollout-flag.code-workspace
        ├── billing-service/   (worktree, branch rollout-flag)
        └── web-front/         (worktree, branch rollout-flag)
```

When every repo of the task has reached production, `izumo done` archives `TASK.md`
and removes the worktrees and branches. It refuses if any work would be lost.

## Install

**Claude Code:** the plugin puts `izumo` on Claude's PATH and adds the skill that teaches
it. Run this in a session:

```
/plugin install izumo --marketplace migueww/izumo
```

On Claude Code older than 2.1.275, run `/plugin marketplace add migueww/izumo`, then
`/plugin install izumo@izumo`. Updates from third-party marketplaces are off by
default: turn them on in `/plugin` → Marketplaces, or run
`/plugin marketplace update izumo`.

**Your own terminal** (also what other agents use): download the binary for your system
from the [latest release](https://github.com/migueww/izumo/releases/latest), rename it to
`izumo` (`izumo.exe` on Windows) and put it on your PATH. Or, with Go:

```sh
go install github.com/migueww/izumo/cmd/izumo@latest
```

Needs git ≥ 2.29, and bash, which reads `.izumo` (on Windows, Git for Windows' bash).
Optional: [`gh`](https://cli.github.com), for PR state and squash-merge detection.

The plugin's `bin/izumo` is a small script that downloads the release binary on first
use, checks it against the release's checksums and keeps it in `~/.cache/izumo`. With a
logged-in `gh` it also checks the binary's build attestation, the proof that this repo's
release workflow built it; `gh attestation verify <file> --repo migueww/izumo` does the
same for a binary you downloaded by hand.

**Windows with Smart App Control on** blocks the binary, which isn't signed yet. Until it
is, use the bash version from the [`bash` branch](https://github.com/migueww/izumo/tree/bash),
which gets bug fixes:

```sh
curl -fsSL https://raw.githubusercontent.com/migueww/izumo/bash/bin/izumo -o ~/.local/bin/izumo
```

## Use

```sh
cd ~/code && touch .izumo                     # mark the multi-repo root once

izumo new rollout-flag billing-service web-front
cd tasks/rollout-flag/web-front
izumo add infra                               # the task grew; task inferred from cwd
izumo                                         # same as izumo ls: every task, repo, branch and stage
izumo done --dry-run && izumo done            # remove what has shipped
```

| Command | Does |
|---|---|
| `new <task> [repo...] [--branch B] [--base BRANCH]` | Creates `tasks/<task>/` with a worktree per repo, `TASK.md`, `AGENTS.md`, `CLAUDE.md` and a VS Code workspace. Uses `B` if it already exists locally or on origin, else creates it from the base. `--base` can be another task's pushed branch, to stack on it; repos without it start from their default branch and say so. |
| `add <repo...> [--task T] [--ref]` | Adds repos to a task. `--ref` adds one the task only reads (the back end whose payload the front needs): detached on the task's base, no branch, no `post_add`, and `done` doesn't wait for it. Adding it again without `--ref` turns it into one the task works on. |
| `drop <repo...> [--task T] [--stop\|--no-stop]` | Takes repos out of a task: removes the worktree and the local branch. Refuses if work would be lost. |
| `adopt <worktree...> [--task T]` | Moves worktrees you made by hand, or a repo from another task, into a task. Creates the task if needed, on the first worktree's branch. |
| `mv <task> <new-name>` | Renames a task. Branches keep their names, since they may have PRs open. |
| `base <branch> [--task T]` | Changes the branch a task is based on: repos added later start from it, and stages compare against it. Existing worktrees aren't rebased. |
| `ls [task...] [--json] [--fetch]` | Stage per repo: `empty` (no commits yet), `wip`, `pr` (open, with review decision and CI: `approved`, `changes-requested`, `ci:failing`...), `base` (merged, not shipped), `prod`, or `ref` for a repo only read (with `3-behind` when its base moved on). Plus modified and untracked files, unpushed commits, the ports its dev servers listen on (`serving:3000`), what the task's agent is doing, and `[ready: izumo done]` when `done` would take the task. Reads the last fetch unless you pass `--fetch`. Plain `izumo` runs it. |
| `done [task...] [--dry-run] [--stop\|--no-stop]` | Fetches, then removes tasks whose repos are all in `prod` or `empty` (at least one in `prod`). No args: sweeps all tasks. |
| `rm <task...> [--dry-run] [--discard] [--stop\|--no-stop]` | Removes a task even if it never shipped: a review, an abandoned idea. `--discard` removes it even when that loses uncommitted changes, unpushed commits or unknown files; `--dry-run --discard` lists exactly what would be lost. |
| `pull [repo...]` | Fetches the root's clones (pruning deleted branches) and fast-forwards the ones on a clean branch, so code you or an agent reads there is current, and moves the tasks' `--ref` repos to their base's tip. Clones with local commits or changes are only fetched, a ref someone edited stays put, and it says why. |
| `run [--task T] [--] <command...>` | Runs a command in each repo the task works on (not its refs), in order, with the hook variables set (`izumo run -- pnpm test`). Fails if it failed in any repo. |

Neither `done`, `rm` nor `drop` removes anything that has uncommitted changes, unpushed commits or
files it didn't create, unless you say `rm --discard`. "Unpushed" means the commit is on no remote branch and is not
the head of a merged PR. Without `gh`, squash-merged work whose remote branch was
deleted looks unpushed, and izumo keeps it. On Windows it also keeps a task whose folder
a program has open (an editor, a terminal), since Windows would stop the removal halfway.
If a removal stops halfway anyway, `ls` flags the task and running the same command
again finishes it. Files git ignores (`.env`, `node_modules`) go with the worktree:
keep anything you need elsewhere, or copy it out in `pre_remove`.

A dev server left running from the task (`pnpm dev` on :3000) is no reason to keep it.
`done`, `rm` and `drop` name the servers listening on a port from the worktrees they
remove and ask before stopping them; `--stop` or `--no-stop` answers ahead, and without
a terminal (an agent) one of them is required. izumo finds a server by its folder, or by
the worktree's path in its command line, as when it runs from `node_modules`.

## Configure

`.izumo` is sourced as bash, so trust it the way you trust a Makefile.

```sh
BASE=develop        # branch new work starts from (default: origin's default branch)
PROD=main           # branch that means "shipped" (default: BASE)
                    # a repo without BASE or PROD on origin uses its default branch instead
PREFIX=feat/        # prefix for branches izumo names (default: none)
TASK_TEMPLATE=.izumo-task.md   # TASK.md template, relative to the root; {{task}} is the
                               # task name and the first section is the goal ls shows

post_add() {        # runs inside each new worktree; TASK REPO SRC WT BRANCH are set
  cp "$SRC/.env" . 2>/dev/null || true
  if [ -f pnpm-lock.yaml ]; then pnpm install; fi
}

pre_remove() {      # runs inside each worktree before done, rm or drop removes it;
  docker compose -p "$TASK-$REPO" down -v 2>/dev/null || true
}                   # failing, or leaving changes behind, keeps the task
```

Running two tasks' services at once? Use `$TASK` in `post_add` to set
`COMPOSE_PROJECT_NAME` or pick ports, and tear them down in `pre_remove`.

## With agents

The task folder is the context: start your agent in `tasks/<task>/` and it sees only
the repos that matter. `AGENTS.md` and `CLAUDE.md` point it to `TASK.md`, so a new
session picks up where the last one stopped. Agents read state with `izumo ls --json`.
izumo asks only on a terminal (before stopping dev servers, which a flag answers), so
agents can run every command.

Keep `TASK.md` to what git can't tell: goal, status / next step, decisions, open items.
Repos, branches and PRs come from `ls`. `TASK.md` lives only on your machine, so move
decisions others need into the PR description.

The Claude Code plugin ships the skill in [`skills/izumo`](skills/izumo/SKILL.md). Other
agents that read `SKILL.md` can copy that folder into their own skills directory.

Running several agents? The plugin's hooks record what the agent in each task is doing
in `tasks/<task>/.agent`, and `ls` shows it: `working` after you send a prompt,
`waiting` when it asks for a permission or an answer, `done` when its turn ends. The
file goes away when the session ends. The hooks fire only on those events, never per
tool call, so they cost nothing noticeable. A permission you just approved still
shows `waiting` until the turn ends.

## Not doing

izumo doesn't run or orchestrate agents, clone your repos, open PRs, start servers,
assign ports or rebase branches. Your other tools do that.

## Why "izumo"

Every year, in the tenth month of the old calendar, the gods of Japan leave their
shrines and travel to the Grand Shrine of Izumo. There they meet to decide the
*en-musubi*, the ties that will bind people and fates in the year ahead. When the
gathering ends, each god goes home. A task works the same way: the repos come together
in one place to settle how they connect, then return to their own clones.

## License

MIT
