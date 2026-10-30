package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"golang.org/x/term"
)

const inUseMsg = "a program has the folder open (VS Code, a terminal, a dev server?), close it"

// errKept: a task stays, and izumo said why
var errKept = errors.New("kept")

// what to do with the dev servers of what is removed
type stopMode int

const (
	stopAsk stopMode = iota // ask on a terminal, refuse elsewhere
	stopYes                 // --stop
	stopNo                  // --no-stop
)

func stopFlag(o opts) (stopMode, error) {
	switch {
	case o.has("--stop") && o.has("--no-stop"):
		return stopAsk, errors.New("--stop or --no-stop, not both")
	case o.has("--stop"):
		return stopYes, nil
	case o.has("--no-stop"):
		return stopNo, nil
	}
	return stopAsk, nil
}

// answers: one reader for every question, so answers piped in for a done sweep all reach it
var answers = bufio.NewReader(os.Stdin)

// terminal: someone can answer a question. The shim tells, since under Git Bash's terminal
// the binary only sees a pipe.
func terminal() bool {
	if v := os.Getenv("IZUMO_TTY"); v != "" {
		return v == "1"
	}
	return term.IsTerminal(int(os.Stdin.Fd())) // on Windows, NUL is a char device but no console
}

// inUse: Windows won't delete a folder a program has open, and git finds out halfway through;
// a rename fails in the same cases, so try one while nothing is removed yet
func inUse(dir string) (bool, error) {
	if !windows {
		return false, nil
	}
	tmp := path.Join(path.Dir(dir), "."+path.Base(dir)+".izumo-check")
	if os.Rename(dir, tmp) != nil {
		return true, nil
	}
	if os.Rename(tmp, dir) != nil {
		return false, fmt.Errorf("could not rename %s back to %s", tmp, dir)
	}
	return false, nil
}

// free stops the servers running from dir once the user agrees, then checks nothing else has
// dir open. why says what keeps it.
func free(label, dir string, srv []server, stop stopMode) (why string, err error) {
	if stop == stopNo {
		srv = nil
	}
	if len(srv) > 0 {
		list := describe(srv)
		if stop == stopAsk {
			if !terminal() {
				return "serving: " + list + "; pass --stop to stop it, or --no-stop to leave it running", nil
			}
			fmt.Fprintf(os.Stderr, "izumo: %s: stop %s? [Y/n] ", label, list)
			if a, err := answers.ReadString('\n'); err != nil && a == "" || strings.HasPrefix(strings.ToLower(a), "n") {
				srv = nil
			}
		}
		if len(srv) > 0 {
			stopServers(srv)
			say("%s: stopped %s", label, list)
			// the shells that started them exit right after and let go of the folder
			for n := 0; n < 5; n++ {
				if busy, _ := inUse(dir); !busy {
					break
				}
				time.Sleep(time.Second)
			}
		}
	}
	busy, err := inUse(dir)
	if busy {
		return inUseMsg, err
	}
	return "", err
}

// preRemove runs the pre_remove hook; it must succeed and, unless discarding, leave the
// worktree clean
func preRemove(t, repo string, discard bool) error {
	if hook("pre_remove", t, repo, os.Stdout) != nil {
		return fmt.Errorf("%s: kept (pre_remove failed in %s)", t, repo)
	}
	if st, _ := gitOut(path.Join(tasksDir, t, repo), "status", "--porcelain"); !discard && st != "" {
		return fmt.Errorf("%s: kept (pre_remove left changes in %s)", t, repo)
	}
	return nil
}

func removeWorktree(repo, wt string, force bool) error {
	// longpaths: deep node_modules otherwise stop the removal halfway on Windows
	args := []string{"-c", "core.longpaths=true", "-C", path.Join(root, repo), "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	c := exec.Command("git", append(args, wt)...)
	c.Stderr = os.Stderr
	return c.Run()
}

// deleteBranch deletes a task's branch only while it still points where the checks saw it: a
// commit made since (an agent in another session, a pre_remove hook) keeps the branch.
// An empty sha comes from a .removing written before izumo recorded it.
func deleteBranch(repo, br, sha string) {
	src := path.Join(root, repo)
	if br == "" || !hasRef(src, "refs/heads/"+br) {
		return
	}
	if cur, _ := gitOut(src, "rev-parse", "refs/heads/"+br); sha != "" && cur != sha {
		say("%s: branch %s moved since the checks, so it stays", repo, br)
		return
	}
	if !gitOK(src, "branch", "-D", "-q", br) {
		say("%s: could not delete branch %s", repo, br)
	}
}

// drop takes repos out of a task when nothing would be lost; the task stays
func cmdDrop(args []string) error {
	o, err := parseFlags(args, []string{"--task"}, []string{"--stop", "--no-stop"})
	if err != nil {
		return err
	}
	if len(o.args) == 0 {
		return errors.New("usage: izumo drop <repo...> [--task T] [--stop|--no-stop]")
	}
	stop, err := stopFlag(o)
	if err != nil {
		return err
	}
	name, err := taskFromCwd(o.values["--task"])
	if err != nil {
		return err
	}
	t, err := loadTask(name)
	if err != nil {
		return err
	}
	for _, r := range o.args {
		if err := drop(t, r, stop); err != nil {
			return err
		}
	}
	return nil
}

func drop(t task, r string, stop stopMode) error {
	if err := validName(r); err != nil {
		return err
	}
	wt := path.Join(tasksDir, t.name, r)
	if !isFile(path.Join(wt, ".git")) {
		return fmt.Errorf("%s is not in task '%s'", r, t.name)
	}
	if under(cwd(), wt) {
		return fmt.Errorf("%s: you are inside it, cd out first", r)
	}
	fetch(path.Join(root, r))
	s, err := stateOf(t, r)
	if err != nil {
		return err
	}
	if s.dirty() {
		return fmt.Errorf("%s: kept (uncommitted changes: %d modified, %d untracked)", r, s.modified, s.untracked)
	}
	if s.unpushed > 0 {
		return fmt.Errorf("%s: kept (%d unpushed commit(s))", r, s.unpushed)
	}
	if why, err := free(r, wt, serversIn(t.name, r), stop); err != nil || why != "" {
		return keptErr(r, why, err)
	}
	if err := preRemove(t.name, r, false); err != nil {
		return err
	}
	if removeWorktree(r, wt, false) != nil {
		return fmt.Errorf("%s: could not remove the worktree (a program has it open?)", r)
	}
	gitOK(path.Join(root, r), "worktree", "prune")
	// every commit is on a remote or in a merged PR, so the local branch holds nothing of its own
	deleteBranch(r, s.branch, s.head)
	if err := writeWorkspace(t.name); err != nil {
		return err
	}
	fmt.Printf("dropped %s from %s\n", r, t.name)
	return nil
}

func cmdDone(args []string) error { return removeTasks("done", args) }
func cmdRm(args []string) error   { return removeTasks("rm", args) }

// done and rm: each task is checked, then removed or kept; a sweep (done with no task named)
// goes on past a task that stays
func removeTasks(mode string, args []string) error {
	o, err := parseFlags(args, nil, []string{"--dry-run", "--discard", "--stop", "--no-stop"})
	if err != nil {
		return err
	}
	if o.has("--discard") && mode != "rm" {
		return errors.New("--discard only goes with rm: done is for work that shipped")
	}
	stop, err := stopFlag(o)
	if err != nil {
		return err
	}
	names, named := o.args, len(o.args) > 0
	if !named {
		if mode != "done" {
			return errors.New("usage: izumo rm <task...> [--dry-run] [--discard]")
		}
		names = allTasks()
	}
	kept, failed := false, false
	for _, n := range names {
		err := removeTask(mode, n, o.has("--dry-run"), o.has("--discard"), stop)
		switch {
		case errors.Is(err, errKept):
			kept = true
		case err != nil:
			say("%v", err)
			failed = true
		}
	}
	if failed || named && kept {
		return errSaid
	}
	return nil
}

// removal: what removing a task would remove, lose and keep, before anything is touched
type removal struct {
	t        task
	dir      string
	states   map[string]repoState // as the checks saw each repo
	lost     []string             // what discarding loses
	why      []string             // what keeps the task
	discard  bool
	resuming bool // an earlier removal stopped halfway
}

// planRemoval: done wants every repo in prod (or untouched) and nothing lost; rm wants nothing
// lost; rm --discard lets what would be lost go, saying what it is
func planRemoval(mode string, t task, discard bool) (*removal, error) {
	p := &removal{t: t, dir: path.Join(tasksDir, t.name), states: map[string]repoState{}, discard: discard}
	// an earlier removal stopped halfway: its checks passed back then, so finish what it listed
	if p.resuming = exists(path.Join(p.dir, ".removing")); p.resuming {
		mode = "rm"
	}
	repos := worktrees(t.name)
	fetchAll(repos)
	type result struct {
		s   repoState
		err error
	}
	results := parallel(len(repos), func(i int) result {
		s, err := stateOf(t, repos[i])
		return result{s, err}
	})
	shipped := false
	for i, r := range repos {
		s, err := results[i].s, results[i].err
		if err != nil {
			return nil, err
		}
		p.states[r] = s
		if s.dirty() {
			p.lost = append(p.lost, fmt.Sprintf("%s has uncommitted changes (%d modified, %d untracked)", r, s.modified, s.untracked))
		}
		if s.unpushed > 0 {
			p.lost = append(p.lost, fmt.Sprintf("%s has %d unpushed commit(s)", r, s.unpushed))
		}
		if mode == "done" {
			switch s.stage {
			case stageProd:
				shipped = true
			case stageEmpty:
			default:
				p.why = append(p.why, fmt.Sprintf("%s is not in prod (%s)", r, s.stage))
			}
		}
	}
	// Windows locks a shell's cwd, so removal would stop halfway; elsewhere the shell is left in a deleted dir
	if under(cwd(), p.dir) {
		p.why = append(p.why, "you are inside it, cd out first")
	}
	listed := map[string]bool{}
	for _, l := range removingLines(t.name) {
		listed[l[0]] = true
	}
	es, _ := os.ReadDir(p.dir)
	for _, e := range es {
		f := e.Name()
		switch f {
		case ".task", ".agent", ".removing", "TASK.md", "AGENTS.md", "CLAUDE.md", t.name + ".code-workspace":
			continue
		}
		if isFile(path.Join(p.dir, f, ".git")) || p.resuming && listed[f] {
			continue
		}
		p.lost = append(p.lost, "unknown file "+f)
	}
	if !discard {
		p.why, p.lost = append(p.lost, p.why...), nil
	}
	if mode == "done" && !shipped && len(p.why) == 0 {
		p.why = []string{"nothing reached prod"}
	}
	return p, nil
}

func removeTask(mode, name string, dry, discard bool, stop stopMode) error {
	t, err := loadTask(name)
	if err != nil {
		return err
	}
	p, err := planRemoval(mode, t, discard)
	if err != nil {
		return err
	}
	stopping := ""
	if len(p.why) == 0 {
		srv := serversIn(name, "")
		if dry && stop != stopNo && len(srv) > 0 {
			// the servers hold the folder open, so whether it is free shows only once they are gone
			stopping = describe(srv)
		} else if why, err := free(name, p.dir, srv, stop); err != nil {
			return err
		} else if why != "" {
			p.why = []string{why}
		}
	}
	if len(p.why) > 0 {
		say("%s: kept (%s)", name, strings.Join(p.why, "; "))
		return errKept
	}
	if dry {
		if len(p.lost) > 0 {
			fmt.Printf("would remove %s, discarding: %s\n", name, strings.Join(p.lost, "; "))
		} else {
			fmt.Printf("would remove %s\n", name)
		}
		if stopping != "" {
			fmt.Printf("  and stop %s\n", stopping)
		}
		return nil
	}
	return p.apply()
}

func (p *removal) apply() error {
	name, dir := p.t.name, p.dir
	if len(p.lost) > 0 {
		say("%s: discarding %s", name, strings.Join(p.lost, "; "))
	}
	if !p.resuming {
		for _, r := range worktrees(name) {
			if err := preRemove(name, r, p.discard); err != nil {
				say("%v", err)
				return errKept
			}
		}
		if err := archive(name); err != nil {
			return err
		}
		// each repo's branch and commit as the checks saw them: deleting the branch waits for both
		var b strings.Builder
		for _, r := range worktrees(name) {
			fmt.Fprintf(&b, "%s %s %s\n", r, p.states[r].branch, p.states[r].head)
		}
		if os.WriteFile(path.Join(dir, ".removing"), []byte(b.String()), 0o644) != nil {
			return fmt.Errorf("%s: could not write %s/.removing; nothing removed", name, dir)
		}
	}
	for _, l := range removingLines(name) {
		r, br, sha := l[0], l[1], l[2]
		if err := validName(r); err != nil {
			return err
		}
		if isFile(path.Join(dir, r, ".git")) {
			if removeWorktree(r, path.Join(dir, r), len(p.lost) > 0) != nil {
				return fmt.Errorf("%s: could not remove worktree %s (a program has it open?); fix that and run the same command to finish", name, r)
			}
		} else if exists(path.Join(dir, r)) {
			if os.RemoveAll(path.Join(dir, r)) != nil {
				return fmt.Errorf("%s: could not remove what is left of %s (a program has it open?); fix that and run the same command to finish", name, r)
			}
		}
		gitOK(path.Join(root, r), "worktree", "prune")
		deleteBranch(r, br, sha)
	}
	if os.RemoveAll(dir) != nil {
		return fmt.Errorf("%s: worktrees and branches are gone, but the folder is in use; delete %s once nothing has it open", name, dir)
	}
	fmt.Printf("removed %s\n", name)
	return nil
}

// archive keeps TASK.md in tasks/.archive; the same name removed twice in a day keeps both
func archive(name string) error {
	b, err := os.ReadFile(path.Join(tasksDir, name, "TASK.md"))
	if err != nil {
		return nil
	}
	arch, day := path.Join(tasksDir, ".archive"), time.Now().Format("2006-01-02")
	f := path.Join(arch, day+"-"+name+".md")
	for n := 2; exists(f); n++ {
		f = path.Join(arch, fmt.Sprintf("%s-%s-%d.md", day, name, n))
	}
	if os.MkdirAll(arch, 0o755) != nil || os.WriteFile(f, b, 0o644) != nil {
		return fmt.Errorf("%s: could not archive TASK.md; nothing removed", name)
	}
	return nil
}

// removingLines: repo, branch and commit per line of .removing, the list a removal started with
func removingLines(t string) [][3]string {
	f, err := os.Open(path.Join(tasksDir, t, ".removing"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][3]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l [3]string
		copy(l[:], strings.SplitN(strings.TrimRight(sc.Text(), "\r"), " ", 3))
		if l[0] != "" {
			out = append(out, l)
		}
	}
	return out
}
