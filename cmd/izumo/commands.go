package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

func addRepos(name string, repos []string, out io.Writer) error {
	t, err := loadTask(name)
	if err != nil {
		return err
	}
	if err := notRemoving(name); err != nil {
		return err
	}
	for _, repo := range repos {
		if err := validName(repo); err != nil {
			return err
		}
		src, wt := path.Join(root, repo), path.Join(tasksDir, name, repo)
		if !isDir(path.Join(src, ".git")) {
			return fmt.Errorf("%s: no git clone at %s", repo, src)
		}
		if exists(wt) {
			return fmt.Errorf("%s is already in task '%s'", repo, name)
		}
		fetch(src)
		if hasRef(src, "refs/heads/"+t.branch) || originOf(src).branches[t.branch] {
			// existing branch, local or on origin (git creates the tracking branch)
			err = gitRun(src, "worktree", "add", "--quiet", wt, t.branch)
		} else {
			ref, e := baseRef(src, t.base)
			if e != nil {
				return e
			}
			if t.base != "" && ref != "origin/"+t.base {
				say("%s: no origin/%s, starting from %s", repo, t.base, ref)
			}
			if !gitOK(src, "rev-parse", "--verify", "--quiet", ref+"^{commit}") {
				return fmt.Errorf("%s: no %s", repo, ref)
			}
			err = gitRun(src, "worktree", "add", "--quiet", "--no-track", "-b", t.branch, wt, ref)
		}
		if err != nil {
			return errSaid // git said why
		}
		if err := writeWorkspace(name); err != nil {
			return err
		}
		if hook("post_add", name, repo, out) != nil {
			say("%s: post_add hook failed (worktree kept)", repo)
		}
		fmt.Fprintln(out, wt)
	}
	return nil
}

func cmdNew(args []string) error {
	o, err := parseFlags(args, []string{"--branch", "--base"}, nil)
	if err != nil {
		return err
	}
	if len(o.args) == 0 {
		return errors.New("usage: izumo new <task> [repo...] [--branch B] [--base BRANCH]")
	}
	name, repos, branch, base := o.args[0], o.args[1:], o.values["--branch"], o.values["--base"]
	if err := validName(name); err != nil {
		return err
	}
	if base != "" {
		if err := checkBase(base, repos); err != nil {
			return err
		}
	}
	if branch == "" {
		branch = cfgPrefix + name
	}
	if err := createTask(task{name, branch, base}); err != nil {
		return err
	}
	if len(repos) > 0 {
		if err := addRepos(name, repos, io.Discard); err != nil {
			return err
		}
	}
	fmt.Println(path.Join(tasksDir, name))
	return nil
}

func cmdAdd(args []string) error {
	o, err := parseFlags(args, []string{"--task"}, nil)
	if err != nil {
		return err
	}
	if len(o.args) == 0 {
		return errors.New("usage: izumo add <repo...> [--task T]")
	}
	name, err := taskFromCwd(o.values["--task"])
	if err != nil {
		return err
	}
	return addRepos(name, o.args, os.Stdout)
}

// adopt moves worktrees made by hand (or by another tool) into a task, creating it if needed
func cmdAdopt(args []string) error {
	o, err := parseFlags(args, []string{"--task"}, nil)
	if err != nil {
		return err
	}
	if len(o.args) == 0 {
		return errors.New("usage: izumo adopt <worktree...> [--task T]")
	}
	name, err := taskFromCwd(o.values["--task"])
	if err != nil {
		return err
	}
	if err := validName(name); err != nil {
		return err
	}
	for _, p := range o.args {
		if err := adopt(p, name); err != nil {
			return err
		}
	}
	return nil
}

func adopt(p, name string) error {
	abs, err := realPath(p)
	if err != nil || !isDir(abs) {
		return fmt.Errorf("%s: no such folder", p)
	}
	if !isFile(path.Join(abs, ".git")) {
		return fmt.Errorf("%s: not a git worktree", p)
	}
	if under(cwd(), abs) {
		return fmt.Errorf("%s: you are inside it, cd out first", p)
	}
	common, err := gitOut(abs, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("%s: can't find its repo", p)
	}
	if !isAbs(common) {
		common = path.Join(abs, common)
	}
	src, err := realPath(path.Dir(common))
	if err != nil {
		return fmt.Errorf("%s: can't find its repo", p)
	}
	if normPath(path.Dir(src)) != normPath(root) {
		return fmt.Errorf("%s: its repo %s is not a clone in %s", p, src, root)
	}
	repo := path.Base(src)
	if err := validName(repo); err != nil {
		return err
	}
	from := ""
	if under(abs, tasksDir) {
		from, _, _ = strings.Cut(abs[len(tasksDir)+1:], "/")
		if err := notRemoving(from); err != nil {
			return err
		}
	}
	if isFile(path.Join(tasksDir, name, ".task")) {
		if err := notRemoving(name); err != nil {
			return err
		}
	} else {
		br, _ := gitOut(abs, "branch", "--show-current")
		if br == "" {
			br = cfgPrefix + name
		}
		if err := createTask(task{name, br, ""}); err != nil {
			return err
		}
	}
	dest := path.Join(tasksDir, name, repo)
	if exists(dest) {
		return fmt.Errorf("%s is already in task '%s'", repo, name)
	}
	if gitRun(src, "worktree", "move", abs, dest) != nil {
		return fmt.Errorf("%s: git could not move it (locked or has submodules?)", p)
	}
	if err := writeWorkspace(name); err != nil {
		return err
	}
	// it came from another task: that one's workspace must stop listing it
	if from != "" && isFile(path.Join(tasksDir, from, ".task")) {
		if err := writeWorkspace(from); err != nil {
			return err
		}
	}
	fmt.Println(dest)
	return nil
}

// mv renames a task; branches keep their names, since they may have PRs open
func cmdMv(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: izumo mv <task> <new-name>")
	}
	old, name := args[0], args[1]
	if _, err := loadTask(old); err != nil {
		return err
	}
	if err := validName(name); err != nil {
		return err
	}
	dir, newDir := path.Join(tasksDir, old), path.Join(tasksDir, name)
	if exists(newDir) {
		return fmt.Errorf("task '%s' already exists", name)
	}
	if err := notRemoving(old); err != nil {
		return err
	}
	if under(cwd(), dir) {
		return fmt.Errorf("%s: you are inside it, cd out first", old)
	}
	if busy, err := inUse(dir); err != nil || busy {
		return keptErr(old, inUseMsg, err)
	}
	if os.Rename(dir, newDir) != nil {
		return fmt.Errorf("%s: could not rename the folder", old)
	}
	// the worktrees moved under git's feet; repair points each repo at the new path
	for _, r := range worktrees(name) {
		if !gitOK(path.Join(root, r), "worktree", "repair", path.Join(newDir, r)) {
			return fmt.Errorf("%s: git lost track of the worktree; run: git -C '%s' worktree repair '%s'", r, path.Join(root, r), path.Join(newDir, r))
		}
	}
	os.Remove(path.Join(newDir, old+".code-workspace"))
	if err := writeWorkspace(name); err != nil {
		return err
	}
	// the default TASK.md and most templates start with the task name as heading
	md := path.Join(newDir, "TASK.md")
	if b, err := os.ReadFile(md); err == nil {
		if first, rest, _ := strings.Cut(string(b), "\n"); first == "# "+old {
			_ = os.WriteFile(md, []byte("# "+name+"\n"+rest), 0o644)
		}
	}
	fmt.Println(newDir)
	return nil
}

// keptErr: "<name>: kept (<why>)", or the error that stopped the check
func keptErr(name, why string, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s: kept (%s)", name, why)
}

// base changes the branch a task's new repos start from and its stages compare against.
// Existing worktrees stay where they are: rebasing them is git's job.
func cmdBase(args []string) error {
	o, err := parseFlags(args, []string{"--task"}, nil)
	if err != nil {
		return err
	}
	if len(o.args) != 1 {
		return errors.New("usage: izumo base <branch> [--task T]")
	}
	branch := o.args[0]
	name, err := taskFromCwd(o.values["--task"])
	if err != nil {
		return err
	}
	t, err := loadTask(name)
	if err != nil {
		return err
	}
	if !gitOK(root, "check-ref-format", "--branch", branch) {
		return fmt.Errorf("invalid branch name: %s", branch)
	}
	if err := checkBase(branch, worktrees(t.name)); err != nil {
		return err
	}
	t.base = branch
	if err := saveTask(t); err != nil {
		return err
	}
	for _, r := range worktrees(t.name) {
		if !onOrigin(path.Join(root, r), branch) {
			ref, _ := baseRef(path.Join(root, r), branch)
			say("%s: no origin/%s, uses %s", r, branch, ref)
		}
	}
	fmt.Printf("%s: base %s\n", t.name, branch)
	return nil
}

// run: one command in each repo of a task, in order; fails if it failed in any.
// Its own flags come first: everything after them is the command's.
func cmdRun(args []string) error {
	given := ""
	for len(args) > 0 {
		if args[0] == "--task" {
			if len(args) < 2 {
				return errors.New("--task needs a value")
			}
			given, args = args[1], args[2:]
			continue
		}
		if args[0] == "--" {
			args = args[1:]
		}
		break
	}
	if len(args) == 0 {
		return errors.New("usage: izumo run [--task T] [--] <command...>")
	}
	name, err := taskFromCwd(given)
	if err != nil {
		return err
	}
	t, err := loadTask(name)
	if err != nil {
		return err
	}
	var failed []string
	for _, r := range worktrees(t.name) {
		fmt.Println("== " + r)
		if inWt(t.name, r, os.Stdout, args...) != nil {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed in: %s", strings.Join(failed, " "))
	}
	return nil
}

// pullOne: fetch, then fast-forward the clone's branch when nothing local is in the way.
// Untracked files (.env and such) don't count: a fast-forward never touches them.
func pullOne(repo string) string {
	src := path.Join(root, repo)
	if !gitOK(src, "fetch", "--quiet", "--prune", "origin") {
		return "fetch failed"
	}
	br, _ := gitOut(src, "branch", "--show-current")
	if br == "" {
		return "fetched (no branch checked out)"
	}
	up, err := gitOut(src, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return fmt.Sprintf("fetched (on %s, which has no upstream)", br)
	}
	head, _ := gitOut(src, "rev-parse", "HEAD")
	if upHead, _ := gitOut(src, "rev-parse", up); head == upHead {
		return "up to date"
	}
	if isAncestor(src, up, "HEAD") {
		return fmt.Sprintf("fetched (%s is ahead of %s)", br, up)
	}
	if !isAncestor(src, "HEAD", up) {
		return fmt.Sprintf("fetched (%s has diverged from %s)", br, up)
	}
	if st, _ := gitOut(src, "status", "--porcelain", "--untracked-files=no"); st != "" {
		return fmt.Sprintf("fetched (%s has local changes)", br)
	}
	if !gitOK(src, "merge", "--ff-only", "--quiet", up) {
		return fmt.Sprintf("fetched (could not fast-forward %s)", br)
	}
	return "updated " + br
}

// pull brings the root's clones up to date, so code read there is current
func cmdPull(repos []string) error {
	for _, r := range repos {
		if err := validName(r); err != nil {
			return err
		}
		if !isDir(path.Join(root, r, ".git")) {
			return fmt.Errorf("%s: no git clone at %s", r, path.Join(root, r))
		}
	}
	if len(repos) == 0 {
		repos = allClones()
	}
	res := parallel(len(repos), func(i int) string { return pullOne(repos[i]) })
	w, failed := 0, false
	for _, r := range repos {
		w = max(w, len(r))
	}
	for i, r := range repos {
		fmt.Printf("%-*s  %s\n", w, r, res[i])
		failed = failed || res[i] == "fetch failed"
	}
	if failed {
		return errSaid
	}
	return nil
}
