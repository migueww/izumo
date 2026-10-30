package main

import (
	"os"

	win "golang.org/x/sys/windows"
)

// enableANSI: the classic Windows console shows escape codes as text unless asked to process
// them; a pipe or Git Bash's terminal has no console mode to set, and needs none
func enableANSI() bool {
	h := win.Handle(os.Stdout.Fd())
	var mode uint32
	if win.GetConsoleMode(h, &mode) != nil {
		return true
	}
	return win.SetConsoleMode(h, mode|win.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
