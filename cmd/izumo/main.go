// izumo: one folder per task, one git worktree per repo.
// https://github.com/migueww/izumo (MIT)
package main

import (
	"errors"
	"fmt"
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

const usage = `izumo: one folder per task, one git worktree per repo

  izumo                                    same as izumo ls
  izumo new <task> [repo...] [--branch B] [--base BRANCH]
  izumo add <repo...> [--task T] [--ref]   --ref: a repo the task only reads
  izumo drop <repo...> [--task T]          take repos out of a task, keeping the task
  izumo adopt <worktree...> [--task T]     move existing worktrees into a task
  izumo mv <task> <new-name>               rename a task (branches keep their names)
  izumo base <branch> [--task T]           change the branch a task is based on
  izumo ls [task...] [--json] [--fetch]
  izumo done [task...] [--dry-run]
  izumo rm <task...> [--dry-run] [--discard]   --discard: even losing changes and commits
      done, rm and drop stop the task's dev servers after asking; --stop / --no-stop answer ahead
  izumo run [--task T] [--] <command...>   run a command in each repo of a task
  izumo pull [repo...]                     fetch the root's clones, fast-forward the clean ones

Run it anywhere under your multi-repo root, the folder holding a .izumo file.
`

var windows = runtime.GOOS == "windows"

func say(format string, a ...any) { fmt.Fprintf(os.Stderr, "izumo: "+format+"\n", a...) }

// errSaid: the command already told why it stopped; exit 1 without saying more
var errSaid = errors.New("said")

func main() { os.Exit(run(os.Args[1:])) }

var commands = map[string]func([]string) error{
	"new": cmdNew, "add": cmdAdd, "drop": cmdDrop, "adopt": cmdAdopt, "mv": cmdMv, "base": cmdBase,
	"ls": cmdLs, "done": cmdDone, "rm": cmdRm, "run": cmdRun, "pull": cmdPull,
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
	case "":
		if _, err := findRoot(); err != nil && os.Getenv("IZUMO_ROOT") == "" {
			fmt.Print(usage)
			return 0
		}
		cmd = "ls"
	case "--version":
		fmt.Println("izumo " + versionString())
		return 0
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	}
	f, ok := commands[cmd]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	// -h anywhere asks for help, except after run, where the rest is the command's own
	for i, a := range args {
		if (a == "-h" || a == "--help") && (cmd != "run" || i == 0) {
			fmt.Print(usage)
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
