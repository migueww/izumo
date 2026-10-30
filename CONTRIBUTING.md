# Contributing to izumo

izumo grows from real use: a need shows up in someone's day, it becomes an issue, and the
smallest change that covers it lands. Bug reports, ideas and pull requests are all welcome.

## Before you start

- **A bug?** Open an issue with what you ran, what you expected and what happened. The
  output of `izumo ls --json` and your OS help a lot.
- **A new command or flag?** Open an issue first, so we agree on the behavior before you
  write it. izumo stays small on purpose: it organizes work across repos, and leaves
  running agents, opening PRs, starting servers and rebasing to the tools that already do
  that well (see "Not doing" in the README).
- **Docs, typos, a clearer error message?** Just send the PR.

## Build and test

You need Go (the version in `go.mod`), git ≥ 2.29, bash and Python 3. On Windows, use
Git Bash.

```sh
go vet ./... && go test ./...                         # unit tests: the pure logic
go build -o /tmp/izumo ./cmd/izumo
IZUMO_BIN=/tmp/izumo bash test/test.sh                # end to end: prints "ok"
```

`test/test.sh` is the spec. It builds real repos in a temp folder, fakes `gh`, runs every
command and checks what it prints and what it leaves on disk. It takes under a minute on
Linux and macOS, a couple on Windows. CI runs it on all three for every push and PR.

On Windows with Smart App Control on, a binary you just built may be blocked. Push a
branch and let CI test it.

## Where things are

```
cmd/izumo/      the CLI: one package, one file per concern
  main.go       dispatch, flags, paths        task.go    .task, workspace, task files
  config.go     .izumo, bash, hooks           commands.go new add adopt mv base run pull
  git.go        git, origin, stages, PRs      ls.go       ls and the agent hook
  remove.go     done, rm, drop                servers.go  dev servers on a task's ports
  color*.go     ls colors, on terminals only
bin/izumo       the shim: downloads the release binary, checks it, runs it
skills/izumo/   the skill that teaches agents to use izumo
hooks/          Claude Code hooks: what each task's agent is doing
test/test.sh    the end-to-end spec
```

## What a change needs

- **A case in `test/test.sh`** for any behavior you add or fix. A bug fix comes with the
  test that would have caught it.
- **Never lose work.** Anything that removes must refuse when it would lose uncommitted
  changes, unpushed commits or files izumo didn't create, unless the user said
  `rm --discard`. When in doubt, keep the task and say why.
- **Keep the output stable.** People and agents read `izumo ls` and parse
  `izumo ls --json`. Adding a field is fine; renaming or removing one breaks someone.
- **Keep `.izumo` working.** It is bash, and existing roots rely on it.
- **Errors say what to do next**, in one line: `kept (a has 1 unpushed commit(s))`, not
  a stack trace.
- **Update the README and the skill** when a command changes.
- `gofmt`, `go vet` and `shellcheck bin/izumo test/test.sh` pass.

Keep pull requests small, one change each, and say in the description why it is needed.

## The `bash` branch

The [`bash` branch](https://github.com/migueww/izumo/tree/bash) keeps the bash version
for Windows machines whose Smart App Control blocks the unsigned binary. It gets bug fixes
only, no features. A fix to izumo's logic (what counts as shipped, what may be removed)
goes to both branches; a fix to Go code alone stays on `main`.

## Releases

The maintainer releases: bump `VERSION` in `bin/izumo` on `main`, then push a `v<version>`
tag. The release workflow checks that the two match, builds the binaries for Linux, macOS
and Windows, attests them and publishes the release.

## License

By contributing, you agree that your contribution is licensed under the MIT License, like
the rest of izumo.
