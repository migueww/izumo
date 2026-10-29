package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"golang.org/x/term"
)

const inUseMsg = "a program has the folder open (VS Code, a terminal, a dev server?), close it"

// stop: "yes" (--stop), "no" (--no-stop) or "" (ask on a terminal, refuse elsewhere)
var stop string

// inUse: Windows won't delete a folder a program has open, and git finds out halfway through;
// a rename fails in the same cases, so try one while nothing is removed yet
func inUse(dir string) bool {
	if !windows {
		return false
	}
	tmp := path.Join(path.Dir(dir), "."+path.Base(dir)+".izumo-check")
	if os.Rename(dir, tmp) != nil {
		return true
	}
	if os.Rename(tmp, dir) != nil {
		die("could not rename %s back to %s", tmp, dir)
	}
	return false
}

// terminal: someone can answer a question. The shim tells, since under Git Bash's terminal
// the binary only sees a pipe.
func terminal() bool {
	if v := os.Getenv("IZUMO_TTY"); v != "" {
		return v == "1"
	}
	return term.IsTerminal(int(os.Stdin.Fd())) // on Windows, NUL is a char device but no console
}

// free stops the servers running from the task (or that repo of it) once the user agrees,
// then checks nothing else has the folder open. why says what keeps it; on a dry run,
// stopping says what would be stopped.
func free(dry bool, label, t, repo string) (why, stopping string) {
	dir := path.Join(tasksDir, t)
	if repo != "" {
		dir = path.Join(dir, repo)
	}
	var srv []server
	if stop != "no" {
		for _, s := range servers() {
			if s.task == t && (repo == "" || s.repo == repo) {
				srv = append(srv, s)
			}
		}
	}
	if len(srv) > 0 {
		var l []string
		for _, s := range srv {
			l = append(l, fmt.Sprintf("%s on :%s in %s (pid %d)", s.name, strings.ReplaceAll(s.portList(), ",", " :"), s.repo, s.pid))
		}
		list := strings.Join(l, ", ")
		// the servers hold the folder open, so whether it is free shows only once they are gone
		if dry {
			return "", list
		}
		if stop != "yes" {
			if !terminal() {
				return "serving: " + list + "; pass --stop to stop it, or --no-stop to leave it running", ""
			}
			fmt.Fprintf(os.Stderr, "izumo: %s: stop %s? [Y/n] ", label, list)
			a, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && a == "" || strings.HasPrefix(strings.ToLower(a), "n") {
				srv = nil
			}
		}
		if len(srv) > 0 {
			stopServers(srv)
			say("%s: stopped %s", label, list)
			// the shells that started them exit right after and let go of the folder
			for n := 0; n < 5 && inUse(dir); n++ {
				time.Sleep(time.Second)
			}
		}
	}
	if inUse(dir) {
		return inUseMsg, ""
	}
	return "", ""
}

func parseStop(a string) bool {
	switch a {
	case "--stop":
		stop = "yes"
	case "--no-stop":
		stop = "no"
	default:
		return false
	}
	return true
}

// preRemoveOK runs the pre_remove hook; it must succeed and, unless discarding, leave the
// worktree clean
func preRemoveOK(t, repo string, discard bool) bool {
	if hook("pre_remove", t, repo, os.Stdout) != nil {
		say("%s: kept (pre_remove failed in %s)", t, repo)
		return false
	}
	if st, _ := gitOut(path.Join(tasksDir, t, repo), "status", "--porcelain"); !discard && st != "" {
		say("%s: kept (pre_remove left changes in %s)", t, repo)
		return false
	}
	return true
}

func removeWorktree(repo, wt string, force bool) error {
	args := []string{"-c", "core.longpaths=true", "-C", path.Join(root, repo), "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	c := exec.Command("git", append(args, wt)...)
	c.Stderr = os.Stderr
	return c.Run()
}

// drop takes repos out of a task when nothing would be lost; the task stays
func cmdDrop(args []string) {
	var name string
	var repos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--task":
			name = flagValue(args, i)
			i++
		case parseStop(a):
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		default:
			repos = append(repos, a)
		}
	}
	if len(repos) == 0 {
		die("usage: izumo drop <repo...> [--task T] [--stop|--no-stop]")
	}
	t := loadTask(taskFromCwd(name))
	for _, r := range repos {
		validName(r)
		wt := path.Join(tasksDir, t.name, r)
		if !isFile(path.Join(wt, ".git")) {
			die("%s is not in task '%s'", r, t.name)
		}
		if under(cwd(), wt) {
			die("%s: you are inside it, cd out first", r)
		}
		fetch(path.Join(root, r))
		s, err := stateOf(t, r)
		if err != nil {
			die("%v", err)
		}
		if s.dirty() {
			die("%s: kept (uncommitted changes: %d modified, %d untracked)", r, s.modified, s.untracked)
		}
		if s.unpushed > 0 {
			die("%s: kept (%d unpushed commit(s))", r, s.unpushed)
		}
		if why, _ := free(false, r, t.name, r); why != "" {
			die("%s: kept (%s)", r, why)
		}
		if !preRemoveOK(t.name, r, false) {
			os.Exit(1)
		}
		if removeWorktree(r, wt, false) != nil {
			die("%s: could not remove the worktree (a program has it open?)", r)
		}
		gitOK(path.Join(root, r), "worktree", "prune")
		// every commit is on a remote or in a merged PR, so the local branch holds nothing of its own
		if s.branch != "" && !gitOK(path.Join(root, r), "branch", "-D", "-q", s.branch) {
			say("%s: could not delete branch %s", r, s.branch)
		}
		writeWorkspace(t.name)
		fmt.Printf("dropped %s from %s\n", r, t.name)
	}
}

func cmdRemove(mode string, args []string) int {
	dry, discard := false, false
	var names []string
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dry = true
		case a == "--discard":
			if mode != "rm" {
				die("--discard only goes with rm: done is for work that shipped")
			}
			discard = true
		case parseStop(a):
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		default:
			names = append(names, a)
		}
	}
	named := len(names) > 0
	if !named {
		if mode != "done" {
			die("usage: izumo rm <task...> [--dry-run] [--discard]")
		}
		names = allTasks()
	}
	kept := 0
	for _, t := range names {
		if !removeTask(mode, dry, t, discard) {
			kept = 1
		}
	}
	if !named {
		return 0
	}
	return kept
}

// done: every repo in prod (or untouched) and nothing would be lost. rm: nothing would be lost.
// rm --discard: work that would be lost goes to lost instead of blocking.
func removeTask(mode string, dry bool, name string, discard bool) bool {
	t := loadTask(name)
	dir := path.Join(tasksDir, name)
	var why, lost []string
	shipped := false
	// an earlier removal stopped halfway: its checks passed back then, so finish what it listed
	resuming := exists(path.Join(dir, ".removing"))
	if resuming {
		mode = "rm"
	}
	for _, r := range worktrees(name) {
		fetch(path.Join(root, r))
		s, err := stateOf(t, r)
		if err != nil {
			die("%v", err)
		}
		if s.dirty() {
			lost = append(lost, fmt.Sprintf("%s has uncommitted changes (%d modified, %d untracked)", r, s.modified, s.untracked))
		}
		if s.unpushed > 0 {
			lost = append(lost, fmt.Sprintf("%s has %d unpushed commit(s)", r, s.unpushed))
		}
		if mode == "done" {
			switch s.stage {
			case "prod":
				shipped = true
			case "empty":
			default:
				why = append(why, fmt.Sprintf("%s is not in prod (%s)", r, s.stage))
			}
		}
	}
	// Windows locks a shell's cwd, so removal would stop halfway; elsewhere the shell is left in a deleted dir
	if under(cwd(), dir) {
		why = append(why, "you are inside it, cd out first")
	}
	listed := removingList(name)
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		f := e.Name()
		switch f {
		case ".task", ".agent", ".removing", "TASK.md", "AGENTS.md", "CLAUDE.md", name + ".code-workspace":
			continue
		}
		if isFile(path.Join(dir, f, ".git")) || resuming && listed[f] {
			continue
		}
		lost = append(lost, "unknown file "+f)
	}
	if !discard {
		why, lost = append(lost, why...), nil
	}
	if mode == "done" && !shipped && len(why) == 0 {
		why = []string{"nothing reached prod"}
	}
	var stopping string
	if len(why) == 0 {
		var w string
		if w, stopping = free(dry, name, name, ""); w != "" {
			why = []string{w}
		}
	}
	if len(why) > 0 {
		say("%s: kept (%s)", name, strings.Join(why, "; "))
		return false
	}
	if dry {
		if len(lost) > 0 {
			fmt.Printf("would remove %s, discarding: %s\n", name, strings.Join(lost, "; "))
		} else {
			fmt.Printf("would remove %s\n", name)
		}
		if stopping != "" {
			fmt.Printf("  and stop %s\n", stopping)
		}
		return true
	}
	if len(lost) > 0 {
		say("%s: discarding %s", name, strings.Join(lost, "; "))
	}

	if !resuming {
		for _, r := range worktrees(name) {
			if !preRemoveOK(name, r, discard) {
				return false
			}
		}
		if b, err := os.ReadFile(path.Join(dir, "TASK.md")); err == nil {
			arch := path.Join(tasksDir, ".archive")
			if os.MkdirAll(arch, 0o755) != nil || os.WriteFile(path.Join(arch, time.Now().Format("2006-01-02")+"-"+name+".md"), b, 0o644) != nil {
				die("%s: could not archive TASK.md; nothing removed", name)
			}
		}
		var b strings.Builder
		for _, r := range worktrees(name) {
			br, _ := gitOut(path.Join(dir, r), "branch", "--show-current")
			fmt.Fprintf(&b, "%s %s\n", r, br)
		}
		if os.WriteFile(path.Join(dir, ".removing"), []byte(b.String()), 0o644) != nil {
			die("%s: could not write %s/.removing; nothing removed", name, dir)
		}
	}
	for _, l := range removingLines(name) {
		r, br := l[0], l[1]
		validName(r)
		src := path.Join(root, r)
		if isFile(path.Join(dir, r, ".git")) {
			// longpaths: deep node_modules otherwise stop the removal halfway on Windows
			if removeWorktree(r, path.Join(dir, r), len(lost) > 0) != nil {
				die("%s: could not remove worktree %s (a program has it open?); fix that and run the same command to finish", name, r)
			}
		} else if exists(path.Join(dir, r)) {
			if os.RemoveAll(path.Join(dir, r)) != nil {
				die("%s: could not remove what is left of %s (a program has it open?); fix that and run the same command to finish", name, r)
			}
		}
		gitOK(src, "worktree", "prune")
		if br != "" && hasRef(src, "refs/heads/"+br) && !gitOK(src, "branch", "-D", "-q", br) {
			say("%s: could not delete branch %s", r, br)
		}
	}
	if os.RemoveAll(dir) != nil {
		die("%s: worktrees and branches are gone, but the folder is in use; delete %s once nothing has it open", name, dir)
	}
	fmt.Printf("removed %s\n", name)
	return true
}

// removingLines: repo and branch per line of .removing, the list a removal started with
func removingLines(t string) [][2]string {
	f, err := os.Open(path.Join(tasksDir, t, ".removing"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][2]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		r, br, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if r != "" {
			out = append(out, [2]string{r, br})
		}
	}
	return out
}

func removingList(t string) map[string]bool {
	m := map[string]bool{}
	for _, l := range removingLines(t) {
		m[l[0]] = true
	}
	return m
}
