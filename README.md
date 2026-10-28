# izumo

**One folder per task, one git worktree per repo.** For people (and agents) who work
across many repos at once, and whose tasks grow mid-way: you start in two services,
find out a third needs a change, and finish in a fifth.

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

```sh
curl -fsSL https://raw.githubusercontent.com/migueww/izumo/main/bin/izumo -o ~/.local/bin/izumo
chmod +x ~/.local/bin/izumo
```

Needs bash and git ≥ 2.22. Optional: [`gh`](https://cli.github.com), for PR state and
squash-merge detection. On Windows it runs in Git Bash.

## Use

```sh
cd ~/code && touch .izumo                     # mark the multi-repo root once

izumo new rollout-flag billing-service web-front
cd tasks/rollout-flag/web-front
izumo add infra                               # the task grew; task inferred from cwd
izumo ls                                      # every task, repo, branch and stage
izumo done --dry-run && izumo done            # remove what has shipped
```

| Command | Does |
|---|---|
| `new <task> [repo...] [--branch B] [--base BRANCH]` | Creates `tasks/<task>/` with a worktree per repo, `TASK.md`, `AGENTS.md`, `CLAUDE.md` and a VS Code workspace. Uses `B` if it already exists locally or on origin, else creates it from the base. |
| `add <repo...> [--task T]` | Adds repos to a task. |
| `ls [task...] [--json]` | Stage per repo: `empty` (no commits yet), `wip`, `pr` (open), `base` (merged, not shipped), `prod`. Plus `dirty` and unpushed commits. Reads the last fetch; it doesn't fetch. |
| `done [task...] [--dry-run]` | Fetches, then removes tasks whose repos are all in `prod` or `empty` (at least one in `prod`). No args: sweeps all tasks. |
| `rm <task...> [--dry-run]` | Removes a task even if it never shipped: a review, an abandoned idea. |

Neither `done` nor `rm` removes a task that has uncommitted changes, unpushed commits or
files it didn't create. "Unpushed" means the commit is on no remote branch and is not
the head of a merged PR. Without `gh`, squash-merged work whose remote branch was
deleted looks unpushed, and izumo keeps it.

## Configure

`.izumo` is sourced as bash, so trust it the way you trust a Makefile.

```sh
BASE=develop        # branch new work starts from (default: origin's default branch)
PROD=main           # branch that means "shipped" (default: BASE)
PREFIX=feat/        # prefix for branches izumo names (default: none)

post_add() {        # runs inside each new worktree; TASK REPO SRC WT BRANCH are set
  cp "$SRC/.env" . 2>/dev/null || true
  if [ -f pnpm-lock.yaml ]; then pnpm install; fi
}
```

Running two tasks' services at once? Use `$TASK` in `post_add` to set
`COMPOSE_PROJECT_NAME` or pick ports.

## With agents

The task folder is the context: start your agent in `tasks/<task>/` and it sees only
the repos that matter. `AGENTS.md` and `CLAUDE.md` point it to `TASK.md`, so a new
session picks up where the last one stopped. Agents read state with `izumo ls --json`.
Nothing in izumo prompts, so agents can run every command.

Keep `TASK.md` to what git can't tell: goal, status / next step, decisions, open items.
Repos, branches and PRs come from `ls`. `TASK.md` lives only on your machine, so move
decisions others need into the PR description.

A skill for Claude Code and other agents that read `SKILL.md` is in
[`skills/izumo`](skills/izumo/SKILL.md).

## Not doing

izumo doesn't run or orchestrate agents, clone or update your repos, open PRs, manage
ports or rebase branches. Your other tools do that, and izumo stays small enough to read
in one sitting.

## Why "izumo"

Every year, in the tenth month of the old calendar, the gods of Japan leave their
shrines and travel to the Grand Shrine of Izumo. There they meet to decide the
*en-musubi*, the ties that will bind people and fates in the year ahead. When the
gathering ends, each god goes home. A task works the same way: the repos come together
in one place to settle how they connect, then return to their own clones.

## License

MIT
