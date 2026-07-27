package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// depsMu holds a per-directory mutex to prevent concurrent bun install runs.
var depsMu sync.Map // map[string]*sync.Mutex

// depsHashFile records the dependency spec node_modules was built for. It lives
// inside node_modules so that removing the directory also invalidates the marker.
const depsHashFile = ".faasbox-deps"

// depsHash returns a hash of package.json and bun.lock (when present).
// bun.lock is part of the hash because with --frozen-lockfile it is the lockfile,
// not package.json, that determines the installed tree.
func depsHash(funcDir string) (string, error) {
	pkg, err := os.ReadFile(filepath.Join(funcDir, "package.json"))
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(pkg)
	if lock, err := os.ReadFile(filepath.Join(funcDir, "bun.lock")); err == nil {
		h.Write(lock)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensureDeps runs "bun install" in funcDir when the dependency spec changed since
// the last successful install. A per-directory lock prevents concurrent installs
// for the same function.
func ensureDeps(ctx context.Context, funcDir string) error {
	pkgPath := filepath.Join(funcDir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return nil // no package.json → nothing to do
	}

	// Acquire a per-directory lock to prevent concurrent bun installs
	val, _ := depsMu.LoadOrStore(funcDir, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	want, err := depsHash(funcDir)
	if err != nil {
		return nil // package.json vanished between the check and the lock
	}

	// Skip install if node_modules was built for this exact dependency spec
	modulesPath := filepath.Join(funcDir, "node_modules")
	if got, err := os.ReadFile(filepath.Join(modulesPath, depsHashFile)); err == nil && string(got) == want {
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

	// Best-effort marker: if writing it fails we simply reinstall next time.
	if err := os.MkdirAll(modulesPath, 0o755); err == nil {
		os.WriteFile(filepath.Join(modulesPath, depsHashFile), []byte(want), 0o644)
	}
	return nil
}
