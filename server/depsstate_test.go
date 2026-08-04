package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

// writeLockfile puts a bun.lock in a function's directory, as a successful install
// would have.
func writeLockfile(t testing.TB, functionsDir, name, content string) {
	t.Helper()
	dir := filepath.Join(functionsDir, name)
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
	writeLockfile(t, functionsDir, "pinned", lock)

	persistLockfile(app, functionsDir, "pinned")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("bunLock"); got != lock {
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
	setBunLock(app, "unpinned", "sentinel")

	persistLockfile(app, functionsDir, "unpinned") // no bun.lock on disk

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("bunLock"); got != "sentinel" {
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
	setBunLock(app, "huge-lock", previous)

	writeLockfile(t, functionsDir, "huge-lock", strings.Repeat("x", maxLockfileSize+1))

	persistLockfile(app, functionsDir, "huge-lock")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("bunLock"); got != previous {
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

	setBunLock(app, "targeted", "resolved")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("bunLock"); got != "resolved" {
		t.Errorf("bunLock = %q, want %q", got, "resolved")
	}
	if got := stored.GetString("script"); got != "console.log('original')" {
		t.Errorf("script = %q, want it untouched by the lockfile write", got)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusInstalling {
		t.Errorf("depsStatus = %q, want it untouched by the lockfile write", got)
	}
	if got := stored.GetString("depsError"); got != "sentinel" {
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

	if got := strings.TrimSpace(stored.GetString("bunLock")); got != "resolved-by-bun" {
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
	setBunLock(app, "failed-install", previous)

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	stored := waitDepsStatus(t, app, record.Id, depsStatusError)

	if got := stored.GetString("bunLock"); got != previous {
		t.Errorf("bunLock = %q, want the previous value %q after a failed install", got, previous)
	}
}
