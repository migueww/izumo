// izumo: one folder per task, one git worktree per repo.
// https://github.com/migueww/izumo (MIT)
package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
)

// version: the release sets it; go install leaves it to the module version
var version = "dev"

func versionString() string {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && strings.HasPrefix(bi.Main.Version, "v") {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return version
}

// helpLines: the commands by when a task needs them, each as {group, command, synopsis, what it does}
var helpLines = [][4]string{
	{"start", "new", "izumo new <task> [repo...] [--branch B] [--base BRANCH]", ""},
	{"", "add", "izumo add <repo...> [--task T] [--ref]", "--ref: a repo the task only reads"},
	{"", "adopt", "izumo adopt <worktree...> [--task T]", "move existing worktrees into a task"},
	{"see", "ls", "izumo ls [task...] [--json] [--fetch]", "plain izumo runs it"},
	{"work", "run", "izumo run [--task T] [--] <command...>", "run a command in each repo of a task"},
	{"", "pull", "izumo pull [repo...]", "fetch the root's clones, fast-forward the clean ones"},
	{"", "base", "izumo base <branch> [--task T]", "change the branch a task is based on"},
	{"", "mv", "izumo mv <task> <new-name>", "rename a task (branches keep their names)"},
	{"", "drop", "izumo drop <repo...> [--task T]", "take repos out of a task, keeping the task"},
	{"", "stop", "izumo stop [repo...] [--task T] [--dry-run]", "stop a task's dev servers, keeping the task"},
	{"finish", "done", "izumo done [task...] [--dry-run]", "remove the tasks whose repos all shipped"},
	{"", "rm", "izumo rm <task...> [--dry-run] [--discard]", "--discard: even losing changes and commits"},
}

// help: every command, or only cmd's with a pointer to the rest. On a terminal, the torii and
// colors; piped, the same text plain.
func help(cmd string) string {
	var b strings.Builder
	if tty() {
		fmt.Fprintf(&b, "%s %s  one folder per task, one git worktree per repo\n\n", paint(vermilion, "⛩"), paint(bold, "izumo"))
	} else {
		b.WriteString("izumo: one folder per task, one git worktree per repo\n\n")
	}
	// the group column only when every command shows; descriptions line up past the longest synopsis
	var shown [][4]string
	gw, sw := 8, 0
	for _, l := range helpLines {
		if cmd == "" || l[1] == cmd {
			shown = append(shown, l)
			if l[3] != "" {
				sw = max(sw, len(l[2]))
			}
		}
	}
	if cmd != "" {
		gw = 2
	}
	stops := false
	for _, l := range shown {
		group, syn := l[0], l[2]
		if cmd != "" {
			group = ""
		}
		if l[3] != "" {
			syn = fmt.Sprintf("%-*s  %s", sw, syn, paint(dim, l[3]))
		}
		fmt.Fprintf(&b, "%s%s\n", paint(vermilion, fmt.Sprintf("%-*s", gw, group)), syn)
		stops = stops || l[1] == "done" || l[1] == "rm" || l[1] == "drop"
	}
	if stops {
		b.WriteString(strings.Repeat(" ", gw) + paint(dim, "done, rm and drop stop the task's dev servers after asking; --stop / --no-stop answer ahead") + "\n")
	}
	if cmd != "" {
		b.WriteString("\n" + paint(dim, "izumo -h for every command") + "\n")
	} else {
		b.WriteString("\nRun it anywhere under your multi-repo root, the folder holding a .izumo file.\n")
	}
	return b.String()
}

// suggest: the command a typo meant, when one is an edit away (two for the longer names)
func suggest(typo string) string {
	best, bd := "", 3
	for _, c := range slices.Sorted(maps.Keys(commands)) {
		if d := distance(typo, c); d < bd && (d == 1 || len(c) >= 4) {
			best, bd = c, d
		}
	}
	return best
}

// distance: the edits that turn a into b: insert, delete, change, or swap two neighbors (nwe, sl)
func distance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+b2i(a[i-1] != b[j-1]))
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

var windows = runtime.GOOS == "windows"

func say(format string, a ...any) {
	stderrMu.Lock()
	defer stderrMu.Unlock()
	if barUp {
		fmt.Fprint(os.Stderr, "\r\x1b[K") // the bar draws itself again on its next tick
	}
	fmt.Fprintf(os.Stderr, "izumo: "+format+"\n", a...)
}

// errSaid: the command already told why it stopped; exit 1 without saying more
var errSaid = errors.New("said")

func main() { os.Exit(run(os.Args[1:])) }

var commands = map[string]func([]string) error{
	"new": cmdNew, "add": cmdAdd, "drop": cmdDrop, "adopt": cmdAdopt, "mv": cmdMv, "base": cmdBase,
	"ls": cmdLs, "done": cmdDone, "rm": cmdRm, "stop": cmdStop, "run": cmdRun, "pull": cmdPull,
}

func run(args []string) int {
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "_agent":
		agentHook(args)
		return 0
	case "_context":
		contextHook()
		return 0
	case "":
		if _, err := findRoot(); err != nil && os.Getenv("IZUMO_ROOT") == "" {
			say("no .izumo here or above; put one in the folder that holds your clones")
			fmt.Print(help(""))
			return 0
		}
		cmd = "ls"
	case "--json", "--fetch": // plain izumo is ls, flags and all
		cmd, args = "ls", append([]string{cmd}, args...)
	case "--version", "-v", "version":
		fmt.Println("izumo " + versionString())
		return 0
	case "-h", "--help", "help":
		// izumo help ls: the same as izumo ls -h
		if len(args) > 0 && commands[args[0]] != nil {
			fmt.Print(help(args[0]))
		} else {
			fmt.Print(help(""))
		}
		return 0
	}
	f, ok := commands[cmd]
	if !ok {
		// a flag is no typo of a command: -v is not mv
		if s := suggest(cmd); s != "" && !strings.HasPrefix(cmd, "-") {
			say("unknown command %q; did you mean %s?", cmd, s)
		} else {
			say("unknown command %q; izumo -h lists them", cmd)
		}
		return 2
	}
	// -h anywhere asks for help, except after run, where the rest is the command's own
	for i, a := range args {
		if (a == "-h" || a == "--help") && (cmd != "run" || i == 0) {
			fmt.Print(help(cmd))
			return 0
		}
	}
	err := setup()
	if err == nil {
		err = f(args)
	}
	if err != nil {
		if !errors.Is(err, errSaid) {
			say("%v", err)
		}
		return 1
	}
	return 0
}

// opts: the flags a command was given, and its other arguments
type opts struct {
	values map[string]string // --task T, and "1" for a flag without value
	args   []string
}

func (o opts) has(f string) bool { return o.values[f] != "" }

// parseFlags reads the flags a command accepts, anywhere among its arguments; a valued one takes
// the next argument
func parseFlags(args []string, valued, boolean []string) (opts, error) {
	o := opts{values: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(valued, a):
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a value", a)
			}
			i++
			o.values[a] = args[i]
		case slices.Contains(boolean, a):
			o.values[a] = "1"
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %s", a)
		default:
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

func validName(s string) error {
	if s == "" || s[0] == '.' || strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) >= 0 {
		return fmt.Errorf("invalid name: '%s'", s)
	}
	return nil
}

// realPath is pwd -P: absolute, symlinks resolved, forward slashes even on Windows,
// so paths compare as strings and read the same in git and in bash hooks
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(r), nil
}

func cwd() string {
	d, _ := os.Getwd()
	if r, err := realPath(d); err == nil {
		return r
	}
	return filepath.ToSlash(d)
}

// fold: paths compare without case where the file system does, on Windows and macOS
var fold = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

func normPath(s string) string { return normFold(s, fold) }

func normFold(s string, fold bool) string {
	s = strings.ReplaceAll(s, `\`, "/")
	if fold {
		s = strings.ToLower(s)
	}
	return s
}

// under reports whether p is dir or inside it
func under(p, dir string) bool { return strings.HasPrefix(normPath(p)+"/", normPath(dir)+"/") }

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
