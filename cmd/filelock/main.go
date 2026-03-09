package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/gofrs/flock"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: filelock <lock-file> <timeout-seconds> <command> [args...]\n")
		fmt.Fprintf(os.Stderr, "Example: filelock /path/to/.lock 10 cat file.txt\n")
		os.Exit(1)
	}

	lockPath := os.Args[1]
	timeoutSec := os.Args[2]
	cmdName := os.Args[3]
	cmdArgs := os.Args[4:]

	// Parse timeout
	var timeout time.Duration
	if timeoutSec == "0" {
		timeout = 0 // wait forever
	} else {
		var seconds int
		if _, err := fmt.Sscanf(timeoutSec, "%d", &seconds); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid timeout: %s\n", timeoutSec)
			os.Exit(1)
		}
		timeout = time.Duration(seconds) * time.Second
	}

	// Create flock instance
	lock := flock.New(lockPath)

	// Try to acquire lock with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to acquire lock: %v\n", err)
		os.Exit(1)
	}
	if !locked {
		fmt.Fprintf(os.Stderr, "Timeout waiting for lock\n")
		os.Exit(1)
	}
	defer lock.Unlock()

	// Execute command
	cmd := exec.Command(cmdName, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "Failed to execute command: %v\n", err)
		os.Exit(1)
	}
}
