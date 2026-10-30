package main

import "testing"

func TestPlace(t *testing.T) {
	cases := []struct {
		name                      string
		fold                      bool
		tasks, cwd, cmdline, want string
	}{
		{"folder", false, "/root/tasks", "/root/tasks/feat/web/src", "node server.js", "feat/web"},
		{"node_modules on Windows", true, "C:/root/tasks", "", `node "C:\root\tasks\Feat\API\node_modules\.bin\..\vite\bin\vite.js" --port 3000`, "feat/api"},
		{"argument", false, "/root/tasks", "/home/x", "python -m http.server --directory /root/tasks/feat/web", "feat/web"},
		{"--flag=path", false, "/root/tasks", "/home/x", "serve --dir=/root/tasks/feat/web", "feat/web"},
		{"editor on the task folder", true, "C:/root/tasks", "", `Code.exe C:\root\tasks\feat\feat.code-workspace`, ""},
		{"shell in the task folder", false, "/root/tasks", "/root/tasks/feat", "bash", ""},
		{"not a worktree", false, "/root/tasks", "/root/tasks/other/web", "node", ""},
		{"same path under another root", false, "/root/tasks", "/mnt/backup/root/tasks/feat/web", "node /mnt/backup/root/tasks/feat/web/s.js", ""},
		{"case matters on Linux", false, "/root/tasks", "/root/tasks/Feat/web", "node", ""},
	}
	for _, c := range cases {
		wts := map[string]string{}
		for _, tr := range []string{"feat/web", "feat/api"} {
			wts[normFold(tr, c.fold)] = tr
		}
		if got, _ := place(c.tasks, wts, c.cwd, c.cmdline, c.fold); got != c.want {
			t.Errorf("%s: place(%q, %q) = %q, want %q", c.name, c.cwd, c.cmdline, got, c.want)
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
