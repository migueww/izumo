package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// parallel runs f for 0..n-1, 8 at a time: each call costs a few git calls and maybe a gh API call
func parallel[T any](n int, f func(int) T) []T {
	out := make([]T, n)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			out[i] = f(i)
		}()
	}
	wg.Wait()
	return out
}

// what ls --json prints, and the text output reads
type lsTask struct {
	Task     string   `json:"task"`
	Path     string   `json:"path"`
	Goal     string   `json:"goal"`
	Ready    bool     `json:"ready"` // done would take it
	Removing bool     `json:"removing"`
	Agent    *agent   `json:"agent"`
	Repos    []lsRepo `json:"repos"`
}

// lsServer: a dev server running from the repo's worktree
type lsServer struct {
	PID     int32    `json:"pid"`
	Program string   `json:"program"`
	Ports   []uint32 `json:"ports"`
	Since   int64    `json:"since"` // unix seconds
}

type agent struct {
	State string `json:"state"`
	Since int64  `json:"since"`
}

type lsRepo struct {
	Repo      string     `json:"repo"`
	Branch    string     `json:"branch"`
	Stage     stage      `json:"stage"`
	Dirty     bool       `json:"dirty"`
	Modified  int        `json:"modified"`
	Untracked int        `json:"untracked"`
	Unpushed  int        `json:"unpushed"`
	PR        string     `json:"pr"`
	Review    string     `json:"review"`
	CI        string     `json:"ci"`
	Serving   []uint32   `json:"serving"` // every port of the servers below
	Servers   []lsServer `json:"servers"`
	Behind    int        `json:"behind"` // a reference: commits its base got since; izumo pull catches up
}

func cmdLs(args []string) error {
	o, err := parseFlags(args, nil, []string{"--json", "--fetch"})
	if err != nil {
		return err
	}
	names := o.args
	if len(names) == 0 {
		names = allTasks()
	}
	type job struct {
		t    task
		repo string
	}
	var jobs []job
	var clones []string
	seen := map[string]bool{}
	tasks := make([]task, len(names))
	for i, n := range names {
		if tasks[i], err = loadTask(n); err != nil {
			return err
		}
		for _, r := range worktrees(n) {
			jobs = append(jobs, job{tasks[i], r})
			if !seen[r] {
				seen[r] = true
				clones = append(clones, r)
			}
		}
	}
	if o.has("--fetch") {
		fetchAll(clones)
	}

	srvc := make(chan []server, 1)
	go func() {
		if len(jobs) == 0 {
			srvc <- nil
			return
		}
		srvc <- servers()
	}()
	states := parallel(len(jobs), func(i int) repoState {
		s, err := stateOf(jobs[i].t, jobs[i].repo)
		if err != nil {
			say("%v", err)
			return repoState{branch: "?", stage: stageError}
		}
		return s
	})
	srv := <-srvc

	list := make([]lsTask, 0, len(tasks))
	k := 0
	for _, t := range tasks {
		lt := lsTask{Task: t.name, Path: path.Join(tasksDir, t.name), Goal: goal(t.name), Repos: []lsRepo{},
			Removing: exists(path.Join(tasksDir, t.name, ".removing")), Agent: agentOf(t.name)}
		shipped, ready := false, true
		for _, r := range worktrees(t.name) {
			s := states[k]
			k++
			lr := lsRepo{Repo: r, Branch: s.branch, Stage: s.stage, Dirty: s.dirty(), Modified: s.modified,
				Untracked: s.untracked, Unpushed: s.unpushed, Behind: s.behind, PR: s.pr, Review: s.review, CI: s.ci,
				Serving: []uint32{}, Servers: []lsServer{}}
			for _, v := range srv {
				if v.task == t.name && v.repo == r {
					lr.Serving = append(lr.Serving, v.ports...)
					lr.Servers = append(lr.Servers, lsServer{v.pid, v.name, v.ports, v.created / 1000})
				}
			}
			switch s.stage {
			case stageProd:
				shipped = true
			case stageEmpty, stageRef:
			default:
				ready = false
			}
			if s.modified+s.untracked+s.unpushed > 0 {
				ready = false
			}
			lt.Repos = append(lt.Repos, lr)
		}
		lt.Ready = ready && shipped && !lt.Removing
		list = append(list, lt)
	}

	if o.has("--json") {
		e := json.NewEncoder(os.Stdout)
		e.SetEscapeHTML(false)
		e.SetIndent("", "  ")
		return e.Encode(list)
	}
	agentColor := map[string]string{"working": cyan, "waiting": yellow, "done": dim}
	for _, t := range list {
		fmt.Print(paint(bold, t.Task))
		if t.Ready {
			fmt.Print("  " + paint(green, "[ready: izumo done]"))
		}
		if t.Agent != nil {
			fmt.Print("  " + paint(agentColor[t.Agent.State], fmt.Sprintf("[agent %s %s]", t.Agent.State, ago(t.Agent.Since))))
		}
		if t.Removing {
			fmt.Print("  " + paint(red, fmt.Sprintf("[removal stopped halfway: izumo rm %s]", t.Task)))
		}
		fmt.Printf("  %s\n", t.Goal)
		for _, r := range t.Repos {
			flags := ""
			for _, f := range [][2]string{{"modified", strconv.Itoa(r.Modified)}, {"untracked", strconv.Itoa(r.Untracked)}, {"unpushed", strconv.Itoa(r.Unpushed)}} {
				if f[1] != "0" {
					flags += " " + paint(flagColor[f[0]], f[1]+"-"+f[0])
				}
			}
			if r.Review != "" {
				flags += " " + paint(flagColor[r.Review], r.Review)
			}
			if r.CI != "" {
				flags += " " + paint(flagColor["ci:"+r.CI], "ci:"+r.CI)
			}
			// each server with how long it has been up: a forgotten one shows its days
			for _, v := range r.Servers {
				flags += " " + paint(cyan, "serving:"+portLinks(v.Ports)) + " " + paint(dim, "("+ago(v.Since)+")")
			}
			if r.Behind > 0 {
				flags += " " + paint(yellow, strconv.Itoa(r.Behind)+"-behind")
			}
			// padded before painting, so the columns line up with or without color
			fmt.Printf("  %s %s %s%s %s\n", fmt.Sprintf("%-32s", r.Repo), paint(dim, fmt.Sprintf("%-36s", r.Branch)),
				paint(stageColor[r.Stage], fmt.Sprintf("%-6s", r.Stage)), flags, paint(dim, r.PR))
		}
	}
	return nil
}

// goal: the first section of TASK.md is the goal, whatever its heading
func goal(t string) string {
	b, err := os.ReadFile(path.Join(tasksDir, t, "TASK.md"))
	if err != nil {
		return ""
	}
	n := 0
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
		if strings.HasPrefix(l, "## ") {
			if n++; n > 1 {
				return ""
			}
			continue
		}
		if n == 1 && strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

// agentOf: what the hook recorded in .agent, nil when nothing
func agentOf(t string) *agent {
	b, err := os.ReadFile(path.Join(tasksDir, t, ".agent"))
	if err != nil {
		return nil
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return nil
	}
	since, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || since < 0 {
		return nil
	}
	return &agent{f[0], since}
}

func ago(since int64) string {
	s := time.Now().Unix() - since
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	}
	return fmt.Sprintf("%dd", s/86400)
}

// portLinks: the ports, each a link to its localhost where the terminal shows links (OSC 8)
func portLinks(ports []uint32) string {
	l := make([]string, len(ports))
	for i, p := range ports {
		l[i] = strconv.Itoa(int(p))
		if color() {
			l[i] = fmt.Sprintf("\x1b]8;;http://localhost:%d\x1b\\%d\x1b]8;;\x1b\\", p, p)
		}
	}
	return strings.Join(l, ",")
}

// agentHook is the Claude Code hook (hooks/hooks.json): record what the agent in this task is
// doing. It fires in every session, so it stays cheap, silent and exits 0 outside a task.
func agentHook(args []string) {
	if len(args) == 0 {
		return
	}
	state := args[0]
	switch state {
	case "working", "waiting", "done", "end":
	default:
		return
	}
	wd, _ := os.Getwd()
	for _, start := range []string{wd, os.Getenv("CLAUDE_PROJECT_DIR")} {
		if start == "" {
			continue
		}
		d, err := realPath(start)
		if err != nil {
			continue
		}
		for ; path.Dir(d) != d; d = path.Dir(d) {
			if isFile(path.Join(d, ".task")) && isFile(path.Join(path.Dir(path.Dir(d)), ".izumo")) {
				f := path.Join(d, ".agent")
				if state == "end" {
					os.Remove(f)
				} else {
					_ = os.WriteFile(f, []byte(fmt.Sprintf("%s %d\n", state, time.Now().Unix())), 0o644)
				}
				return
			}
		}
	}
}
