package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

type task struct{ name, branch, base string }

func loadTask(name string) task {
	validName(name)
	b, err := os.ReadFile(path.Join(tasksDir, name, ".task"))
	if err != nil {
		die("no task '%s'", name)
	}
	t := task{name: name}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(l, "\r"), "=")
		switch k {
		case "T_BRANCH":
			t.branch = unquote(v)
		case "T_BASE":
			t.base = unquote(v)
		}
	}
	return t
}

// .task holds shell assignments (the bash izumo sourced it): single quotes or bash's %q
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

var plain = regexp.MustCompile(`^[A-Za-z0-9._/@+,:=-]+$`)

func shellQuote(v string) string {
	if plain.MatchString(v) {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func saveTask(t task) {
	s := fmt.Sprintf("T_BRANCH=%s\nT_BASE=%s\n", shellQuote(t.branch), shellQuote(t.base))
	if err := os.WriteFile(path.Join(tasksDir, t.name, ".task"), []byte(s), 0o644); err != nil {
		die("could not write %s/.task: %v", t.name, err)
	}
}

func cwdTask() (string, bool) {
	p := cwd()
	if !under(p, tasksDir) || len(p) <= len(tasksDir)+1 {
		return "", false
	}
	t, _, _ := strings.Cut(p[len(tasksDir)+1:], "/")
	return t, true
}

func taskFromCwd(given string) string {
	if given != "" {
		return given
	}
	t, ok := cwdTask()
	if !ok {
		die("not inside a task folder; pass --task")
	}
	return t
}

// subdirs: folders in dir, hidden ones left out, sorted
func subdirs(dir string) []string {
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

func allTasks() []string {
	var out []string
	for _, d := range subdirs(tasksDir) {
		if isFile(path.Join(tasksDir, d, ".task")) {
			out = append(out, d)
		}
	}
	return out
}

// the root's clones: folders with a .git directory and a name izumo accepts
func allClones() []string {
	var out []string
	for _, d := range subdirs(root) {
		if okName.MatchString(d) && isDir(path.Join(root, d, ".git")) {
			out = append(out, d)
		}
	}
	return out
}

var okName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func worktrees(t string) []string {
	var out []string
	for _, d := range subdirs(path.Join(tasksDir, t)) {
		if isFile(path.Join(tasksDir, t, d, ".git")) {
			out = append(out, d)
		}
	}
	return out
}

func jsonStr(s string) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(strings.ReplaceAll(s, "\r", ""))
	return strings.TrimSuffix(b.String(), "\n")
}

func writeWorkspace(t string) {
	var b strings.Builder
	b.WriteString("{\n  \"folders\": [")
	sep := ""
	for _, r := range worktrees(t) {
		br, _ := gitOut(path.Join(tasksDir, t, r), "branch", "--show-current")
		fmt.Fprintf(&b, "%s\n    { \"name\": %s, \"path\": %s }", sep, jsonStr(r+" ("+br+")"), jsonStr(r))
		sep = ","
	}
	b.WriteString("\n  ]\n}\n")
	if err := os.WriteFile(path.Join(tasksDir, t, t+".code-workspace"), []byte(b.String()), 0o644); err != nil {
		die("could not write the workspace of %s: %v", t, err)
	}
}

const agentsMD = "This folder is an izumo task: each subfolder is a git worktree of one repo, all on\n" +
	"the task's branch. Read TASK.md first and keep it current. It holds only what git\n" +
	"can't tell: goal, status / next step, decisions and open items. Repos, branches and\n" +
	"PR state come from `izumo ls <task>`, so never write them here: the next step is the\n" +
	"next thing a person does. Need another repo? Run `izumo add <repo>` from here;\n" +
	"one no longer needed? `izumo drop <repo>`.\n"

func writeTaskFiles(t string) {
	dir := path.Join(tasksDir, t)
	md := fmt.Sprintf("# %s\n\n## Goal\n\n## Status / next step\n\n## Decisions\n\n## Open items\n", t)
	if cfgTemplate != "" {
		tpl, err := os.ReadFile(cfgTemplate)
		if err != nil {
			die("TASK_TEMPLATE not found: %s", cfgTemplate)
		}
		md = strings.ReplaceAll(string(tpl), "{{task}}", t)
	}
	for name, s := range map[string]string{"TASK.md": md, "AGENTS.md": agentsMD, "CLAUDE.md": "@AGENTS.md\n@TASK.md\n"} {
		if err := os.WriteFile(path.Join(dir, name), []byte(s), 0o644); err != nil {
			die("could not write %s/%s: %v", t, name, err)
		}
	}
}

func createTask(t task) {
	if exists(path.Join(tasksDir, t.name)) {
		die("task '%s' already exists", t.name)
	}
	if cfgTemplate != "" && !isFile(cfgTemplate) {
		die("TASK_TEMPLATE not found: %s", cfgTemplate)
	}
	if !gitOK(root, "check-ref-format", "--branch", t.branch) {
		die("invalid branch name: %s", t.branch)
	}
	if err := os.MkdirAll(path.Join(tasksDir, t.name), 0o755); err != nil {
		die("could not create the task folder: %v", err)
	}
	saveTask(t)
	writeTaskFiles(t.name)
	writeWorkspace(t.name)
}

// notRemoving: a removal that stopped halfway lists what it removes; changing the task before it
// finishes would leave the change out of that list
func notRemoving(t string) {
	if exists(path.Join(tasksDir, t, ".removing")) {
		die("%s: a removal stopped halfway; finish it with izumo rm %s", t, t)
	}
}

func addRepos(name string, repos []string, out io.Writer) {
	t := loadTask(name)
	notRemoving(name)
	for _, repo := range repos {
		validName(repo)
		src, wt := path.Join(root, repo), path.Join(tasksDir, name, repo)
		if !isDir(path.Join(src, ".git")) {
			die("%s: no git clone at %s", repo, src)
		}
		if exists(wt) {
			die("%s is already in task '%s'", repo, name)
		}
		fetch(src)
		var err error
		if hasRef(src, "refs/heads/"+t.branch) || hasRef(src, "refs/remotes/origin/"+t.branch) {
			// existing branch, local or on origin (git creates the tracking branch)
			err = gitRun(src, "worktree", "add", "--quiet", wt, t.branch)
		} else {
			ref, e := baseRef(src, t.base)
			if e != nil {
				die("%v", e)
			}
			if t.base != "" && ref != "origin/"+t.base {
				say("%s: no origin/%s, starting from %s", repo, t.base, ref)
			}
			if !gitOK(src, "rev-parse", "--verify", "--quiet", ref+"^{commit}") {
				die("%s: no %s", repo, ref)
			}
			err = gitRun(src, "worktree", "add", "--quiet", "--no-track", "-b", t.branch, wt, ref)
		}
		if err != nil {
			os.Exit(1)
		}
		writeWorkspace(name)
		if hook("post_add", name, repo, out) != nil {
			say("%s: post_add hook failed (worktree kept)", repo)
		}
		fmt.Fprintln(out, wt)
	}
}

func cmdNew(args []string) {
	var name, branch, baseB string
	var repos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--branch":
			branch = flagValue(args, i)
			i++
		case a == "--base":
			baseB = flagValue(args, i)
			i++
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		case name == "":
			name = a
		default:
			repos = append(repos, a)
		}
	}
	if name == "" {
		die("usage: izumo new <task> [repo...] [--branch B] [--base BRANCH]")
	}
	validName(name)
	if baseB != "" {
		checkBase(baseB, repos)
	}
	if branch == "" {
		branch = cfgPrefix + name
	}
	createTask(task{name, branch, baseB})
	if len(repos) > 0 {
		addRepos(name, repos, io.Discard)
	}
	fmt.Println(path.Join(tasksDir, name))
}

func cmdAdd(args []string) {
	var name string
	var repos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--task":
			name = flagValue(args, i)
			i++
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		default:
			repos = append(repos, a)
		}
	}
	if len(repos) == 0 {
		die("usage: izumo add <repo...> [--task T]")
	}
	addRepos(taskFromCwd(name), repos, os.Stdout)
}

// adopt moves worktrees made by hand (or by another tool) into a task, creating it if needed
func cmdAdopt(args []string) {
	var name string
	var paths []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--task":
			name = flagValue(args, i)
			i++
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		default:
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 {
		die("usage: izumo adopt <worktree...> [--task T]")
	}
	name = taskFromCwd(name)
	validName(name)
	for _, p := range paths {
		abs, err := realPath(p)
		if err != nil || !isDir(abs) {
			die("%s: no such folder", p)
		}
		if !isFile(path.Join(abs, ".git")) {
			die("%s: not a git worktree", p)
		}
		if under(cwd(), abs) {
			die("%s: you are inside it, cd out first", p)
		}
		common, err := gitOut(abs, "rev-parse", "--git-common-dir")
		if err != nil {
			die("%s: can't find its repo", p)
		}
		if !strings.HasPrefix(common, "/") && !(len(common) > 1 && common[1] == ':') {
			common = path.Join(abs, common)
		}
		src, err := realPath(path.Dir(common))
		if err != nil {
			die("%s: can't find its repo", p)
		}
		if !strings.EqualFold(path.Dir(src), root) {
			die("%s: its repo %s is not a clone in %s", p, src, root)
		}
		repo := path.Base(src)
		validName(repo)
		from := ""
		if under(abs, tasksDir) {
			from, _, _ = strings.Cut(abs[len(tasksDir)+1:], "/")
			notRemoving(from)
		}
		if isFile(path.Join(tasksDir, name, ".task")) {
			notRemoving(name)
		}
		if !isFile(path.Join(tasksDir, name, ".task")) {
			br, _ := gitOut(abs, "branch", "--show-current")
			if br == "" {
				br = cfgPrefix + name
			}
			createTask(task{name, br, ""})
		}
		dest := path.Join(tasksDir, name, repo)
		if exists(dest) {
			die("%s is already in task '%s'", repo, name)
		}
		if gitRun(src, "worktree", "move", abs, dest) != nil {
			die("%s: git could not move it (locked or has submodules?)", p)
		}
		writeWorkspace(name)
		// it came from another task: that one's workspace must stop listing it
		if from != "" && isFile(path.Join(tasksDir, from, ".task")) {
			writeWorkspace(from)
		}
		fmt.Println(dest)
	}
}

// mv renames a task; branches keep their names, since they may have PRs open
func cmdMv(args []string) {
	if len(args) != 2 {
		die("usage: izumo mv <task> <new-name>")
	}
	old, name := args[0], args[1]
	loadTask(old)
	validName(name)
	dir := path.Join(tasksDir, old)
	if exists(path.Join(tasksDir, name)) {
		die("task '%s' already exists", name)
	}
	notRemoving(old)
	if under(cwd(), dir) {
		die("%s: you are inside it, cd out first", old)
	}
	if inUse(dir) {
		die("%s: kept (%s)", old, inUseMsg)
	}
	newDir := path.Join(tasksDir, name)
	if os.Rename(dir, newDir) != nil {
		die("%s: could not rename the folder", old)
	}
	// the worktrees moved under git's feet; repair points each repo at the new path
	for _, r := range worktrees(name) {
		if !gitOK(path.Join(root, r), "worktree", "repair", path.Join(newDir, r)) {
			die("%s: git lost track of the worktree; run: git -C '%s' worktree repair '%s'", r, path.Join(root, r), path.Join(newDir, r))
		}
	}
	os.Remove(path.Join(newDir, old+".code-workspace"))
	writeWorkspace(name)
	// the default TASK.md and most templates start with the task name as heading
	md := path.Join(newDir, "TASK.md")
	if b, err := os.ReadFile(md); err == nil {
		first, rest, _ := strings.Cut(string(b), "\n")
		if first == "# "+old {
			_ = os.WriteFile(md, []byte("# "+name+"\n"+rest), 0o644)
		}
	}
	fmt.Println(newDir)
}

// base changes the branch a task's new repos start from and its stages compare against.
// Existing worktrees stay where they are: rebasing them is git's job.
func cmdBase(args []string) {
	var name, branch string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--task":
			name = flagValue(args, i)
			i++
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		case branch != "":
			die("usage: izumo base <branch> [--task T]")
		default:
			branch = a
		}
	}
	if branch == "" {
		die("usage: izumo base <branch> [--task T]")
	}
	t := loadTask(taskFromCwd(name))
	if !gitOK(root, "check-ref-format", "--branch", branch) {
		die("invalid branch name: %s", branch)
	}
	checkBase(branch, worktrees(t.name))
	t.base = branch
	saveTask(t)
	for _, r := range worktrees(t.name) {
		if !onOrigin(path.Join(root, r), branch) {
			ref, _ := baseRef(path.Join(root, r), branch)
			say("%s: no origin/%s, uses %s", r, branch, ref)
		}
	}
	fmt.Printf("%s: base %s\n", t.name, branch)
}

// run: one command in each repo of a task, in order; fails if it failed in any
func cmdRun(args []string) {
	var name string
	for len(args) > 0 {
		if args[0] == "--task" {
			name = flagValue(args, 0)
			args = args[2:]
			continue
		}
		if args[0] == "--" {
			args = args[1:]
		}
		break
	}
	if len(args) == 0 {
		die("usage: izumo run [--task T] [--] <command...>")
	}
	t := loadTask(taskFromCwd(name))
	var failed []string
	for _, r := range worktrees(t.name) {
		fmt.Println("== " + r)
		if inWt(t.name, r, os.Stdout, args...) != nil {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		die("failed in: %s", strings.Join(failed, " "))
	}
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
func cmdPull(repos []string) {
	for _, r := range repos {
		validName(r)
		if !isDir(path.Join(root, r, ".git")) {
			die("%s: no git clone at %s", r, path.Join(root, r))
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
		os.Exit(1)
	}
}
