//go:build !linux

package main

import "os/exec"

// The engine targets Linux; elsewhere this backend is built and tested only.
// Without Pdeathsig the engine ends when its parent goes away only on some
// platforms, so close()/kill() are the reliable paths.

func setChildAttrs(*exec.Cmd) {}

func lockThreadForChild() {}

func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func processRSS(int) uint64 { return 0 }
