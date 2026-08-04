package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/pocketbase/pocketbase/core"
)

// This file carries the startup dependency pass: reinstalling what the disk lost
// while the record kept saying it was there. deps.go runs an install, depsstate.go
// records its outcome; here we only decide who needs one at boot.

// installMissingDeps reinstalls what the disk has lost. It is the startup
// counterpart of scheduleDepsInstall: the disk may have been rebuilt from the
// database — new machine, restored database, directory wiped — in which case
// node_modules is gone while the record still announces ready.
//
// Serialised on purpose. One install per function in parallel would multiply the
// memory peak by their number, and a process killed for that restarts to do
// exactly the same thing: a restart loop that never converges. One install at a
// time brings the peak back to the one the save path already carries.
//
// No retry is planned, and that is deliberate. The safety net on the invocation
// path calls ensureDeps too: a function whose startup install failed tries again
// on its next HTTP call or cron trigger. This pass is an optimisation — the first
// caller does not pay the wait — not a guarantee. The rule settling every failure
// case: a failed pass must never leave the system worse off than if it had not run.
func installMissingDeps(ctx context.Context, app core.App, functionsDir string) {
	records, err := app.FindAllRecords(faasboxFunctionsCollection)
	if err != nil {
		app.Logger().Error("faasbox: failed to load functions for the startup install", "error", err)
		return
	}

	for _, record := range records {
		// Checked before each function, not only around the install: a shutdown
		// must not wait for the rest of the list to be walked.
		if ctx.Err() != nil {
			return
		}

		name := record.GetString("name")
		if name == "" || !validName.MatchString(name) || len(name) > 64 {
			continue
		}
		if record.GetString("packageJson") == "" {
			continue
		}
		if depsUpToDate(filepath.Join(functionsDir, name)) {
			continue
		}

		app.Logger().Info("faasbox: installing missing dependencies", "function", name)

		// A failure is published on the record and the pass moves on: one broken
		// function must not deprive the others of their install.
		if interrupted := runDepsInstall(ctx, app, functionsDir, record.Id, name); interrupted {
			return
		}
	}
}

// depsUpToDate reports whether node_modules was built for the current dependency
// spec. It is an advisory filter, taken outside the lock: ensureDeps redoes the
// check under it, and that one alone is authoritative.
//
// Both ways to be wrong are benign. A false positive skips the preinstall, and the
// safety net on the invocation path catches it; a false negative calls ensureDeps,
// which leaves on its own check. What the filter buys is a warm restart costing
// one fingerprint per function and zero writes — without it, every function would
// be published installing then ready for nothing, flickering in an open editor.
func depsUpToDate(funcDir string) bool {
	want, err := depsHash(funcDir)
	if err != nil {
		return false
	}
	got, err := os.ReadFile(filepath.Join(funcDir, "node_modules", depsHashFile))
	return err == nil && string(got) == want
}
