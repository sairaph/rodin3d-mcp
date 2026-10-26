//go:build darwin || freebsd || netbsd || openbsd

package app

import "golang.org/x/sys/unix"

// guardStderr routes stderr through collapse (stderr.go).
func guardStderr() { pipeStderr(unix.Dup2, unix.Dup) }
