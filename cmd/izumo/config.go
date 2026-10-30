package main

import (
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

var (
	root, tasksDir, configFile string

	// from .izumo
	cfgBase, cfgProd, cfgPrefix, cfgTemplate string
	hasPostAdd, hasPreRemove                 bool
)

func findRoot() (string, error) {
	d := cwd()
	for !isFile(path.Join(d, ".izumo")) {
		parent := path.Dir(d)
		if parent == d {
			return "", os.ErrNotExist
		}
		d = parent
	}
	return d, nil
}

func setup() {
	r := os.Getenv("IZUMO_ROOT")
	if r == "" {
		var err error
		if r, err = findRoot(); err != nil {
			wd, _ := os.Getwd()
			die("no .izumo above %s; create one at your multi-repo root: touch .izumo", wd)
		}
	}
	var err error
	if root, err = realPath(r); err != nil || !isFile(path.Join(root, ".izumo")) {
		die("no .izumo in %s", r)
	}
	tasksDir = path.Join(root, "tasks")
	configFile = path.Join(root, ".izumo")
	loadConfig()
}

// .izumo is bash: variables and hook functions. bash reads it, so roots written for the
// bash izumo keep working, and the hooks run where they were written to run.
func loadConfig() {
	if st, err := os.Stat(configFile); err == nil && st.Size() == 0 {
		return // the common `touch .izumo`: no need to start bash
	}
	out, err := bash(`. "$IZUMO_CONFIG" >&2
printf '%s\0' "${BASE-}" "${PROD-}" "${PREFIX-}" "${TASK_TEMPLATE-}"
declare -F post_add >/dev/null && printf post_add; printf '\0'
declare -F pre_remove >/dev/null && printf pre_remove; printf '\0'`).Output()
	f := strings.Split(string(out), "\x00")
	if err != nil || len(f) < 6 {
		die("could not read %s", configFile)
	}
	cfgBase, cfgProd, cfgPrefix, cfgTemplate = f[0], f[1], f[2], f[3]
	hasPostAdd, hasPreRemove = f[4] != "", f[5] != ""
	if cfgTemplate != "" && !strings.HasPrefix(cfgTemplate, "/") && !(len(cfgTemplate) > 1 && cfgTemplate[1] == ':') {
		cfgTemplate = path.Join(root, cfgTemplate)
	}
}

// findBash runs once: ls reaches it from parallel goroutines
var findBash = sync.OnceValue(func() string {
	if p := os.Getenv("IZUMO_BASH"); p != "" {
		return p
	}
	// on Windows, bash on PATH may be WSL's; Git for Windows ships its own next to git
	if windows {
		if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
			for d := strings.TrimSpace(strings.ReplaceAll(string(out), `\`, "/")); path.Dir(d) != d; d = path.Dir(d) {
				for _, c := range []string{"bin/bash.exe", "usr/bin/bash.exe"} {
					if isFile(path.Join(d, c)) {
						return path.Join(d, c)
					}
				}
			}
		}
	}
	p, err := exec.LookPath("bash")
	if err != nil {
		die("bash not found: izumo reads .izumo with it (set IZUMO_BASH to its path)")
	}
	return p
})

// bash runs a script with the root's variables in the environment and $1... set to args
func bash(script string, args ...string) *exec.Cmd {
	c := exec.Command(findBash(), append([]string{"-c", script, "izumo"}, args...)...)
	c.Env = append(os.Environ(), "IZUMO_CONFIG="+configFile, "ROOT="+root, "TASKS="+tasksDir)
	return c
}

// inWt runs a command (a hook, or any command) inside the worktree with .izumo loaded
// and TASK REPO SRC WT BRANCH set
func inWt(task, repo string, stdout io.Writer, argv ...string) error {
	wt := path.Join(tasksDir, task, repo)
	branch, _ := gitOut(wt, "branch", "--show-current")
	c := bash(`post_add() { :; }; pre_remove() { :; }; . "$IZUMO_CONFIG"; "$@"`, argv...)
	c.Dir = wt
	c.Env = append(c.Env, "TASK="+task, "REPO="+repo, "SRC="+path.Join(root, repo), "WT="+wt, "BRANCH="+branch)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, os.Stderr
	return c.Run()
}

func hook(name, task, repo string, stdout io.Writer) error {
	if name == "post_add" && !hasPostAdd || name == "pre_remove" && !hasPreRemove {
		return nil
	}
	return inWt(task, repo, stdout, name)
}
