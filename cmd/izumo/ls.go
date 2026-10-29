package main

import (
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

func cmdLs(args []string) {
	jsonOut, doFetch := false, false
	var names []string
	for _, a := range args {
		switch {
		case a == "--json":
			jsonOut = true
		case a == "--fetch":
			doFetch = true
		case strings.HasPrefix(a, "-"):
			die("unknown flag %s", a)
		default:
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		names = allTasks()
	}
	tasks := make([]task, len(names))
	type job struct {
		t    task
		repo string
	}
	var jobs []job
	seen := map[string]bool{}
	var clones []string
	for i, n := range names {
		tasks[i] = loadTask(n)
		for _, r := range worktrees(n) {
			jobs = append(jobs, job{tasks[i], r})
			if !seen[r] {
				seen[r] = true
				clones = append(clones, r)
			}
		}
	}

	if doFetch {
		var wg sync.WaitGroup
		for _, r := range clones {
			wg.Add(1)
			go func() { defer wg.Done(); fetch(path.Join(root, r)) }()
		}
		wg.Wait()
	}

	var srv []server
	srvDone := make(chan struct{})
	go func() {
		if len(jobs) > 0 {
			srv = servers()
		}
		close(srvDone)
	}()
	states := parallel(len(jobs), func(i int) repoState {
		s, err := stateOf(jobs[i].t, jobs[i].repo)
		if err != nil {
			say("%v", err)
			return repoState{branch: "?", stage: "error"}
		}
		return s
	})
	<-srvDone
	stateOfJob := map[string]repoState{}
	for i, j := range jobs {
		stateOfJob[j.t.name+"/"+j.repo] = states[i]
	}

	if jsonOut {
		fmt.Print("[")
	}
	tsep := ""
	for _, t := range tasks {
		state, since := agentOf(t.name)
		removing := exists(path.Join(tasksDir, t.name, ".removing"))
		// repos first: the header says whether done would take the task
		var lines strings.Builder
		rsep, shipped, ready := "", false, true
		for _, r := range worktrees(t.name) {
			s := stateOfJob[t.name+"/"+r]
			var ports []string
			for _, v := range srv {
				if v.task == t.name && v.repo == r {
					ports = append(ports, v.portList())
				}
			}
			serving := strings.Join(ports, ",")
			switch s.stage {
			case "prod":
				shipped = true
			case "empty":
			default:
				ready = false
			}
			if s.modified+s.untracked+s.unpushed > 0 {
				ready = false
			}
			if jsonOut {
				fmt.Fprintf(&lines, "%s\n    { \"repo\": %s, \"branch\": %s, \"stage\": \"%s\", \"dirty\": %t, \"modified\": %d, \"untracked\": %d, \"unpushed\": %d, \"pr\": %s, \"review\": %s, \"ci\": %s, \"serving\": [%s] }",
					rsep, jsonStr(r), jsonStr(s.branch), s.stage, s.dirty(), s.modified, s.untracked, s.unpushed,
					jsonStr(s.pr), jsonStr(s.review), jsonStr(s.ci), serving)
				rsep = ","
				continue
			}
			flags := ""
			if s.modified > 0 {
				flags += fmt.Sprintf(" %d-modified", s.modified)
			}
			if s.untracked > 0 {
				flags += fmt.Sprintf(" %d-untracked", s.untracked)
			}
			if s.unpushed > 0 {
				flags += fmt.Sprintf(" %d-unpushed", s.unpushed)
			}
			if s.review != "" {
				flags += " " + s.review
			}
			if s.ci != "" {
				flags += " ci:" + s.ci
			}
			if serving != "" {
				flags += " serving:" + serving
			}
			fmt.Fprintf(&lines, "  %-32s %-36s %-6s%s %s\n", r, s.branch, s.stage, flags, s.pr)
		}
		if !shipped || removing {
			ready = false
		}
		if jsonOut {
			fmt.Printf("%s\n  { \"task\": %s, \"path\": %s, \"goal\": %s, \"ready\": %t, \"removing\": %t, \"agent\": ",
				tsep, jsonStr(t.name), jsonStr(path.Join(tasksDir, t.name)), jsonStr(goal(t.name)), ready, removing)
			if state != "" {
				fmt.Printf("{ \"state\": %s, \"since\": %d }", jsonStr(state), since)
			} else {
				fmt.Print("null")
			}
			fmt.Printf(", \"repos\": [%s\n  ] }", lines.String())
			tsep = ","
			continue
		}
		fmt.Print(t.name)
		if ready {
			fmt.Print("  [ready: izumo done]")
		}
		if state != "" {
			fmt.Printf("  [agent %s %s]", state, ago(since))
		}
		if removing {
			fmt.Printf("  [removal stopped halfway: izumo rm %s]", t.name)
		}
		fmt.Printf("  %s\n%s", goal(t.name), lines.String())
	}
	if jsonOut {
		fmt.Print("\n]\n")
	}
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

// agentOf: what the hook recorded in .agent, empty when nothing
func agentOf(t string) (string, int64) {
	b, err := os.ReadFile(path.Join(tasksDir, t, ".agent"))
	if err != nil {
		return "", 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return "", 0
	}
	since, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || since < 0 {
		return "", 0
	}
	return f[0], since
}

func ago(since int64) string {
	s := time.Now().Unix() - since
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%dh", s/3600)
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
