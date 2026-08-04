package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// depsStatusOf reads the published installation state of a function.
func depsStatusOf(t *testing.T, app core.App, recordId string) string {
	t.Helper()
	record, err := app.FindRecordById(faasboxFunctionsCollection, recordId)
	if err != nil {
		t.Fatal(err)
	}
	return record.GetString("depsStatus")
}

// TestInstallMissingDeps_ReinstallsWhatTheDiskLost covers the case the pass
// exists for: the record says ready, the filesystem was rebuilt from the
// database, and node_modules is nowhere to be found.
func TestInstallMissingDeps_ReinstallsWhatTheDiskLost(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	runs := fakeBun(t, "printf 'lock-v1' > bun.lock\nmkdir -p node_modules\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "with-deps",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)
	setDepsState(app, record.Id, "with-deps", depsStatusReady, "")

	installMissingDeps(context.Background(), app, functionsDir)

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusReady {
		t.Errorf("depsStatus = %q, want %q", got, depsStatusReady)
	}
	if got := stored.GetString("depsError"); got != "" {
		t.Errorf("depsError = %q, want empty on success", got)
	}
	// The lockfile the install resolved must reach the record, or the pinning would
	// not outlive the next restart any better than the node_modules just lost.
	if got := stored.GetString("bunLock"); got != "lock-v1" {
		t.Errorf("bunLock = %q, want the lockfile the install resolved", got)
	}
	if got := runs(); got != 1 {
		t.Errorf("expected 1 install, got %d", got)
	}
}

func TestInstallMissingDeps_PublishesInstallingFirst(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	fakeBun(t, "sleep 0.4\nmkdir -p node_modules\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "slow-deps",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)

	done := make(chan struct{})
	go func() {
		defer close(done)
		installMissingDeps(context.Background(), app, functionsDir)
	}()

	// The editor follows the record, so the pass owes it the running state and not
	// just the outcome.
	waitDepsStatus(t, app, record.Id, depsStatusInstalling)
	waitDepsStatus(t, app, record.Id, depsStatusReady)
	<-done
}

// TestInstallMissingDeps_SkipsWhatDoesNotNeedIt covers the three exits taken
// before anything is engaged. The one that matters on every warm restart is the
// fingerprint: without it each function would be published installing then ready
// for nothing, flickering in an open editor.
func TestInstallMissingDeps_SkipsWhatDoesNotNeedIt(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	runs := installCounter(t)

	const pkg = `{"dependencies":{"left-pad":"1.0.0"}}`
	upToDate := saveTestFunction(t, app, functionsDir, "warm", "console.log('hi')", pkg)
	noDeps := saveTestFunction(t, app, functionsDir, "bare", "console.log('hi')", "")
	// A name that never built a directory: the pass must not go looking for one.
	badName := saveTestFunction(t, app, functionsDir, "not a name", "console.log('hi')", pkg)

	warmDir := filepath.Join(functionsDir, "warm")
	hash, err := depsHash(warmDir)
	if err != nil {
		t.Fatal(err)
	}
	writeDepsMarker(t, warmDir, hash)

	installMissingDeps(context.Background(), app, functionsDir)

	for _, tc := range []struct {
		label  string
		record *core.Record
	}{
		{"up to date", upToDate},
		{"without package.json", noDeps},
		{"with an invalid name", badName},
	} {
		if got := depsStatusOf(t, app, tc.record.Id); got != "" {
			t.Errorf("%s: depsStatus = %q, want the record left alone", tc.label, got)
		}
	}
	if got := runs(); got != 0 {
		t.Errorf("expected no install, got %d", got)
	}
}

// TestInstallMissingDeps_InstallsOneAtATime pins the serialisation. Installing
// every function at once would multiply the memory peak by their number, and a
// process killed for that comes back to do exactly the same thing.
func TestInstallMissingDeps_InstallsOneAtATime(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	fakeBun(t, "echo start >> \""+trace+"\"\nsleep 0.2\necho end >> \""+trace+"\"\nmkdir -p node_modules\nexit 0")

	const pkg = `{"dependencies":{"left-pad":"1.0.0"}}`
	saveTestFunction(t, app, functionsDir, "first", "console.log('hi')", pkg)
	saveTestFunction(t, app, functionsDir, "second", "console.log('hi')", pkg)

	installMissingDeps(context.Background(), app, functionsDir)

	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "end", "start", "end"}
	got := strings.Fields(string(data))
	if len(got) != len(want) {
		t.Fatalf("install trace = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("install trace = %v, want %v — the installs overlapped", got, want)
		}
	}
}

// TestInstallMissingDeps_FailureDoesNotStopThePass keeps a broken function from
// depriving the others of their install. The stub fails on whichever function is
// reached first, so the assertion does not depend on the order records come back
// in.
func TestInstallMissingDeps_FailureDoesNotStopThePass(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	once := filepath.Join(t.TempDir(), "first-install")
	runs := fakeBun(t, "if [ ! -f \""+once+"\" ]; then\n"+
		"  touch \""+once+"\"\n"+
		"  echo \"error: package nope@1.0.0 not found\" >&2\n"+
		"  exit 1\n"+
		"fi\nmkdir -p node_modules\nexit 0")

	const pkg = `{"dependencies":{"nope":"1.0.0"}}`
	first := saveTestFunction(t, app, functionsDir, "one", "console.log('hi')", pkg)
	second := saveTestFunction(t, app, functionsDir, "two", "console.log('hi')", pkg)

	installMissingDeps(context.Background(), app, functionsDir)

	statuses := map[string]string{
		"one": depsStatusOf(t, app, first.Id),
		"two": depsStatusOf(t, app, second.Id),
	}
	var failed, ready string
	for name, status := range statuses {
		switch status {
		case depsStatusError:
			failed = name
		case depsStatusReady:
			ready = name
		}
	}
	if failed == "" || ready == "" {
		t.Fatalf("statuses = %v, want one error and one ready", statuses)
	}
	if got := runs(); got != 2 {
		t.Errorf("expected the pass to attempt both installs, got %d", got)
	}

	record, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", failed)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.GetString("depsError"); !strings.Contains(got, "nope@1.0.0 not found") {
		t.Errorf("depsError = %q, want the bun install output", got)
	}
}

// TestInstallMissingDeps_ShutdownStopsThePass covers the two halves of an
// interruption: the install in flight is owed, not failed, and the rest of the
// list is dropped rather than walked while the server is going down.
func TestInstallMissingDeps_ShutdownStopsThePass(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	fakeBun(t, "sleep 5\nexit 0")

	const pkg = `{"dependencies":{"left-pad":"1.0.0"}}`
	first := saveTestFunction(t, app, functionsDir, "one", "console.log('hi')", pkg)
	second := saveTestFunction(t, app, functionsDir, "two", "console.log('hi')", pkg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		installMissingDeps(ctx, app, functionsDir)
	}()

	running := waitInstallingRecord(t, app, first.Id, second.Id)
	cancel() // server shutdown

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass outlived the shutdown")
	}

	stored := waitDepsStatus(t, app, running, depsStatusPending)
	if got := stored.GetString("depsError"); got != "" {
		t.Errorf("depsError = %q, want empty after an interrupted install", got)
	}

	other := first.Id
	if running == first.Id {
		other = second.Id
	}
	if got := depsStatusOf(t, app, other); got != "" {
		t.Errorf("depsStatus = %q on the next function, want the pass stopped before it", got)
	}
}

// waitInstallingRecord returns the id of the first candidate reaching installing.
func waitInstallingRecord(t *testing.T, app core.App, ids ...string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, id := range ids {
			if depsStatusOf(t, app, id) == depsStatusInstalling {
				return id
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no function reached installing")
	return ""
}
