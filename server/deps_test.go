package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
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

// fakeBun puts a stub "bun" running body on PATH, and returns a function
// reporting how many times it was invoked.
func fakeBun(t *testing.T, body string) func() int {
	t.Helper()
	binDir := t.TempDir()
	countPath := filepath.Join(binDir, "runs")
	script := "#!/bin/sh\necho x >> \"" + countPath + "\"\n" + body + "\n"
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

// installCounter puts a fake "bun" that always succeeds on PATH.
func installCounter(t *testing.T) func() int {
	t.Helper()
	return fakeBun(t, "exit 0")
}

func TestEnsureDeps_NoPackageJson(t *testing.T) {
	dir := t.TempDir()
	// Empty dir, no package.json → should return nil immediately
	_, err := ensureDeps(context.Background(), dir)
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

	if _, err := ensureDeps(ctx, dir); err != nil {
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

	if _, err := ensureDeps(ctx, dir); err == nil {
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

	if _, err := ensureDeps(ctx, dir); err == nil {
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

	if _, err := ensureDeps(context.Background(), dir); err != nil {
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

	if _, err := ensureDeps(context.Background(), dir); err != nil {
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

	if _, err := ensureDeps(ctx, dir); err == nil {
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

	_, err := ensureDeps(ctx, dir)
	if err == nil {
		t.Error("expected error with cancelled context, got nil")
	}
}

// depsFixture creates a function directory whose spec is not installed, so the
// next ensureDeps call necessarily runs the fake bun on PATH.
func depsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestEnsureDeps_TimeoutIsNamed covers the confusion this whole qualification
// exists to end: exec.CommandContext kills through Process.Kill, so an expired
// deadline used to read "signal: killed" — indistinguishable from an OOM kill.
func TestEnsureDeps_TimeoutIsNamed(t *testing.T) {
	dir := depsFixture(t)
	fakeBun(t, "sleep 5\nexit 0")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := ensureDeps(ctx, dir)
	if err == nil {
		t.Fatal("expected an error when the install outlives its deadline")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to name the deadline", err)
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("error = %q, want the plumbing kept out of the message", err)
	}
}

func TestEnsureDeps_KilledFromOutsideIsNamed(t *testing.T) {
	dir := depsFixture(t)
	// The process kills itself with SIGKILL, which is what the OOM killer or a
	// container shutdown does to it.
	fakeBun(t, "kill -KILL $$")

	_, err := ensureDeps(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error when bun is killed")
	}
	if !strings.Contains(err.Error(), "killed by the system") {
		t.Errorf("error = %q, want it to say the process was killed from outside", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it distinct from the deadline message", err)
	}
}

func TestEnsureDeps_OrdinaryFailureKeepsBunOutput(t *testing.T) {
	dir := depsFixture(t)
	fakeBun(t, `echo "error: package nope@1.0.0 not found" >&2`+"\nexit 1")

	_, err := ensureDeps(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error when bun install fails")
	}
	// Neither of the two qualified cases: bun's own output is the information,
	// and it must come through as it always did.
	got := err.Error()
	if !strings.Contains(got, "nope@1.0.0 not found") {
		t.Errorf("error = %q, want the bun output verbatim", got)
	}
	if !strings.Contains(got, "exit status 1") {
		t.Errorf("error = %q, want the wrapped exit status kept", got)
	}
	if strings.Contains(got, "timed out") || strings.Contains(got, "killed by the system") {
		t.Errorf("error = %q, want no qualification on an ordinary failure", got)
	}
}

func TestEnsureDeps_CapturesStdoutToo(t *testing.T) {
	dir := depsFixture(t)
	// bun appears to write everything to stderr, but a version that does not must
	// not leave the field empty.
	fakeBun(t, `echo "spoken on stdout"`+"\nexit 1")

	_, err := ensureDeps(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error when bun install fails")
	}
	if !strings.Contains(err.Error(), "spoken on stdout") {
		t.Errorf("error = %q, want the standard output captured too", err)
	}
}

// TestEnsureDeps_KeepsTheEndOfAVerboseFailure is the point of the tail capture:
// a chatty install that fails prints its diagnosis last, and a head-first cut
// would keep the progress and drop it.
func TestEnsureDeps_KeepsTheEndOfAVerboseFailure(t *testing.T) {
	dir := depsFixture(t)
	fakeBun(t, `head -c 200000 /dev/zero | tr "\0" "x" >&2`+"\n"+
		`echo "error: the last line, which is the one that matters" >&2`+"\nexit 1")

	_, err := ensureDeps(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error when bun install fails")
	}

	got := err.Error()
	if !strings.Contains(got, "the last line, which is the one that matters") {
		t.Errorf("error = %q, want the tail of the output kept", got)
	}
	if !strings.Contains(got, "...[truncated, ") {
		t.Errorf("error = %q, want a marker stating what was dropped", got)
	}
}

// TestEnsureDeps_FailureFitsUnderTheFieldCap ties the capture budget to the
// field it feeds: truncateForLog cuts the tail, so a message that overflows
// maxDepsError would lose exactly the bytes the tail capture went looking for.
func TestEnsureDeps_FailureFitsUnderTheFieldCap(t *testing.T) {
	dir := depsFixture(t)
	fakeBun(t, `head -c 200000 /dev/zero | tr "\0" "x" >&2`+"\n"+
		`echo "error: the last line" >&2`+"\nexit 1")

	_, err := ensureDeps(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error when bun install fails")
	}

	// Both the message ensureDeps builds and the one an invocation wraps it in
	// have to clear the cap: the safety net publishes the former, but the latter
	// is what the HTTP response carries.
	raw := err.Error()
	wrapped := (&errDepsFailed{Cause: err}).Error()
	for label, msg := range map[string]string{"ensureDeps": raw, "errDepsFailed": wrapped} {
		if len(msg) > maxDepsError {
			t.Errorf("%s message is %d bytes, beyond the %d cap — truncateForLog would cut the tail",
				label, len(msg), maxDepsError)
		}
		if cut, _ := truncateForLog(msg, maxDepsError); cut != msg {
			t.Errorf("%s message got cut by truncateForLog, which must stay a rampart", label)
		}
	}
}

func TestEnsureDeps_ReportsWhetherItInstalled(t *testing.T) {
	dir := depsFixture(t)
	fakeBun(t, "mkdir -p node_modules\nexit 0")

	installed, err := ensureDeps(context.Background(), dir)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !installed {
		t.Error("installed = false after a real install, want true")
	}

	// Second call leaves on the hash check: nothing was installed, and the
	// invocation path must not write the record for it.
	installed, err = ensureDeps(context.Background(), dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if installed {
		t.Error("installed = true on the hash-check exit, want false")
	}

	// No package.json at all is the other silent exit.
	installed, err = ensureDeps(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("without package.json: %v", err)
	}
	if installed {
		t.Error("installed = true without a package.json, want false")
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
			if _, err := ensureDeps(ctx, dir); err != nil {
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
	if _, err := ensureDeps(context.Background(), dir); err != nil {
		t.Errorf("unexpected error without bun.lock: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "install.args"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "install --ignore-scripts" {
		t.Errorf("expected 'install --ignore-scripts', got %q", string(args))
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

	if _, err := ensureDeps(context.Background(), dir); err != nil {
		t.Errorf("unexpected error with bun.lock: %v", err)
	}
	args, err = os.ReadFile(filepath.Join(dir, "install.args"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "install --ignore-scripts --frozen-lockfile" {
		t.Errorf("expected 'install --ignore-scripts --frozen-lockfile', got %q", string(args))
	}
}

func TestEnsureDeps_ReinstallsAfterNodeModulesRemoved(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runs := fakeBun(t, "mkdir -p node_modules\nexit 0")

	if _, err := ensureDeps(context.Background(), dir); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if got := runs(); got != 1 {
		t.Fatalf("expected 1 install, got %d", got)
	}

	// A restart on a fresh filesystem — or a manual wipe — takes the marker with
	// node_modules, so the safety net on the invocation path must reinstall.
	if err := os.RemoveAll(filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureDeps(context.Background(), dir); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if got := runs(); got != 2 {
		t.Errorf("expected a reinstall after node_modules was removed, got %d installs", got)
	}
}

func TestEnsureDeps_ConcurrentCallsInstallOnce(t *testing.T) {
	root := t.TempDir()
	funcDir := filepath.Join(root, "svc")
	if err := os.MkdirAll(funcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(funcDir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A slow install, so the second caller necessarily arrives while the first
	// still holds the lock.
	runs := fakeBun(t, "sleep 0.4\nexit 0")
	depsMu.Delete(funcDir)

	// Two spellings of the same directory: the background install builds its path
	// from the --functionsDir flag, which may be relative, while the invocation
	// path resolves an absolute one. Both must take the same lock.
	paths := []string{funcDir, root + "/./svc"}

	var wg sync.WaitGroup
	errs := make([]error, len(paths))
	wg.Add(len(paths))
	for i, p := range paths {
		go func() {
			defer wg.Done()
			_, errs[i] = ensureDeps(context.Background(), p)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("ensureDeps(%q) = %v, want nil", paths[i], err)
		}
	}
	if got := runs(); got != 1 {
		t.Errorf("expected a single install for %d concurrent callers, got %d", len(paths), got)
	}
}

func TestScheduleDepsInstall_ReadyWithoutBlockingTheSave(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	runs := fakeBun(t, "sleep 0.4\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "with-deps",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)

	start := time.Now()
	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	elapsed := time.Since(start)

	// The install takes 400 ms; the save must not carry any of it.
	if elapsed > 200*time.Millisecond {
		t.Errorf("scheduleDepsInstall blocked for %s, want an immediate return", elapsed)
	}

	waitDepsStatus(t, app, record.Id, depsStatusInstalling)
	done := waitDepsStatus(t, app, record.Id, depsStatusReady)

	if got := done.GetString("depsError"); got != "" {
		t.Errorf("depsError = %q, want empty on success", got)
	}
	if got := runs(); got != 1 {
		t.Errorf("expected 1 install, got %d", got)
	}
}

func TestScheduleDepsInstall_ErrorCarriesInstallOutput(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	fakeBun(t, `echo "error: package nope@1.0.0 not found" >&2`+"\nexit 1")
	record := saveTestFunction(t, app, functionsDir, "bad-deps",
		"console.log('hi')", `{"dependencies":{"nope":"1.0.0"}}`)

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	failed := waitDepsStatus(t, app, record.Id, depsStatusError)

	if got := failed.GetString("depsError"); !strings.Contains(got, "nope@1.0.0 not found") {
		t.Errorf("depsError = %q, want the bun install output", got)
	}
}

func TestScheduleDepsInstall_BoundsPersistedError(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	// A failing install can print far more than the field accepts. Without the
	// cap, the record would be unsaveable through the PocketBase API.
	fakeBun(t, `head -c 200000 /dev/zero | tr "\0" "x" >&2`+"\nexit 1")
	record := saveTestFunction(t, app, functionsDir, "verbose-failure",
		"console.log('hi')", `{"dependencies":{"nope":"1.0.0"}}`)

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	failed := waitDepsStatus(t, app, record.Id, depsStatusError)

	stored := failed.GetString("depsError")
	if len(stored) > maxDepsError+logMarkerSlack {
		t.Errorf("depsError is %d bytes, beyond the %d declared for the field",
			len(stored), maxDepsError+logMarkerSlack)
	}
	if !strings.Contains(stored, "truncated") {
		t.Errorf("depsError = %q, want a truncation marker", stored)
	}

	// The stored value must survive a round-trip through PocketBase validation.
	failed.Set("script", "console.log('edited')")
	if err := app.Save(failed); err != nil {
		t.Errorf("saving a record carrying a truncated depsError failed: %v", err)
	}
}

func TestScheduleDepsInstall_ShutdownLeavesInstallOwed(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	fakeBun(t, "sleep 5\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "interrupted",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)

	ctx, cancel := context.WithCancel(context.Background())
	scheduleDepsInstall(ctx, app, record, functionsDir)
	waitDepsStatus(t, app, record.Id, depsStatusInstalling)

	cancel() // server shutdown

	// The install was interrupted, not refused: the state must say it is still owed
	// rather than accuse the dependencies of an error that never happened.
	stored := waitDepsStatus(t, app, record.Id, depsStatusPending)
	if got := stored.GetString("depsError"); got != "" {
		t.Errorf("depsError = %q, want empty after an interrupted install", got)
	}
}

func TestScheduleDepsInstall_NoPackageJson(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	runs := installCounter(t)
	record := saveTestFunction(t, app, functionsDir, "no-deps", "console.log('hi')", "")

	scheduleDepsInstall(context.Background(), app, record, functionsDir)

	// Nothing is scheduled, so there is no state to wait for.
	time.Sleep(100 * time.Millisecond)

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != "" {
		t.Errorf("depsStatus = %q, want empty for a function without package.json", got)
	}
	if got := runs(); got != 0 {
		t.Errorf("expected no install without a package.json, got %d", got)
	}
}

func TestScheduleDepsInstall_ClearsStateWhenDepsRemoved(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	installCounter(t)
	record := saveTestFunction(t, app, functionsDir, "dropped-deps",
		"console.log('hi')", `{"dependencies":{}}`)

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	waitDepsStatus(t, app, record.Id, depsStatusReady)

	// The dependency spec is dropped: a leftover "ready" would outlive its subject.
	record = saveTestFunction(t, app, functionsDir, "dropped-deps", "console.log('hi')", "")
	scheduleDepsInstall(context.Background(), app, record, functionsDir)

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != "" {
		t.Errorf("depsStatus = %q, want it cleared once package.json is gone", got)
	}
}

func TestScheduleDepsInstall_ScriptOnlyChangeSkipsInstall(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	runs := fakeBun(t, "mkdir -p node_modules\nexit 0")
	const pkg = `{"dependencies":{"left-pad":"1.0.0"}}`

	record := saveTestFunction(t, app, functionsDir, "stable-deps", "console.log('v1')", pkg)
	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	waitDepsStatus(t, app, record.Id, depsStatusReady)
	if got := runs(); got != 1 {
		t.Fatalf("expected 1 install on the first save, got %d", got)
	}

	// Same dependency spec, new script: the content fingerprint must hold.
	record = saveTestFunction(t, app, functionsDir, "stable-deps", "console.log('v2')", pkg)
	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	waitDepsStatus(t, app, record.Id, depsStatusReady)
	if got := runs(); got != 1 {
		t.Errorf("expected no reinstall for a script-only change, got %d installs", got)
	}

	// And the invocation path must not repay the cost either.
	if _, err := ensureDeps(context.Background(), filepath.Join(functionsDir, "stable-deps")); err != nil {
		t.Fatalf("safety net on the invocation path: %v", err)
	}
	if got := runs(); got != 1 {
		t.Errorf("expected the invocation to exit on the hash check, got %d installs", got)
	}
}

// TestSetDepsState_WritesWithoutFiringTheSaveHooks pins a mechanism, not a
// style. app.Save would fire OnRecordAfterUpdateSuccess, which is precisely what
// schedules an install — the record would save itself in an endless loop. Both
// spellings of the write, by id and by name, have to stay out of that.
func TestSetDepsState_WritesWithoutFiringTheSaveHooks(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "state-write",
		"console.log('hi')", `{"dependencies":{}}`)

	var saves atomic.Int32
	app.OnRecordAfterUpdateSuccess(faasboxFunctionsCollection).BindFunc(func(e *core.RecordEvent) error {
		saves.Add(1)
		return e.Next()
	})

	cases := []struct {
		label  string
		write  func()
		status string
		errMsg string
	}{
		{"by id", func() { setDepsState(app, record.Id, "state-write", depsStatusError, "boom") }, depsStatusError, "boom"},
		{"by name", func() { setDepsStateByName(app, "state-write", depsStatusReady, "") }, depsStatusReady, ""},
	}

	for _, tc := range cases {
		tc.write()

		stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
		if err != nil {
			t.Fatal(err)
		}
		if got := stored.GetString("depsStatus"); got != tc.status {
			t.Errorf("%s: depsStatus = %q, want %q", tc.label, got, tc.status)
		}
		if got := stored.GetString("depsError"); got != tc.errMsg {
			t.Errorf("%s: depsError = %q, want %q", tc.label, got, tc.errMsg)
		}
		// The update targets the two dependency columns and nothing else.
		if got := stored.GetString("script"); got != "console.log('hi')" {
			t.Errorf("%s: script = %q, want it untouched by the state write", tc.label, got)
		}
		if got := saves.Load(); got != 0 {
			t.Fatalf("%s: the record save hooks fired %d times — the install loop is back", tc.label, got)
		}
	}
}

func TestEnsureFunctionsCollection_AddsDepsFieldsToExistingCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// A collection predating the dependency state fields.
	col := core.NewBaseCollection(faasboxFunctionsCollection)
	col.Fields.Add(
		&core.TextField{Name: "name", Required: true},
		&core.TextField{Name: "env", Hidden: true},
		&core.JSONField{Name: "plainEnv"},
		&core.TextField{Name: "script"},
		&core.TextField{Name: "packageJson"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)
	if err := app.Save(col); err != nil {
		t.Fatal(err)
	}

	existing := core.NewRecord(col)
	existing.Set("name", "legacy")
	existing.Set("script", "console.log('legacy')")
	if err := app.Save(existing); err != nil {
		t.Fatal(err)
	}

	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	migrated, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}

	status, ok := migrated.Fields.GetByName("depsStatus").(*core.SelectField)
	if !ok {
		t.Fatal("depsStatus is missing or is not a SelectField")
	}
	want := []string{depsStatusPending, depsStatusInstalling, depsStatusReady, depsStatusError}
	if len(status.Values) != len(want) {
		t.Fatalf("depsStatus values = %v, want %v", status.Values, want)
	}
	for i, v := range want {
		if status.Values[i] != v {
			t.Fatalf("depsStatus values = %v, want %v", status.Values, want)
		}
	}
	if status.IsMultiple() {
		t.Error("depsStatus must hold a single value")
	}

	errField, ok := migrated.Fields.GetByName("depsError").(*core.TextField)
	if !ok {
		t.Fatal("depsError is missing or is not a TextField")
	}
	if errField.Max < maxDepsError {
		t.Errorf("depsError Max = %d, want at least %d", errField.Max, maxDepsError)
	}

	// The existing record must come through the migration intact.
	kept, err := app.FindRecordById(faasboxFunctionsCollection, existing.Id)
	if err != nil {
		t.Fatalf("the record saved before the migration is gone: %v", err)
	}
	if got := kept.GetString("script"); got != "console.log('legacy')" {
		t.Errorf("script = %q, want it preserved by the migration", got)
	}
	if got := kept.GetString("depsStatus"); got != "" {
		t.Errorf("depsStatus = %q, want empty on a record that predates the field", got)
	}
}

func TestDefaultFunctionsDir(t *testing.T) {
	dir := defaultFunctionsDir()
	if dir != filepath.Join(".", "functions") {
		t.Errorf("defaultFunctionsDir() = %q, want %q", dir, filepath.Join(".", "functions"))
	}
}
