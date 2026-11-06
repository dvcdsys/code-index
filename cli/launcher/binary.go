//go:build darwin || windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// binaryVersion runs `<bin> <flag>` and returns its first line of output.
//
// Both bundled binaries answer a version flag and exit immediately
// (cix-server -v, cix --version), so the timeout is a guard against a wedged
// exec rather than an expected wait.
func binaryVersion(bin, flag string) string {
	if _, err := os.Stat(bin); err != nil {
		return "missing"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, flag).Output()
	if err != nil {
		return "unknown"
	}

	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line == "" {
		return "unknown"
	}
	return line
}
