package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

// TestPublishDepsOutcome_InterruptionLeavesTheInstallOwed is the invocation-path
// half of what runDepsInstall has always done for the save path. A cancelled
// context is not a failed install: publishing error would put a diagnosis on the
// record — and push it live to the open editor — for an HTTP client that merely
// hung up, and the qualification it would carry accuses the memory.
func TestPublishDepsOutcome_InterruptionLeavesTheInstallOwed(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "hung-up",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)
	setDepsState(app, record.Id, "hung-up", depsStatusInstalling, "")

	cause := fmt.Errorf("%w: resolving dependencies", errDepsInterrupted)
	publishDepsOutcome(app, functionsDir, record.Id, "hung-up",
		execResult{Err: &errDepsFailed{Cause: cause}})

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusPending {
		t.Errorf("depsStatus = %q, want %q — the work is still owed", got, depsStatusPending)
	}
	if got := functionDepsError(app, stored); got != "" {
		t.Errorf("depsError = %q, want no diagnosis for an install nobody let finish", got)
	}
}

// writeLockfile puts a bun.lock in a function's directory — named by its id, as
// on disk — the way a successful install would have.
func writeLockfile(t testing.TB, functionsDir, functionId, content string) {
	t.Helper()
	dir := filepath.Join(functionsDir, functionId)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPersistLockfile_StoresWhatTheInstallResolved(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "pinned",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)

	const lock = `{"lockfileVersion":1,"packages":{"dayjs":["dayjs@1.11.13"]}}`
	writeLockfile(t, functionsDir, record.Id, lock)

	persistLockfile(app, functionsDir, record.Id)

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := functionBunLock(app, stored); got != lock {
		t.Errorf("bunLock = %q, want the resolved lockfile %q", got, lock)
	}
}

// TestPersistLockfile_NoLockfileLeavesTheRecordAlone covers the early return: a
// function whose install resolved nothing to pin is not an anomaly.
func TestPersistLockfile_NoLockfileLeavesTheRecordAlone(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "unpinned", "console.log('hi')", "")
	setBunLock(app, record.Id, "sentinel")

	persistLockfile(app, functionsDir, record.Id) // no bun.lock on disk

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := functionBunLock(app, stored); got != "sentinel" {
		t.Errorf("bunLock = %q, want the sentinel untouched", got)
	}
}

// TestPersistLockfile_TooLargeKeepsThePreviousValue pins the choice not to
// truncate. A cut lockfile is not degraded information, it is invalid input — and
// a stale one still pins everything that did not change.
func TestPersistLockfile_TooLargeKeepsThePreviousValue(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	enableServerLogs(t, app)

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "huge-lock",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)

	const previous = `{"lockfileVersion":1}`
	setBunLock(app, record.Id, previous)

	writeLockfile(t, functionsDir, record.Id, strings.Repeat("x", maxLockfileSize+1))

	persistLockfile(app, functionsDir, record.Id)

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := functionBunLock(app, stored); got != previous {
		t.Errorf("bunLock = %q (%d bytes), want the previous value %q intact",
			got, len(got), previous)
	}

	// Nothing tells the user: the server log is the only trace.
	messages := serverLogMessages(t, app)
	found := false
	for _, m := range messages {
		if strings.Contains(m, "lockfile too large") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("server logs = %v, want a warning about the oversized lockfile", messages)
	}
}

// TestSetBunLock_WritesOnlyItsOwnColumn is what forbids app.Save here: the write
// happens from a path holding a record loaded up to a minute earlier, so rewriting
// the whole record would discard a user save made in the meantime.
func TestSetBunLock_WritesOnlyItsOwnColumn(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "targeted",
		"console.log('original')", `{"dependencies":{}}`)
	setDepsState(app, record.Id, "targeted", depsStatusInstalling, "sentinel")

	setBunLock(app, record.Id, "resolved")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := functionBunLock(app, stored); got != "resolved" {
		t.Errorf("bunLock = %q, want %q", got, "resolved")
	}
	if got := stored.GetString("script"); got != "console.log('original')" {
		t.Errorf("script = %q, want it untouched by the lockfile write", got)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusInstalling {
		t.Errorf("depsStatus = %q, want it untouched by the lockfile write", got)
	}
	if got := functionDepsError(app, stored); got != "sentinel" {
		t.Errorf("depsError = %q, want it untouched by the lockfile write", got)
	}
}

func TestScheduleDepsInstall_PersistsTheResolvedLockfile(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	fakeBun(t, "mkdir -p node_modules\necho resolved-by-bun > bun.lock\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "save-pins",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	stored := waitDepsStatus(t, app, record.Id, depsStatusReady)

	if got := strings.TrimSpace(functionBunLock(app, stored)); got != "resolved-by-bun" {
		t.Errorf("bunLock = %q, want what the install resolved", got)
	}
}

func TestScheduleDepsInstall_FailedInstallLeavesTheLockfileAlone(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	// The install writes a lockfile and *then* fails: only the outcome must decide
	// whether it is kept, not whether bun happened to touch the file.
	fakeBun(t, "echo half-written > bun.lock\necho 'error: nope' >&2\nexit 1")
	record := saveTestFunction(t, app, functionsDir, "failed-install",
		"console.log('hi')", `{"dependencies":{"nope":"1.0.0"}}`)

	const previous = `{"lockfileVersion":1}`
	setBunLock(app, record.Id, previous)

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	stored := waitDepsStatus(t, app, record.Id, depsStatusError)

	if got := functionBunLock(app, stored); got != previous {
		t.Errorf("bunLock = %q, want the previous value %q after a failed install", got, previous)
	}
}
