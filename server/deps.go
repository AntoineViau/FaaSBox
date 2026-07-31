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

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// depsMu holds a per-directory mutex to prevent concurrent bun install runs.
var depsMu sync.Map // map[string]*sync.Mutex

// depsHashFile records the dependency spec node_modules was built for. It lives
// inside node_modules so that removing the directory also invalidates the marker.
const depsHashFile = ".faasbox-deps"

// Installation states published on the faasbox_functions record. An empty value
// means the function declares no dependencies at all.
const (
	depsStatusPending    = "pending"    // install requested, not started yet
	depsStatusInstalling = "installing" // bun install is running
	depsStatusReady      = "ready"      // node_modules matches the current spec
	depsStatusError      = "error"      // last install failed, see depsError
)

// maxDepsError bounds the bun install output kept in depsError. A TextField with
// no explicit Max rejects the whole record past 5000 runes, and an install failure
// can print far more than that — the cap and the field size must move together.
const maxDepsError = 4 << 10

func newDepsStatusField() *core.SelectField {
	return &core.SelectField{
		Name:   "depsStatus",
		Values: []string{depsStatusPending, depsStatusInstalling, depsStatusReady, depsStatusError},
	}
}

func newDepsErrorField() *core.TextField {
	return &core.TextField{Name: "depsError", Max: maxDepsError + logMarkerSlack}
}

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
	// Normalise first: the per-directory lock below is keyed on this string, so a
	// caller passing a relative path would take a different lock than the
	// invocation path for the very same directory — and two bun installs would
	// then run concurrently on it.
	funcDir, err := filepath.Abs(funcDir)
	if err != nil {
		return fmt.Errorf("cannot resolve function directory: %w", err)
	}

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

	// --ignore-scripts: npm lifecycle scripts would run outside every guard the
	// invocation path provides (no execution timeout, no bounded output capture),
	// so a single malicious transitive dependency would get a free hand.
	args := []string{"install", "--ignore-scripts"}
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

// scheduleDepsInstall installs the dependencies of a saved function in the
// background. The save moment is when the dependency spec is known to have
// changed, so the install belongs here rather than on the invocation path, where
// the caller would wait up to depsTimeout for it.
//
// ctx is the server lifecycle context: a shutdown cancels an install in flight
// instead of leaving it orphaned. The install runs detached from the save, which
// returns immediately.
func scheduleDepsInstall(ctx context.Context, app core.App, record *core.Record, functionsDir string) {
	name := record.GetString("name")
	if name == "" || !validName.MatchString(name) || len(name) > 64 {
		return
	}

	if record.GetString("packageJson") == "" {
		// No dependency spec: the state goes back to empty. Clearing unconditionally
		// covers the package.json that was just removed, where a leftover "ready"
		// would outlive its subject.
		setDepsState(app, record.Id, "", "")
		return
	}

	setDepsState(app, record.Id, depsStatusPending, "")

	recordId := record.Id
	funcDir := filepath.Join(functionsDir, name)

	go func() {
		setDepsState(app, recordId, depsStatusInstalling, "")

		installCtx, cancel := context.WithTimeout(ctx, depsTimeout)
		defer cancel()

		// ensureDeps returns on the hash check alone when the spec is unchanged,
		// so a save that only touched the script costs nothing here.
		if err := ensureDeps(installCtx, funcDir); err != nil {
			// A shutdown interrupted the install, it did not fail: the work is still
			// owed, and the safety net on the invocation path will do it. Reporting
			// an error here would outlive the restart and accuse the dependencies.
			if ctx.Err() != nil {
				setDepsState(app, recordId, depsStatusPending, "")
				return
			}
			app.Logger().Error("faasbox: dependency install failed", "function", name, "error", err)
			msg, _ := truncateForLog(err.Error(), maxDepsError)
			setDepsState(app, recordId, depsStatusError, msg)
			return
		}

		setDepsState(app, recordId, depsStatusReady, "")
	}()
}

// setDepsState publishes the installation state on the function record. The write
// is a direct SQL update on purpose: app.Save would fire OnRecordAfterUpdateSuccess,
// which is precisely what triggered this install — the record would save itself in
// an endless loop.
func setDepsState(app core.App, recordId, status, errMsg string) {
	_, err := app.DB().NewQuery(
		"UPDATE " + faasboxFunctionsCollection + " SET depsStatus = {:status}, depsError = {:err} WHERE id = {:id}",
	).Bind(dbx.Params{
		"status": status,
		"err":    errMsg,
		"id":     recordId,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to record dependency state",
			"recordId", recordId, "status", status, "error", err)
	}
}
