// izumo: one folder per task, one git worktree per repo.
// https://github.com/migueww/izumo (MIT)
package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

var version = "1.0.0"

const usage = `izumo: one folder per task, one git worktree per repo

  izumo                                    same as izumo ls
  izumo new <task> [repo...] [--branch B] [--base BRANCH]
  izumo add <repo...> [--task T]
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

func die(format string, a ...any) {
	say(format, a...)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "new", "add", "drop", "adopt", "mv", "base", "ls", "done", "rm", "run", "pull":
		args = args[1:]
	case "_agent":
		agentHook(args[1:])
		return
	case "":
		if os.Getenv("IZUMO_ROOT") == "" {
			if _, err := findRoot(); err != nil {
				fmt.Print(usage)
				return
			}
		}
		cmd = "ls"
	case "--version":
		fmt.Println("izumo " + version)
		return
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	// -h anywhere asks for help, except after run, where the rest is the command's own
	for i, a := range args {
		if (a == "-h" || a == "--help") && (cmd != "run" || i == 0) {
			fmt.Print(usage)
			return
		}
	}

	setup()
	switch cmd {
	case "new":
		cmdNew(args)
	case "add":
		cmdAdd(args)
	case "drop":
		cmdDrop(args)
	case "adopt":
		cmdAdopt(args)
	case "mv":
		cmdMv(args)
	case "base":
		cmdBase(args)
	case "ls":
		cmdLs(args)
	case "done", "rm":
		os.Exit(cmdRemove(cmd, args))
	case "run":
		cmdRun(args)
	case "pull":
		cmdPull(args)
	}
}

func validName(s string) {
	if s == "" || s[0] == '.' || strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) >= 0 {
		die("invalid name: '%s'", s)
	}
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
	d, err := os.Getwd()
	if err != nil {
		die("can't read the current folder: %v", err)
	}
	if r, err := realPath(d); err == nil {
		return r
	}
	return filepath.ToSlash(d)
}

// under reports whether p is dir or inside it; Windows paths compare without case
func under(p, dir string) bool {
	p, dir = p+"/", dir+"/"
	if windows {
		p, dir = strings.ToLower(p), strings.ToLower(dir)
	}
	return strings.HasPrefix(p, dir)
}

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

// flagValue takes the value after a flag like --task, dying when it is missing
func flagValue(args []string, i int) string {
	if i+1 >= len(args) {
		die("%s needs a value", args[i])
	}
	return args[i+1]
}

func base(p string) string { return path.Base(p) }
