package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// task: tasks/<name>/.task holds its branch and base
type task struct{ name, branch, base string }

func loadTask(name string) (task, error) {
	if err := validName(name); err != nil {
		return task{}, err
	}
	b, err := os.ReadFile(path.Join(tasksDir, name, ".task"))
	if err != nil {
		return task{}, fmt.Errorf("no task '%s'", name)
	}
	t := task{name: name}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(l, "\r"), "=")
		switch k {
		case "T_BRANCH":
			t.branch = unquote(v)
		case "T_BASE":
			t.base = unquote(v)
		}
	}
	return t, nil
}

// .task holds shell assignments (the bash izumo sources it): single quotes or bash's %q
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

var plain = regexp.MustCompile(`^[A-Za-z0-9._/@+,:=-]+$`)

func shellQuote(v string) string {
	if plain.MatchString(v) {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func saveTask(t task) error {
	s := fmt.Sprintf("T_BRANCH=%s\nT_BASE=%s\n", shellQuote(t.branch), shellQuote(t.base))
	if err := os.WriteFile(path.Join(tasksDir, t.name, ".task"), []byte(s), 0o644); err != nil {
		return fmt.Errorf("could not write %s/.task: %v", t.name, err)
	}
	return nil
}

// taskFromCwd: the task given, else the one the current folder is in
func taskFromCwd(given string) (string, error) {
	if given != "" {
		return given, nil
	}
	p := cwd()
	if under(p, tasksDir) && len(p) > len(tasksDir)+1 {
		t, _, _ := strings.Cut(p[len(tasksDir)+1:], "/")
		return t, nil
	}
	return "", fmt.Errorf("not inside a task folder; pass --task")
}

// notRemoving: a removal that stopped halfway lists what it removes; changing the task before it
// finishes would leave the change out of that list
func notRemoving(t string) error {
	if exists(path.Join(tasksDir, t, ".removing")) {
		return fmt.Errorf("%s: a removal stopped halfway; finish it with izumo rm %s", t, t)
	}
	return nil
}

// subdirs: folders in dir, hidden ones left out, sorted
func subdirs(dir string) []string {
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

func allTasks() []string {
	var out []string
	for _, d := range subdirs(tasksDir) {
		if isFile(path.Join(tasksDir, d, ".task")) {
			out = append(out, d)
		}
	}
	return out
}

// allClones: the root's folders with a .git directory and a name izumo accepts
func allClones() []string {
	var out []string
	for _, d := range subdirs(root) {
		if validName(d) == nil && isDir(path.Join(root, d, ".git")) {
			out = append(out, d)
		}
	}
	return out
}

func worktrees(t string) []string {
	var out []string
	for _, d := range subdirs(path.Join(tasksDir, t)) {
		if isFile(path.Join(tasksDir, t, d, ".git")) {
			out = append(out, d)
		}
	}
	return out
}

func writeJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		err = os.WriteFile(file, append(b, '\n'), 0o644)
	}
	return err
}

func writeWorkspace(t string) error {
	type folder struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	ws := struct {
		Folders []folder `json:"folders"`
	}{Folders: []folder{}}
	for _, r := range worktrees(t) {
		br, _ := gitOut(path.Join(tasksDir, t, r), "branch", "--show-current")
		ws.Folders = append(ws.Folders, folder{r + " (" + br + ")", r})
	}
	if err := writeJSON(path.Join(tasksDir, t, t+".code-workspace"), ws); err != nil {
		return fmt.Errorf("could not write the workspace of %s: %v", t, err)
	}
	return nil
}

const agentsMD = "This folder is an izumo task: each subfolder is a git worktree of one repo, all on\n" +
	"the task's branch. Read TASK.md first and keep it current. It holds only what git\n" +
	"can't tell: goal, status / next step, decisions and open items. Repos, branches and\n" +
	"PR state come from `izumo ls <task>`, so never write them here: the next step is the\n" +
	"next thing a person does. Need another repo? Run `izumo add <repo>` from here;\n" +
	"one no longer needed? `izumo drop <repo>`.\n"

func createTask(t task) error {
	dir := path.Join(tasksDir, t.name)
	if exists(dir) {
		return fmt.Errorf("task '%s' already exists", t.name)
	}
	md := fmt.Sprintf("# %s\n\n## Goal\n\n## Status / next step\n\n## Decisions\n\n## Open items\n", t.name)
	if cfgTemplate != "" {
		tpl, err := os.ReadFile(cfgTemplate)
		if err != nil {
			return fmt.Errorf("TASK_TEMPLATE not found: %s", cfgTemplate)
		}
		md = strings.ReplaceAll(string(tpl), "{{task}}", t.name)
	}
	if !gitOK(root, "check-ref-format", "--branch", t.branch) {
		return fmt.Errorf("invalid branch name: %s", t.branch)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("could not create the task folder: %v", err)
	}
	if err := saveTask(t); err != nil {
		return err
	}
	for _, f := range [][2]string{{"TASK.md", md}, {"AGENTS.md", agentsMD}, {"CLAUDE.md", "@AGENTS.md\n@TASK.md\n"}} {
		if err := os.WriteFile(path.Join(dir, f[0]), []byte(f[1]), 0o644); err != nil {
			return fmt.Errorf("could not write %s/%s: %v", t.name, f[0], err)
		}
	}
	return writeWorkspace(t.name)
}
