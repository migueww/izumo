package main

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// server: a process listening on a TCP port that runs from one of the tasks' worktrees
type server struct {
	pid        int32
	ports      []uint32
	name       string
	task, repo string
}

func (s server) portList() string {
	p := make([]string, len(s.ports))
	for i, v := range s.ports {
		p[i] = fmt.Sprint(v)
	}
	return strings.Join(p, ",")
}

func servers() []server {
	wts := map[string]string{} // lowercase task/repo -> task/repo
	for _, t := range subdirs(tasksDir) {
		for _, r := range worktrees(t) {
			wts[strings.ToLower(t+"/"+r)] = t + "/" + r
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
		tr, ok := place(tasksDir, wts, cwd, cmdline)
		if !ok {
			continue
		}
		name, _ := p.Name()
		slices.Sort(ps)
		t, r, _ := strings.Cut(tr, "/")
		out = append(out, server{pid, ps, strings.TrimSuffix(path.Base(strings.ReplaceAll(name, `\`, "/")), ".exe"), t, r})
	}
	slices.SortFunc(out, func(a, b server) int { return int(a.pid - b.pid) })
	return out
}

// place finds the worktree a process runs from: its folder, or a path in its command line
// (a dev server run from node_modules). Paths compare without case, as on Windows and macOS.
// A file in the task folder itself (its workspace, open in an editor) is no worktree.
func place(tasks string, wts map[string]string, cwd, cmdline string) (string, bool) {
	dir := strings.ToLower(tasks) + "/"
	for _, s := range []string{cwd + "/", cmdline} {
		s = strings.ToLower(strings.ReplaceAll(s, `\`, "/"))
		i := strings.Index(s, dir)
		if i < 0 {
			continue
		}
		rest := s[i+len(dir):]
		if j := strings.IndexAny(rest, " \"'"); j >= 0 {
			rest = rest[:j]
		}
		p := strings.SplitN(rest, "/", 3)
		if len(p) >= 2 {
			if tr, ok := wts[p[0]+"/"+p[1]]; ok {
				return tr, true
			}
		}
	}
	return "", false
}

// stopServers: on Windows with their children (a dev server's esbuild holds the folder too);
// elsewhere SIGTERM, then SIGKILL for what is still up after 5s
func stopServers(srv []server) {
	var procs []*process.Process
	for _, s := range srv {
		p, err := process.NewProcess(s.pid)
		if err != nil {
			continue
		}
		procs = append(procs, p)
		if windows {
			procs = append(procs, descendants(p)...)
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

func descendants(p *process.Process) []*process.Process {
	kids, _ := p.Children()
	out := kids
	for _, k := range kids {
		out = append(out, descendants(k)...)
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
