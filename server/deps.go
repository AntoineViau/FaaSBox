package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// depsMu holds a per-directory mutex to prevent concurrent bun install runs.
var depsMu sync.Map // map[string]*sync.Mutex

// ensureDeps runs "bun install" in funcDir if package.json is newer than node_modules.
// A per-directory lock prevents concurrent installs for the same function.
func ensureDeps(ctx context.Context, funcDir string) error {
	pkgPath := filepath.Join(funcDir, "package.json")
	pkgInfo, err := os.Stat(pkgPath)
	if err != nil {
		return nil // no package.json → nothing to do
	}

	// Acquire a per-directory lock to prevent concurrent bun installs
	val, _ := depsMu.LoadOrStore(funcDir, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	// Skip install if node_modules is newer than package.json
	modulesPath := filepath.Join(funcDir, "node_modules")
	if modInfo, err := os.Stat(modulesPath); err == nil && modInfo.ModTime().After(pkgInfo.ModTime()) {
		return nil
	}

	args := []string{"install"}
	lockPath := filepath.Join(funcDir, "bun.lock")
	if _, err := os.Stat(lockPath); err == nil {
		args = append(args, "--frozen-lockfile")
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = funcDir
	cmd.Stderr = &stderr
	cmd.Env = []string{
		"HOME=/tmp",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}
