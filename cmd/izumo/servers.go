package main

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// server: a process listening on a TCP port that runs from one of the tasks' worktrees
type server struct {
	pid        int32
	created    int64 // with the pid, tells the process apart from a later one given the same pid
	ports      []uint32
	name       string
	task, repo string
}

func portList(ports []uint32) string {
	p := make([]string, len(ports))
	for i, v := range ports {
		p[i] = fmt.Sprint(v)
	}
	return strings.Join(p, ",")
}

// servers scans once per run: a done sweep asks about each task from the same scan
var servers = sync.OnceValue(func() []server {
	wts := map[string]string{} // task/repo as compared -> task/repo
	for _, t := range subdirs(tasksDir) {
		for _, r := range worktrees(t) {
			wts[normPath(t+"/"+r)] = t + "/" + r
		}
	}
	if len(wts) == 0 {
		return nil
	}
	conns, err := psnet.Connections("tcp")
	if err != nil {
		return nil
	}
	ports := map[int32][]uint32{}
	for _, c := range conns {
		if c.Status == "LISTEN" && c.Pid > 4 && !slices.Contains(ports[c.Pid], c.Laddr.Port) {
			ports[c.Pid] = append(ports[c.Pid], c.Laddr.Port)
		}
	}
	var out []server
	for pid, ps := range ports {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}
		cwd, _ := p.Cwd()
		cmdline, _ := p.Cmdline()
		tr, ok := place(tasksDir, wts, cwd, cmdline, fold)
		if !ok {
			continue
		}
		// the program, not the process name: on Linux node renames its main thread, which /proc reports
		name, err := p.Exe()
		if err != nil || name == "" {
			name, _ = p.Name()
		}
		created, _ := p.CreateTime()
		slices.Sort(ps)
		t, r, _ := strings.Cut(tr, "/")
		out = append(out, server{pid, created, ps, strings.TrimSuffix(path.Base(strings.ReplaceAll(name, `\`, "/")), ".exe"), t, r})
	}
	slices.SortFunc(out, func(a, b server) int { return int(a.pid - b.pid) })
	return out
})

// serversIn: the servers running from a task, or from one repo of it
func serversIn(t, repo string) []server {
	var out []server
	for _, s := range servers() {
		if s.task == t && (repo == "" || s.repo == repo) {
			out = append(out, s)
		}
	}
	return out
}

func describe(srv []server) string {
	l := make([]string, len(srv))
	for i, s := range srv {
		l[i] = fmt.Sprintf("%s on :%s in %s (pid %d)", s.name, strings.ReplaceAll(portList(s.ports), ",", " :"), s.repo, s.pid)
	}
	return strings.Join(l, ", ")
}

// place finds the worktree a process runs from: its folder, or a path in its command line that
// starts an argument (a dev server run from node_modules, --dir=...). A file in the task folder
// itself (its workspace, open in an editor) is no worktree.
func place(tasks string, wts map[string]string, cwd, cmdline string, fold bool) (string, bool) {
	dir := normFold(tasks, fold) + "/"
	worktree := func(rest string) (string, bool) {
		if j := strings.IndexAny(rest, " \"'"); j >= 0 {
			rest = rest[:j]
		}
		p := strings.SplitN(rest, "/", 3)
		if len(p) < 2 {
			return "", false
		}
		tr, ok := wts[p[0]+"/"+p[1]]
		return tr, ok
	}
	if c := normFold(cwd, fold) + "/"; strings.HasPrefix(c, dir) {
		if tr, ok := worktree(c[len(dir):]); ok {
			return tr, true
		}
	}
	s := normFold(cmdline, fold)
	for i := 0; ; {
		j := strings.Index(s[i:], dir)
		if j < 0 {
			return "", false
		}
		at := i + j
		if at == 0 || strings.ContainsRune(" \"'=", rune(s[at-1])) {
			if tr, ok := worktree(s[at+len(dir):]); ok {
				return tr, true
			}
		}
		i = at + 1
	}
}

// same: is p still the process the scan saw, and not a later one given its pid?
func same(p *process.Process, created int64) bool {
	c, err := p.CreateTime()
	return err == nil && c == created
}

// stopServers: on Windows with their children (a dev server's esbuild holds the folder too);
// elsewhere SIGTERM, then SIGKILL for what is still up after 5s
func stopServers(srv []server) {
	var procs []*process.Process
	for _, s := range srv {
		p, err := process.NewProcess(s.pid)
		if err != nil || !same(p, s.created) {
			continue
		}
		procs = append(procs, p)
		if windows {
			procs = append(procs, descendants(p, s.created)...)
		}
	}
	if windows {
		for _, p := range procs {
			_ = p.Kill()
		}
		return
	}
	for _, p := range procs {
		_ = p.Terminate()
	}
	for n := 0; n < 10 && anyRunning(procs); n++ {
		time.Sleep(500 * time.Millisecond)
	}
	for _, p := range procs {
		if ok, _ := p.IsRunning(); ok {
			_ = p.Kill()
		}
	}
}

// descendants: Windows keeps a dead parent's pid in its children, so a child must be younger
// than the parent to be its own
func descendants(p *process.Process, created int64) []*process.Process {
	kids, _ := p.Children()
	var out []*process.Process
	for _, k := range kids {
		if c, err := k.CreateTime(); err == nil && c >= created {
			out = append(out, k)
			out = append(out, descendants(k, c)...)
		}
	}
	return out
}

func anyRunning(procs []*process.Process) bool {
	for _, p := range procs {
		if ok, _ := p.IsRunning(); ok {
			return true
		}
	}
	return false
}
