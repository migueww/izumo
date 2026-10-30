package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

func gitCmd(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

// gitOut: trimmed stdout, stderr dropped
func gitOut(dir string, args ...string) (string, error) {
	out, err := gitCmd(dir, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// gitOK: did it succeed, output dropped
func gitOK(dir string, args ...string) bool { return gitCmd(dir, args...).Run() == nil }

// gitRun: stderr shown, as git's own messages explain failures best
func gitRun(dir string, args ...string) error {
	c := gitCmd(dir, args...)
	c.Stderr = os.Stderr
	return c.Run()
}

func isAncestor(dir, a, b string) bool { return gitOK(dir, "merge-base", "--is-ancestor", a, b) }

func hasRef(dir, ref string) bool { return gitOK(dir, "show-ref", "--verify", "--quiet", ref) }

// origin as a clone last fetched it: its branches and default branch, read once per clone and
// read again after a fetch
type origin struct {
	branches map[string]bool
	head     string // origin/<default branch>, empty when origin/HEAD is unknown
}

var origins sync.Map // clone -> *origin

func originOf(clone string) *origin {
	if o, ok := origins.Load(clone); ok {
		return o.(*origin)
	}
	o := &origin{branches: map[string]bool{}}
	refs, _ := gitOut(clone, "for-each-ref", "--format=%(refname:lstrip=3)", "refs/remotes/origin/")
	for _, b := range strings.Split(refs, "\n") {
		if b != "" && b != "HEAD" {
			o.branches[b] = true
		}
	}
	o.head, _ = gitOut(clone, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	actual, _ := origins.LoadOrStore(clone, o)
	return actual.(*origin)
}

type fetchOnce struct {
	sync.Once
	ok bool
}

var fetches sync.Map // clone -> *fetchOnce: one fetch per clone and run is enough

// fetch prunes: a remote branch deleted on GitHub must stop counting as "pushed"
func fetch(clone string) bool {
	v, _ := fetches.LoadOrStore(clone, &fetchOnce{})
	f := v.(*fetchOnce)
	f.Do(func() {
		f.ok = gitOK(clone, "fetch", "--quiet", "--prune", "origin")
		origins.Delete(clone)
		if !f.ok {
			say("%s: fetch failed, using local refs", path.Base(clone))
		}
	})
	return f.ok
}

// fetchAll fetches the root's clones of these repos in parallel
func fetchAll(repos []string) {
	parallel(len(repos), func(i int) bool { return fetch(path.Join(root, repos[i])) })
}

// baseRef: origin/<branch> work starts from: task base, else BASE, else origin's default branch.
// A repo without that branch on origin (mixed roots) falls back to its default branch.
func baseRef(clone, b string) (string, error) {
	if b == "" {
		b = cfgBase
	}
	o := originOf(clone)
	if b != "" && o.branches[b] {
		return "origin/" + b, nil
	}
	if o.head != "" {
		return o.head, nil
	}
	return "", fmt.Errorf("%s: origin/HEAD unknown; set BASE in .izumo or run: git -C '%s' remote set-head origin -a", path.Base(clone), clone)
}

func prodRef(clone, b string) (string, error) {
	if cfgProd != "" && originOf(clone).branches[cfgProd] {
		return "origin/" + cfgProd, nil
	}
	return baseRef(clone, b)
}

// onOrigin: is origin/<branch> there, fetching once if not?
func onOrigin(clone, b string) bool {
	return originOf(clone).branches[b] || fetch(clone) && originOf(clone).branches[b]
}

// checkBase: a base asked for by name must be on origin in at least one repo; otherwise it is
// a typo or an unpushed branch, and the default branch would stand in silently
func checkBase(b string, repos []string) error {
	clones := 0
	for _, r := range repos {
		if !isDir(path.Join(root, r, ".git")) {
			continue
		}
		clones++
		if onOrigin(path.Join(root, r), b) {
			return nil
		}
	}
	if clones > 0 {
		return fmt.Errorf("no repo has origin/%s; push it first, or check the name", b)
	}
	return nil
}

// stage: how far a repo's work went
type stage string

const (
	stageEmpty    stage = "empty" // no commits yet
	stageWip      stage = "wip"
	stagePR       stage = "pr"   // open PR
	stageBase     stage = "base" // merged, not shipped
	stageProd     stage = "prod"
	stageDetached stage = "detached"
	stageError    stage = "error" // couldn't tell
)

type repoState struct {
	branch, head                  string
	stage                         stage
	modified, untracked, unpushed int
	pr, review, ci                string
}

func (s repoState) dirty() bool { return s.modified+s.untracked > 0 }

func stateOf(t task, repo string) (repoState, error) {
	wt, src := path.Join(tasksDir, t.name, repo), path.Join(root, repo)
	var s repoState
	// one call for branch, commit and changes
	status, err := gitCmd(wt, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return s, fmt.Errorf("%s: git status failed", repo)
	}
	for _, l := range strings.Split(string(status), "\n") {
		switch {
		case strings.HasPrefix(l, "# branch.oid "):
			s.head = strings.TrimPrefix(l, "# branch.oid ")
		case strings.HasPrefix(l, "# branch.head "):
			if b := strings.TrimPrefix(l, "# branch.head "); b != "(detached)" {
				s.branch = b
			}
		case strings.HasPrefix(l, "? "):
			s.untracked++
		case l != "" && l[0] != '#' && l[0] != '!':
			s.modified++
		}
	}
	n, _ := gitOut(wt, "rev-list", "--count", "HEAD", "--not", "--remotes")
	s.unpushed, _ = strconv.Atoi(n)
	if s.branch == "" {
		s.stage = stageDetached
		return s, nil
	}
	// oldest reflog entry = where the branch started; still there means no work on it
	reflog, _ := gitOut(wt, "log", "-g", "--format=%H", "refs/heads/"+s.branch)
	created := reflog[strings.LastIndex(reflog, "\n")+1:]
	baseR, err := baseRef(src, t.base)
	if err != nil {
		return s, err
	}
	prodR, err := prodRef(src, t.base)
	if err != nil {
		return s, err
	}
	switch {
	case s.head == created:
		s.stage = stageEmpty
	case isAncestor(wt, s.head, prodR):
		s.stage = stageProd
	case isAncestor(wt, s.head, baseR):
		s.stage = stageBase
	default:
		s.stage = stageWip
	}
	if s.stage == stageEmpty || s.stage == stageProd {
		return s, nil
	}
	p := prOf(wt, s.branch)
	if p == nil {
		return s, nil
	}
	switch p.State {
	case "OPEN":
		s.stage, s.pr = stagePR, p.URL
		s.review = map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes-requested", "REVIEW_REQUIRED": "review-required"}[p.ReviewDecision]
		s.ci = p.ci()
	case "MERGED":
		// gh matches by branch name: an old PR of a reused name, or a fork's, says nothing about this work
		if p.HeadRefOid != s.head && !isAncestor(wt, s.head, p.HeadRefOid) {
			return s, nil
		}
		s.stage, s.pr = stageBase, p.URL
		if p.MergeCommit != nil && isAncestor(wt, p.MergeCommit.Oid, prodR) {
			s.stage = stageProd
		}
		// squash merge: the branch commits live on in the PR, so nothing is lost
		if p.HeadRefOid == s.head {
			s.unpushed = 0
		}
	}
	return s, nil
}

type pr struct {
	URL         string `json:"url"`
	State       string `json:"state"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	HeadRefOid        string `json:"headRefOid"`
	ReviewDecision    string `json:"reviewDecision"`
	StatusCheckRollup []struct {
		Conclusion string `json:"conclusion"` // check runs
		State      string `json:"state"`      // commit statuses
		Status     string `json:"status"`
	} `json:"statusCheckRollup"`
}

var ciFailed = regexp.MustCompile(`FAILURE|ERROR|TIMED_OUT|CANCELLED|ACTION_REQUIRED|STARTUP_FAILURE`)

// ci: failing if any check failed, running if any hasn't finished, else passing; empty without checks
func (p pr) ci() string {
	if len(p.StatusCheckRollup) == 0 {
		return ""
	}
	running := false
	for _, c := range p.StatusCheckRollup {
		r := c.Conclusion
		if r == "" {
			r = c.State
		}
		if ciFailed.MatchString(r) {
			return "failing"
		}
		running = running || c.Status != "" && c.Status != "COMPLETED" || c.State == "PENDING" || c.State == "EXPECTED"
	}
	if running {
		return "running"
	}
	return "passing"
}

// prOf: the latest PR whose head is this branch, nil without gh or a PR
func prOf(wt, branch string) *pr {
	gh := os.Getenv("IZUMO_GH")
	if gh == "" {
		gh = "gh"
	}
	bin, err := exec.LookPath(gh)
	if err != nil {
		return nil
	}
	c := exec.Command(bin, "pr", "list", "--head", branch, "--state", "all", "--limit", "1",
		"--json", "url,state,mergeCommit,headRefOid,reviewDecision,statusCheckRollup")
	c.Dir = wt
	out, err := c.Output()
	var prs []pr
	if err != nil || json.Unmarshal(out, &prs) != nil || len(prs) == 0 {
		return nil
	}
	return &prs[0]
}
