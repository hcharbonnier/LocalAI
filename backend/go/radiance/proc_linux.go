//go:build linux

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// setChildAttrs makes the kernel kill the engine when this process dies, so a
// crashed or SIGKILLed backend cannot leave an engine holding GPU memory.
func setChildAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// lockThreadForChild pins the calling goroutine to its OS thread for good.
// PDEATHSIG fires when the thread that created the child exits, not the
// process; the supervising goroutine keeps that thread alive until the child
// is reaped.
func lockThreadForChild() { runtime.LockOSThread() }

// terminate asks the engine to shut down. Radiance exits on SIGTERM; the
// caller kills it when it does not.
func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// processRSS returns the resident memory of a process in bytes, or 0.
func processRSS(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				kb, _ := strconv.ParseUint(f[0], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}
