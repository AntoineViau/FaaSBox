package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

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

// depsOutputSlack is the room left inside maxDepsError for everything the
// captured install output gets wrapped in: the longest message prefix built
// below (91 bytes for the out-of-memory wording), the head marker tailWriter
// adds when it drops bytes (some forty), and the "failed to install
// dependencies: " an errDepsFailed puts in front on the invocation path
// (32 bytes).
//
// It exists because truncateForLog stays applied to the finished message and
// cuts the *tail* — the very end this capture went looking for. Sizing the
// capture at maxDepsError would push the total past the cap and hand that last
// rampart exactly the bytes that carry the diagnosis. With this margin it never
// cuts, and remains what it is meant to be: a rampart, not a participant.
const depsOutputSlack = 256

// maxLockfileSize bounds what a record accepts of a lockfile. A TextField with no
// explicit Max rejects the whole record past 5000 runes, and a lockfile goes past
// that threshold on the very first real dependency.
const maxLockfileSize = 1 << 20 // 1 MB

func newDepsStatusField() *core.SelectField {
	return &core.SelectField{
		Name:   "depsStatus",
		Values: []string{depsStatusPending, depsStatusInstalling, depsStatusReady, depsStatusError},
	}
}

func newDepsErrorField() *core.TextField {
	return &core.TextField{Name: "depsError", Max: maxDepsError + logMarkerSlack}
}

// newBunLockField declares the column carrying the lockfile a successful install
// resolved. It is not Hidden, and that is not an oversight: autoResolveRecordsFlags
// unmasks every hidden field as soon as the request authenticates as superuser,
// which is what the editor does. Marking it would suggest a protection that does
// not exist, on a value that is no secret anyway. What the API hands back is kept
// down by the field selection the editor sends, not by this flag.
func newBunLockField() *core.TextField {
	return &core.TextField{Name: "bunLock", Max: maxLockfileSize}
}

// depsHash returns a hash of package.json and bun.lock (when present).
// bun.lock is part of the hash because it determines the resolved tree for
// everything it covers: a different lockfile is a different node_modules, even
// for an unchanged package.json.
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
//
// installed reports that bun install actually ran and succeeded. Every early
// return — no package.json, unchanged spec — reports false, which is what lets
// the invocation path tell a real install from the hash check it pays on every
// call, and write the state only for the former.
func ensureDeps(ctx context.Context, funcDir string) (installed bool, err error) {
	// Normalise first: the per-directory lock below is keyed on this string, so a
	// caller passing a relative path would take a different lock than the
	// invocation path for the very same directory — and two bun installs would
	// then run concurrently on it.
	funcDir, err = filepath.Abs(funcDir)
	if err != nil {
		return false, fmt.Errorf("cannot resolve function directory: %w", err)
	}

	pkgPath := filepath.Join(funcDir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return false, nil // no package.json → nothing to do
	}

	// Acquire a per-directory lock to prevent concurrent bun installs
	val, _ := depsMu.LoadOrStore(funcDir, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	want, err := depsHash(funcDir)
	if err != nil {
		return false, nil // package.json vanished between the check and the lock
	}

	// Skip install if node_modules was built for this exact dependency spec
	modulesPath := filepath.Join(funcDir, "node_modules")
	if got, err := os.ReadFile(filepath.Join(modulesPath, depsHashFile)); err == nil && string(got) == want {
		return false, nil
	}

	// --ignore-scripts: npm lifecycle scripts would run outside every guard the
	// invocation path provides (no execution timeout, no bounded output capture),
	// so a single malicious transitive dependency would get a free hand.
	//
	// No --frozen-lockfile. The flag does not mean "use the lockfile" but "use the
	// lockfile *and fail* if it does not already satisfy package.json". Without it
	// bun still honours the lockfile for everything it covers, resolves only what
	// changed, then updates it — the pinning keeps applying, only the refusal to
	// update goes away. That refusal is made for a CI, where the lockfile is a
	// versioned file read in review. Nobody here commits it, reads it or is shown
	// it, so the veto protected nothing and blocked the one gesture users make:
	// adding a dependency to an already installed function.
	args := []string{"install", "--ignore-scripts"}

	// Both streams land in the same bounded capture. bun appears to write
	// everything to stderr, but wiring stdout too costs nothing and settles the
	// question. The writer keeps the tail: bun prints its progress first and its
	// failure last, so the head is the half worth dropping.
	output := newTailWriter(maxDepsError - depsOutputSlack)
	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = funcDir
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = []string{
		"HOME=/tmp",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if err := cmd.Run(); err != nil {
		// exec.CommandContext kills through Process.Kill, so a deadline that
		// expires and a kill coming from outside both surface as "signal: killed".
		// Naming the cause is the whole point: an operator reading the field has
		// to know whether to raise the timeout or the memory.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, fmt.Errorf("dependency install timed out: %s", output)
		}

		// SIGKILL while the deadline still had room: something outside killed the
		// process. The kernel leaves nothing behind in its victim, so the cause
		// cannot be named — only ruled out as neither bun nor a timeout.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signal() == syscall.SIGKILL {
				return false, fmt.Errorf(
					"dependency install was killed by the system before it finished, most likely out of memory: %s",
					output,
				)
			}
		}

		// An ordinary bun failure — missing package, desynchronised lockfile. Its
		// own output carries the information and is already readable.
		return false, fmt.Errorf("%w: %s", err, output)
	}

	// Recomputed, not reused: bun rewrites bun.lock during the install, so the
	// hash taken before it no longer describes the directory. Storing the stale
	// one would have the next call find a mismatch and reinstall for nothing.
	// Best effort, like writing the marker itself.
	if got, err := depsHash(funcDir); err == nil {
		want = got
	}

	// Best-effort marker: if writing it fails we simply reinstall next time.
	if err := os.MkdirAll(modulesPath, 0o755); err == nil {
		os.WriteFile(filepath.Join(modulesPath, depsHashFile), []byte(want), 0o644)
	}
	return true, nil
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
		setDepsState(app, record.Id, name, "", "")
		return
	}

	setDepsState(app, record.Id, name, depsStatusPending, "")

	recordId := record.Id
	funcDir := filepath.Join(functionsDir, name)

	go func() {
		setDepsState(app, recordId, name, depsStatusInstalling, "")

		installCtx, cancel := context.WithTimeout(ctx, depsTimeout)
		defer cancel()

		// ensureDeps returns on the hash check alone when the spec is unchanged,
		// so a save that only touched the script costs nothing here. Whether it
		// installed is of no use here: this path publishes the state either way,
		// having claimed it from pending onwards.
		if _, err := ensureDeps(installCtx, funcDir); err != nil {
			// A shutdown interrupted the install, it did not fail: the work is still
			// owed, and the safety net on the invocation path will do it. Reporting
			// an error here would outlive the restart and accuse the dependencies.
			if ctx.Err() != nil {
				setDepsState(app, recordId, name, depsStatusPending, "")
				return
			}
			app.Logger().Error("faasbox: dependency install failed", "function", name, "error", err)
			msg, _ := truncateForLog(err.Error(), maxDepsError)
			setDepsState(app, recordId, name, depsStatusError, msg)
			return
		}

		// Before the state, so that a record announcing "ready" already carries
		// what the install resolved.
		persistLockfile(app, functionsDir, name)
		setDepsState(app, recordId, name, depsStatusReady, "")
	}()
}
