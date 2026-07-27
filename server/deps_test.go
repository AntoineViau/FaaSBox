package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// writeDepsMarker records hash as the dependency spec node_modules was built for.
func writeDepsMarker(t *testing.T, dir, hash string) {
	t.Helper()
	modPath := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(modPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modPath, depsHashFile), []byte(hash), 0o644); err != nil {
		t.Fatal(err)
	}
}

// installCounter puts a fake "bun" on PATH and returns a function reporting how
// many times it was invoked.
func installCounter(t *testing.T) func() int {
	t.Helper()
	binDir := t.TempDir()
	countPath := filepath.Join(binDir, "runs")
	script := "#!/bin/sh\necho x >> \"" + countPath + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bun"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })

	return func() int {
		data, err := os.ReadFile(countPath)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "\n")
	}
}

func TestEnsureDeps_NoPackageJson(t *testing.T) {
	dir := t.TempDir()
	// Empty dir, no package.json → should return nil immediately
	err := ensureDeps(context.Background(), dir)
	if err != nil {
		t.Errorf("expected nil error for dir without package.json, got: %v", err)
	}
}

func TestEnsureDeps_MarkerMatches(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, err := depsHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeDepsMarker(t, dir, hash)

	// Marker matches → install must be skipped. The cancelled context would make
	// bun install fail, so a nil error proves nothing was run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ensureDeps(ctx, dir); err != nil {
		t.Errorf("expected nil when the deps marker matches, got: %v", err)
	}
}

func TestEnsureDeps_MarkerMismatch(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDepsMarker(t, dir, "stale-hash")

	// Marker does not match the current spec → should try bun install.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ensureDeps(ctx, dir); err == nil {
		t.Error("expected error (bun install attempted with cancelled ctx), got nil")
	}
}

func TestEnsureDeps_NodeModulesWithoutMarker(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// node_modules left over from an install that predates the marker
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ensureDeps(ctx, dir); err == nil {
		t.Error("expected error (bun install attempted with cancelled ctx), got nil")
	}
}

func TestEnsureDeps_SkipsInstallWhenSpecUnchanged(t *testing.T) {
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runs := installCounter(t)
	depsMu.Delete(dir)

	if err := ensureDeps(context.Background(), dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got := runs(); got != 1 {
		t.Fatalf("expected 1 install on first call, got %d", got)
	}

	// syncRecordToDisk used to rewrite package.json on every save, which refreshed
	// its mtime and forced a reinstall. Identical content must now be a no-op.
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureDeps(context.Background(), dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := runs(); got != 1 {
		t.Errorf("expected no reinstall when the spec is unchanged, got %d installs", got)
	}
}

func TestEnsureDeps_LockfileChangeTriggersInstall(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, err := depsHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeDepsMarker(t, dir, hash)

	// Same package.json, different lockfile → with --frozen-lockfile the installed
	// tree would differ, so this counts as a spec change.
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ensureDeps(ctx, dir); err == nil {
		t.Error("expected error (bun install attempted after lockfile change), got nil")
	}
}

func TestEnsureDeps_ContextCancelled(t *testing.T) {
	dir := t.TempDir()

	// Create package.json (to pass the first check)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// No node_modules → will try to run bun install

	// Cancel the context before calling ensureDeps
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ensureDeps(ctx, dir)
	if err == nil {
		t.Error("expected error with cancelled context, got nil")
	}
}

func TestEnsureDeps_ConcurrentCalls(t *testing.T) {
	dir := t.TempDir()

	// Create package.json so ensureDeps enters the lock path
	pkgPath := filepath.Join(dir, "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// No node_modules → all goroutines will want to install

	// Clear any cached mutex for this dir
	depsMu.Delete(dir)

	const n = 10
	var wg sync.WaitGroup
	var errCount atomic.Int32

	// Use cancelled context so bun install fails fast but the lock path is exercised
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := ensureDeps(ctx, dir); err != nil {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()

	// All should have hit the error (bun install fails with cancelled ctx)
	if errCount.Load() != n {
		t.Errorf("expected all %d goroutines to get an error, got %d errors", n, errCount.Load())
	}

	// Verify all goroutines used the same mutex (single entry in sync.Map)
	val, ok := depsMu.Load(dir)
	if !ok {
		t.Fatal("expected mutex in depsMu for dir")
	}
	if _, isMutex := val.(*sync.Mutex); !isMutex {
		t.Fatal("expected *sync.Mutex in depsMu")
	}
}

func TestEnsureDeps_FrozenLockfile(t *testing.T) {
	dir := t.TempDir()

	// 1. Setup: package.json present, no node_modules, no bun.lock
	pkgPath := filepath.Join(dir, "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a "fake bun" in PATH to capture arguments
	binDir := t.TempDir()
	fakeBun := filepath.Join(binDir, "bun")
	// This script writes all arguments to a file in the function dir
	script := "#!/bin/sh\necho \"$*\" > \"$PWD/install.args\"\nexit 0\n"
	if err := os.WriteFile(fakeBun, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	// Test Case A: No bun.lock -> should NOT have --frozen-lockfile
	if err := ensureDeps(context.Background(), dir); err != nil {
		t.Errorf("unexpected error without bun.lock: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "install.args"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "install" {
		t.Errorf("expected 'install', got %q", string(args))
	}

	// Test Case B: With bun.lock -> SHOULD have --frozen-lockfile
	lockPath := filepath.Join(dir, "bun.lock")
	if err := os.WriteFile(lockPath, []byte("fake lock"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Make package.json newer than node_modules (which was created by our fake bun if it was real,
	// but here we just need to bypass the mtime check if we were to have node_modules)
	// Actually, ensureDeps checks if node_modules exists.
	// Let's just remove node_modules if it exists to be sure.
	os.RemoveAll(filepath.Join(dir, "node_modules"))

	if err := ensureDeps(context.Background(), dir); err != nil {
		t.Errorf("unexpected error with bun.lock: %v", err)
	}
	args, err = os.ReadFile(filepath.Join(dir, "install.args"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "install --frozen-lockfile" {
		t.Errorf("expected 'install --frozen-lockfile', got %q", string(args))
	}
}

func TestDefaultFunctionsDir(t *testing.T) {
	dir := defaultFunctionsDir()
	if dir != filepath.Join(".", "functions") {
		t.Errorf("defaultFunctionsDir() = %q, want %q", dir, filepath.Join(".", "functions"))
	}
}
