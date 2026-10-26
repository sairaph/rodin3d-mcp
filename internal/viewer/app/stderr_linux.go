//go:build linux

package app

import "golang.org/x/sys/unix"

// guardStderr routes stderr through collapse (stderr.go). Linux on arm64
// has no dup2 system call, so dup3 is used on every architecture.
func guardStderr() {
	pipeStderr(func(oldfd, newfd int) error { return unix.Dup3(oldfd, newfd, 0) }, unix.Dup)
}
