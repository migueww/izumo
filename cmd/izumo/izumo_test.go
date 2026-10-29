package main

import "testing"

func TestPlace(t *testing.T) {
	wts := map[string]string{"feat/web": "feat/web", "feat/api": "feat/api"}
	cases := []struct {
		tasks, cwd, cmdline, want string
	}{
		{"/root/tasks", "/root/tasks/feat/web/src", "node server.js", "feat/web"},
		{"C:/root/tasks", "", `node "C:\root\tasks\Feat\API\node_modules\.bin\..\vite\bin\vite.js" --port 3000`, "feat/api"},
		{"/root/tasks", "/home/x", "python -m http.server --directory /root/tasks/feat/web", "feat/web"},
		{"C:/root/tasks", "", `Code.exe C:\root\tasks\feat\feat.code-workspace`, ""}, // an editor on the task folder
		{"/root/tasks", "/root/tasks/feat", "bash", ""},                              // a shell in the task folder
		{"/root/tasks", "/root/tasks/other/web", "node", ""},                         // not a worktree
	}
	for _, c := range cases {
		if got, _ := place(c.tasks, wts, c.cwd, c.cmdline); got != c.want {
			t.Errorf("place(%q, %q) = %q, want %q", c.cwd, c.cmdline, got, c.want)
		}
	}
}

func TestTaskFileQuoting(t *testing.T) {
	for _, v := range []string{"", "feat/x", "old-branch", "it's", "a b"} {
		if got := unquote(shellQuote(v)); got != v {
			t.Errorf("round trip of %q gave %q", v, got)
		}
	}
	// what the bash izumo wrote with printf %q
	for in, want := range map[string]string{"''": "", `feat/a\ b`: "feat/a b", "main": "main"} {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}
