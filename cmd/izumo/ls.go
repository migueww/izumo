package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
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
	Stale bool   `json:"stale"` // working for hours without a word: its session most likely died
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
	Conflict  bool       `json:"conflict"` // the open PR can't merge into its base
	Serving   []uint32   `json:"serving"`  // every port of the servers below
	Servers   []lsServer `json:"servers"`
	Behind    int        `json:"behind"` // a reference: commits its base got since; izumo pull catches up
	NoPR      int64      `json:"-"`      // pushed, no PR: the last commit's time
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
	gapc := make(chan []gap, 1)
	go func() {
		if o.has("--json") || !tty() || len(o.args) > 0 {
			gapc <- nil
			return
		}
		gapc <- parallel(len(clones), func(i int) gap { return gapOf(clones[i]) })
	}()
	var read atomic.Int32
	stop := gather("gathering", &read, len(jobs))
	states := parallel(len(jobs), func(i int) repoState {
		defer read.Add(1)
		s, err := stateOf(jobs[i].t, jobs[i].repo)
		if err != nil {
			say("%v", err)
			return repoState{branch: "?", stage: stageError}
		}
		return s
	})
	srv, gaps := <-srvc, <-gapc
	stop()

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
				Conflict: s.conflict, NoPR: s.noPR, Serving: []uint32{}, Servers: []lsServer{}}
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
		// done keeps a task with files of its own in the folder, so it isn't ready either
		lt.Ready = ready && shipped && !lt.Removing && len(unknownFiles(t.name)) == 0
		list = append(list, lt)
	}

	if o.has("--json") {
		e := json.NewEncoder(os.Stdout)
		e.SetEscapeHTML(false)
		e.SetIndent("", "  ")
		return e.Encode(list)
	}
	printLs(list, len(o.args) == 0, gaps)
	return nil
}

// printLs: on a terminal it reads at a glance: a summary, the tasks that need you first, quiet
// repos folded into one line, goals cut at the width and the tasks done would take at the end,
// one line each, then the repos whose base has work prod doesn't. Piped, every task keeps its rows, order and whole goal, for scripts. Tasks asked
// for by name keep their rows too: whoever names one wants to see it.
func printLs(list []lsTask, all bool, gaps []gap) {
	if len(list) == 0 {
		if tty() {
			fmt.Printf("%s the shrine is quiet. Start a task: izumo new <task> <repo...>\n", paint(vermilion, "⛩"))
		}
		return
	}
	var open, ready []lsTask
	need, serving := 0, 0
	for _, t := range list {
		if t.Ready && tty() && all {
			ready = append(ready, t)
		} else {
			open = append(open, t)
			need += b2i(needsYou(t))
		}
		for _, r := range t.Repos {
			serving += len(r.Servers)
		}
	}
	if tty() {
		slices.SortStableFunc(open, func(a, b lsTask) int { return b2i(needsYou(b)) - b2i(needsYou(a)) })
		if all && len(list) > 1 {
			sum := []string{fmt.Sprintf("%d open", len(open))}
			if need > 0 {
				sum = append(sum, paint(red, fmt.Sprintf("%d need you", need)))
			}
			if len(ready) > 0 {
				sum = append(sum, paint(green, fmt.Sprintf("%d going home", len(ready))))
			}
			if serving > 0 {
				sum = append(sum, paint(cyan, fmt.Sprintf("%d servers up", serving)))
			}
			fmt.Printf("%s %s\n\n", paint(vermilion, "⛩"), strings.Join(sum, paint(dim, " · ")))
		}
	}
	// columns as wide as the longest repo and branch shown, so long names don't shift the rest
	rw, bw := 0, 0
	for _, t := range open {
		for _, r := range t.Repos {
			if !quiet(r) {
				rw, bw = max(rw, len(r.Repo)), max(bw, len(r.Branch))
			}
		}
	}
	for i, t := range open {
		if i > 0 && tty() {
			fmt.Println()
		}
		head, n := paint(bold, t.Task), len(t.Task)
		tag := func(code, s string) { head += "  " + paint(code, s); n += 2 + len(s) }
		if t.Ready {
			tag(green, "[ready: izumo done]")
		}
		if t.Agent != nil {
			tag(agentTag(t))
		}
		if t.Removing {
			tag(red, fmt.Sprintf("[removal stopped halfway: izumo rm %s]", t.Task))
		}
		fmt.Printf("%s  %s\n", head, clip(t.Goal, width()-n-2))
		folded := map[stage][]string{}
		for _, r := range t.Repos {
			if quiet(r) {
				folded[r.Stage] = append(folded[r.Stage], r.Repo)
			} else {
				fmt.Println(repoLine(r, rw, bw))
			}
		}
		for _, s := range []stage{stageEmpty, stageRef} {
			if len(folded[s]) > 0 {
				fmt.Println("  " + paint(dim, clip(string(s)+": "+strings.Join(folded[s], ", "), width()-2)))
			}
		}
	}
	if len(ready) > 0 {
		if len(open) > 0 {
			fmt.Println()
		}
		// the gathering is over for these: like the gods leaving Izumo, they go home
		fmt.Printf("%s %s %s\n", paint(vermilion, "⛩"), paint(green, fmt.Sprintf("ready to go home (%d)", len(ready))),
			paint(dim, "· izumo done"))
		for _, t := range ready {
			head := paint(bold, t.Task)
			if t.Agent != nil {
				code, s := agentTag(t)
				head += "  " + paint(code, s)
			}
			for _, r := range t.Repos {
				if s := servingFlags(r); s != "" {
					head += " " + s
				}
			}
			fmt.Println("  " + head)
		}
	}
	gaps = slices.DeleteFunc(gaps, func(g gap) bool { return g.n == 0 })
	slices.SortFunc(gaps, func(a, b gap) int { return int(a.oldest - b.oldest) }) // longest wait first
	if len(gaps) > 0 {
		// merged but not promoted: a merge that came in just after a promotion waits here unseen
		fmt.Printf("\n%s %s\n", paint(vermilion, "⛩"), paint(yellow, fmt.Sprintf("waiting for prod (%d)", len(gaps))))
		w := 0
		for _, g := range gaps {
			w = max(w, len(g.repo))
		}
		for _, g := range gaps {
			fmt.Printf("  %-*s %s\n", w, g.repo, paint(dim, fmt.Sprintf("%d %s in %s, not in %s · oldest %s",
				g.n, map[bool]string{true: "commit", false: "commits"}[g.n == 1], g.base, g.prod, ago(g.oldest))))
		}
	}
}

// needsYou: the task waits on you, not on others: changes asked in review, failing CI, a
// conflict, commits only here, a branch pushed a day ago with no PR, an agent asking, or a
// removal stopped halfway
func needsYou(t lsTask) bool {
	if t.Removing || (t.Agent != nil && t.Agent.State == "waiting") {
		return true
	}
	for _, r := range t.Repos {
		if r.Review == "changes-requested" || r.CI == "failing" || r.Conflict || r.Unpushed > 0 || forgotten(r) {
			return true
		}
	}
	return false
}

// quiet: a repo with nothing to show beyond its stage, which a terminal folds into one line per
// stage. Only on a terminal: piped, every repo keeps its row.
func quiet(r lsRepo) bool {
	return tty() && (r.Stage == stageEmpty || r.Stage == stageRef) &&
		r.Modified+r.Untracked+r.Unpushed+r.Behind == 0 && len(r.Servers) == 0
}

// forgotten: pushed over a day ago and still no PR. Younger, the PR is likely on its way.
func forgotten(r lsRepo) bool { return r.NoPR > 0 && time.Now().Unix()-r.NoPR > 86400 }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// agentTag: what the task's agent is doing, and its color
func agentTag(t lsTask) (code, s string) {
	if t.Agent.Stale {
		return dim, fmt.Sprintf("[agent stale: working %s]", ago(t.Agent.Since))
	}
	return map[string]string{"working": cyan, "waiting": yellow, "done": dim}[t.Agent.State],
		fmt.Sprintf("[agent %s %s]", t.Agent.State, ago(t.Agent.Since))
}

// clip: s cut to n columns with an ellipsis; whole when the width is unknown
func clip(s string, n int) string {
	if width() == 0 || cols(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	w := 0
	for i, r := range s {
		if w += runeCols(r); w > n-1 {
			return s[:i] + "…"
		}
	}
	return ""
}

// cols: the columns s takes on a terminal
func cols(s string) int {
	n := 0
	for _, r := range s {
		n += runeCols(r)
	}
	return n
}

// runeCols: 2 for East Asian wide characters and emoji, 0 for what joins the one before (marks,
// variation selectors, skin tones, the zero-width joiner), else 1.
// ponytail: ranges by hand, not the whole Unicode table; a rare symbol outside them counts 1
func runeCols(r rune) int {
	switch {
	case r >= 0x300 && r <= 0x36F, r == 0x200D, r >= 0xFE00 && r <= 0xFE0F, r >= 0x1F3FB && r <= 0x1F3FF:
		return 0
	case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF && r != 0x303F, r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE4F, r >= 0xFF00 && r <= 0xFF60, r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, r >= 0x1F680 && r <= 0x1F6FF, r >= 0x1F900 && r <= 0x1FAFF,
		r >= 0x20000 && r <= 0x3FFFD, strings.ContainsRune(wideSymbols, r):
		return 2
	}
	return 1
}

// the symbols below U+3000 that terminals draw as emoji, two columns wide
const wideSymbols = "⌚⌛⏩⏪⏫⏬⏰⏳◽◾☔☕♈♉♊♋♌♍♎♏♐♑♒♓♿⚓⚡⚪⚫⚽⚾⛄⛅⛎⛔⛪⛲⛳⛵⛺⛽✅✊✋✨❌❎❓❔❕❗➕➖➗➰➿⬛⬜⭐⭕"

// servingFlags: each server with how long it has been up; past two days it turns red, likely
// forgotten
func servingFlags(r lsRepo) string {
	s := ""
	for _, v := range r.Servers {
		age := dim
		if time.Now().Unix()-v.Since > 2*86400 {
			age = red
		}
		s += " " + paint(cyan, "serving:"+portLinks(v.Ports)) + " " + paint(age, "("+ago(v.Since)+")")
	}
	return s
}

func repoLine(r lsRepo, rw, bw int) string {
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
	if r.Conflict {
		flags += " " + paint(red, "conflict")
	}
	if forgotten(r) {
		flags += " " + paint(red, "no-pr ("+ago(r.NoPR)+")")
	}
	flags += servingFlags(r)
	if r.Behind > 0 {
		flags += " " + paint(yellow, strconv.Itoa(r.Behind)+"-behind")
	}
	// a terminal that shows links gets #123 opening the PR; anywhere else, the whole URL
	pr := r.PR
	if pr != "" && color() {
		pr = link(pr, "#"+path.Base(pr))
	}
	// padded before painting, so the columns line up with or without color
	return fmt.Sprintf("  %-*s %s %s%s %s", rw, r.Repo, paint(dim, fmt.Sprintf("%-*s", bw, r.Branch)),
		paint(stageColor[r.Stage], fmt.Sprintf("%-6s", r.Stage)), flags, paint(dim, pr))
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
	// working stops at every reply (Stop), so a turn this long is a session that never sent it
	return &agent{f[0], since, f[0] == "working" && time.Now().Unix()-since > 6*3600}
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
		l[i] = link(fmt.Sprintf("http://localhost:%d", p), strconv.Itoa(int(p)))
	}
	return strings.Join(l, ",")
}

// contextHook is the SessionStart hook (hooks/hooks.json): inside an izumo root, tell the agent so
// before its first move. The skill loads only when the model picks it, and a request like "get it
// running" can lead it to another skill that never asks izumo what is already up.
func contextHook() {
	if _, err := findRoot(); err != nil {
		return
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "SessionStart",
		"additionalContext": "This folder is an izumo workspace: each task is a folder in tasks/ with one git worktree " +
			"per repo. Before looking through repos one by one, or starting a dev server, use the izumo skill and run " +
			"`izumo ls --json` (name a task to narrow it): in one call it gives each repo's branch, pull request, CI " +
			"and conflicts, whether the work reached the base branch and production, what exists only on this " +
			"machine, and the dev servers already running from each worktree.",
	}})
	fmt.Println(string(out))
}

// agentHook is the Claude Code hook (hooks/hooks.json): record what the agent in this task is
// doing. It fires in every session, so it stays cheap, silent and exits 0 outside a task.
//
// A session started at the root moves between tasks as it cd's, and a Stop sent from elsewhere
// never reached the task it left: that task said "working" for days. So .agent names the
// session, and any event of that session clears what it left in the tasks it is no longer in.
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
	session := hookSession()
	task, root := "", ""
	wd, _ := os.Getwd()
	for _, start := range []string{wd, os.Getenv("CLAUDE_PROJECT_DIR")} {
		if start == "" || task != "" {
			continue
		}
		d, err := realPath(start)
		if err != nil {
			continue
		}
		for ; path.Dir(d) != d; d = path.Dir(d) {
			if task == "" && isFile(path.Join(d, ".task")) && isFile(path.Join(path.Dir(path.Dir(d)), ".izumo")) {
				task = d
			}
			if root == "" && isFile(path.Join(d, ".izumo")) {
				root = d
			}
		}
	}
	if task != "" {
		f := path.Join(task, ".agent")
		if state == "end" {
			os.Remove(f)
		} else {
			_ = os.WriteFile(f, []byte(fmt.Sprintf("%s %d %s\n", state, time.Now().Unix(), session)), 0o644)
		}
	}
	if session == "" || root == "" {
		return
	}
	for _, t := range subdirs(path.Join(root, "tasks")) {
		d := path.Join(root, "tasks", t)
		if d == task {
			continue
		}
		b, _ := os.ReadFile(path.Join(d, ".agent"))
		if f := strings.Fields(string(b)); len(f) >= 3 && f[2] == session {
			os.Remove(path.Join(d, ".agent"))
		}
	}
}

// hookSession: the session_id Claude Code passes a hook on stdin; "" without one (a terminal, or
// an id that would not fit in .agent's one line)
func hookSession() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in)
	if strings.ContainsAny(in.SessionID, " \t\r\n") {
		return ""
	}
	return in.SessionID
}
