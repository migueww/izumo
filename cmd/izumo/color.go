package main

import (
	"os"
	"sync"

	"golang.org/x/term"
)

// color: ls paints stages and flags when it writes to a terminal. NO_COLOR turns it off;
// IZUMO_COLOR=1 turns it on where the binary can't tell, as under Git Bash's terminal (the shim sets it).
var color = sync.OnceValue(func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if v := os.Getenv("IZUMO_COLOR"); v != "" {
		return v == "1" && enableANSI()
	}
	return term.IsTerminal(int(os.Stdout.Fd())) && enableANSI()
})

const (
	bold    = "1"
	dim     = "2"
	red     = "31"
	green   = "32"
	yellow  = "33"
	blue    = "34"
	magenta = "35"
	cyan    = "36"
)

func paint(code, s string) string {
	if s == "" || !color() {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// how far a repo's work went, at a glance: shipped green, waiting on review blue, not started dim
var stageColor = map[stage]string{
	stageEmpty: dim, stageRef: dim, stageWip: yellow, stagePR: blue, stageBase: magenta, stageProd: green,
	stageDetached: red, stageError: red,
}

// flagColor: what needs you is red, what is on its way yellow, what is fine green
var flagColor = map[string]string{
	"modified": yellow, "untracked": yellow, "unpushed": red,
	"approved": green, "changes-requested": red, "review-required": yellow,
	"ci:passing": green, "ci:failing": red, "ci:running": yellow,
}
