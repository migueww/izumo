package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
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

// fetch prunes: a remote branch deleted on GitHub must stop counting as "pushed"
func fetch(clone string) {
	if !gitOK(clone, "fetch", "--quiet", "--prune", "origin") {
		say("%s: fetch failed, using local refs", path.Base(clone))
	}
}

// baseRef: origin/<branch> work starts from: task base, else BASE, else origin's default branch.
// A repo without that branch on origin (mixed roots) falls back to its default branch.
func baseRef(clone, b string) (string, error) {
	if b == "" {
		b = cfgBase
	}
	if b != "" && hasRef(clone, "refs/remotes/origin/"+b) {
		return "origin/" + b, nil
	}
	if r, err := gitOut(clone, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && r != "" {
		return r, nil
	}
	return "", fmt.Errorf("%s: origin/HEAD unknown; set BASE in .izumo or run: git -C '%s' remote set-head origin -a", path.Base(clone), clone)
}

func prodRef(clone, b string) (string, error) {
	if cfgProd != "" && hasRef(clone, "refs/remotes/origin/"+cfgProd) {
		return "origin/" + cfgProd, nil
	}
	return baseRef(clone, b)
}

// onOrigin: is origin/<branch> there, fetching once if not?
func onOrigin(clone, b string) bool {
	return hasRef(clone, "refs/remotes/origin/"+b) ||
		gitOK(clone, "fetch", "--quiet", "origin") && hasRef(clone, "refs/remotes/origin/"+b)
}

// checkBase: a base asked for by name must be on origin in at least one repo; otherwise it is
// a typo or an unpushed branch, and the default branch would stand in silently
func checkBase(b string, repos []string) {
	clones := 0
	for _, r := range repos {
		if !isDir(path.Join(root, r, ".git")) {
			continue
		}
		clones++
		if onOrigin(path.Join(root, r), b) {
			return
		}
	}
	if clones > 0 {
		die("no repo has origin/%s; push it first, or check the name", b)
	}
}

type repoState struct {
	branch, stage                 string // stage: empty|wip|pr|base|prod|detached, error if unknown
	head                          string
	modified, untracked, unpushed int
	pr, review, ci                string
}

func (s repoState) dirty() bool { return s.modified+s.untracked > 0 }

func stateOf(t task, repo string) (repoState, error) {
	wt, src := path.Join(tasksDir, t.name, repo), path.Join(root, repo)
	var s repoState
	s.branch, _ = gitOut(wt, "branch", "--show-current")
	status, err := gitCmd(wt, "status", "--porcelain").Output()
	if err != nil {
		return s, fmt.Errorf("%s: git status failed", repo)
	}
	for _, l := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(l, "??") {
			s.untracked++
		} else if l != "" {
			s.modified++
		}
	}
	head, _ := gitOut(wt, "rev-parse", "HEAD")
	s.head = head
	n, _ := gitOut(wt, "rev-list", "--count", "HEAD", "--not", "--remotes")
	s.unpushed, _ = strconv.Atoi(n)
	if s.branch == "" {
		s.stage = "detached"
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
	case head == created:
		s.stage = "empty"
	case isAncestor(wt, head, prodR):
		s.stage = "prod"
	case isAncestor(wt, head, baseR):
		s.stage = "base"
	default:
		s.stage = "wip"
	}
	if s.stage == "empty" || s.stage == "prod" {
		return s, nil
	}
	pr := prOf(wt, s.branch)
	if pr == nil {
		return s, nil
	}
	s.pr = pr.url
	switch pr.state {
	case "OPEN":
		s.stage = "pr"
		s.review = map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes-requested", "REVIEW_REQUIRED": "review-required"}[pr.review]
		if pr.ci != "-" {
			s.ci = pr.ci
		}
	case "MERGED":
		// gh matches by branch name: an old PR of a reused name, or a fork's, says nothing about this work
		if pr.head != head && !isAncestor(wt, head, pr.head) {
			s.pr = ""
			return s, nil
		}
		s.stage = "base"
		if isAncestor(wt, pr.merge, prodR) {
			s.stage = "prod"
		}
		// squash merge: the branch commits live on in the PR, so nothing is lost
		if pr.head == head {
			s.unpushed = 0
		}
	}
	return s, nil
}

// one TSV line per PR: url state mergeCommit head review ci, "-" for empty fields
const prJQ = `.[0] // empty | (.statusCheckRollup // []) as $c | [
  .url, .state, (.mergeCommit.oid // "-"), .headRefOid,
  (if (.reviewDecision // "") == "" then "-" else .reviewDecision end),
  (if ($c | length) == 0 then "-"
   elif any($c[]; (.conclusion // .state // "") | test("FAILURE|ERROR|TIMED_OUT|CANCELLED|ACTION_REQUIRED|STARTUP_FAILURE")) then "failing"
   elif any($c[]; (.status // "COMPLETED") != "COMPLETED" or (.state // "") == "PENDING" or (.state // "") == "EXPECTED") then "running"
   else "passing" end)
] | @tsv`

type pr struct{ url, state, merge, head, review, ci string }

func prOf(wt, branch string) *pr {
	c := ghCmd("pr", "list", "--head", branch, "--state", "all", "--limit", "1",
		"--json", "url,state,mergeCommit,headRefOid,reviewDecision,statusCheckRollup", "--jq", prJQ)
	if c == nil {
		return nil
	}
	c.Dir = wt
	out, err := c.Output()
	line := strings.TrimSpace(string(bytes.TrimRight(out, "\r\n")))
	if err != nil || line == "" {
		return nil
	}
	f := append(strings.Split(line, "\t"), "", "", "", "", "", "")
	return &pr{f[0], f[1], f[2], f[3], f[4], f[5]}
}

// ghCmd: gh, or IZUMO_GH; nil when it isn't installed. A script given as IZUMO_GH runs
// through bash on Windows, which can't run it by itself.
func ghCmd(args ...string) *exec.Cmd {
	gh := os.Getenv("IZUMO_GH")
	if gh == "" {
		gh = "gh"
	}
	if p, err := exec.LookPath(gh); err == nil {
		return exec.Command(p, args...)
	}
	if windows && isFile(gh) {
		return exec.Command(findBash(), append([]string{gh}, args...)...)
	}
	return nil
}
