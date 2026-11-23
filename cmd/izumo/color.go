package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// tty: someone reads ls on a terminal, so it lays out for the eye. IZUMO_COLOR=1 says so where
// the binary can't tell, as under Git Bash's terminal (the shim sets it); 0 says it isn't.
var tty = sync.OnceValue(func() bool {
	if v := os.Getenv("IZUMO_COLOR"); v != "" {
		return v == "1"
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
})

// errTTY: stderr reaches a terminal, so the bar can draw there and not into a file.
// IZUMO_ERR_TTY says it where the binary can't tell (the shim sets it under Git Bash's terminal).
var errTTY = sync.OnceValue(func() bool {
	if v := os.Getenv("IZUMO_ERR_TTY"); v != "" {
		return v == "1"
	}
	return term.IsTerminal(int(os.Stderr.Fd()))
})

// color: ls paints stages and flags on a terminal; NO_COLOR turns it off
var color = sync.OnceValue(func() bool {
	return os.Getenv("NO_COLOR") == "" && tty() && enableANSI()
})

// width: the terminal's columns, 0 when unknown. Git Bash's terminal can't be asked; the shim
// passes COLUMNS.
var width = sync.OnceValue(func() int {
	if !tty() {
		return 0
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	n, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	return n
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
	// the torii's vermilion, from the icon
	vermilion = "38;5;202"
)

func paint(code, s string) string {
	if s == "" || !color() {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// link: text that opens url where the terminal shows links (OSC 8)
func link(url, text string) string {
	if !color() {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// gather: on stderr while ls waits on git and GitHub, the repos arriving at the torii, like the
// gods at Izumo: ⛩ gathering ━━━━━━━━────── 23/41. Terminals only; it draws after the first
// tick, so a quick ls never shows it.
func gather(verb string, n *atomic.Int32, total int) (stop func()) {
	// NO_COLOR takes the color away, not the bar
	if !tty() || !errTTY() || !enableANSI() || total == 0 {
		return func() {}
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-quit:
				stderrMu.Lock()
				if barUp {
					fmt.Fprint(os.Stderr, "\r\x1b[K")
					barUp = false
				}
				stderrMu.Unlock()
				return
			case <-t.C:
				const w = 24
				k := int(n.Load())
				f := w * k / total
				stderrMu.Lock()
				fmt.Fprintf(os.Stderr, "\r\x1b[K%s %-9s %s%s %s", paint(vermilion, "⛩"), verb,
					paint(vermilion, strings.Repeat("━", f)), paint(dim, strings.Repeat("─", w-f)),
					paint(dim, fmt.Sprintf("%d/%d", k, total)))
				barUp = true
				stderrMu.Unlock()
			}
		}
	}()
	return func() { close(quit); <-done }
}

// stderrMu: the bar and say take turns on stderr; barUp: the bar holds the line, so say clears it
// first instead of finishing it ("⛩ fetching ──── 0/3izumo: slow: fetch failed")
var (
	stderrMu sync.Mutex
	barUp    bool
)

// how far a repo's work went, at a glance: shipped green, waiting on review blue, not started dim
var stageColor = map[stage]string{
	stageEmpty: dim, stageRef: dim, stageWip: yellow, stagePushed: yellow, stagePR: blue, stageBase: magenta, stageProd: green,
	stageDetached: red, stageError: red,
}

// flagColor: what needs you is red, what is on its way yellow, what is fine green
var flagColor = map[string]string{
	"modified": yellow, "untracked": yellow, "unpushed": red,
	"approved": green, "changes-requested": red, "review-required": yellow,
	"ci:passing": green, "ci:failing": red, "ci:running": yellow,
}
