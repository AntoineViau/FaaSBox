package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// depsMu holds a per-directory mutex to prevent concurrent bun install runs.
var depsMu sync.Map // map[string]*sync.Mutex

// depsTimeout bounds a single bun install. It lives here because ensureDeps is
// what applies it, holding the directory lock: no caller gets to set this budget,
// and none may add one of its own on top.
//
// A var rather than a const, so a test can shrink it: the property that matters —
// when the clock starts — is only observable when it expires, and no test waits a
// minute for that.
var depsTimeout = 60 * time.Second

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

// errDepsInterrupted qualifies an install cut short because its context was
// cancelled — a server shutdown, or an HTTP client that hung up while bun was
// running. It is not a failure: nothing is wrong with the dependencies, the work
// is simply still owed, and the next invocation reinstalls.
//
// Without it the cancellation is indistinguishable from an out-of-memory kill:
// exec.CommandContext kills through Process.Kill, so the process comes back as
// "signal: killed" either way, and the qualification below would blame the memory
// for a process this very server killed. That verdict does not stay in one place —
// it lands on the record, in faasbox_logs, in the server log and, through the
// realtime channel, in the open editor.
var errDepsInterrupted = errors.New("dependency install was interrupted")

// ensureDeps runs "bun install" in funcDir when the dependency spec changed since
// the last successful install, starting from an emptied node_modules (see below).
// A per-directory lock prevents concurrent installs for the same function.
//
// It owns depsTimeout, and takes it only once the lock is held (see below): the
// callers hand it their own context, untouched.
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

	// The deadline starts here and not at the caller: waiting for another
	// goroutine's install must not eat this one's budget, or a caller that queued
	// behind a slow failure returns a "timed out" accusing an install that never
	// had the time to run. The derived context stays a child, so a shutdown
	// cancelling the parent still propagates.
	ctx, cancel := context.WithTimeout(ctx, depsTimeout)
	defer cancel()

	want, err := depsHash(funcDir)
	if err != nil {
		return false, nil // package.json vanished between the check and the lock
	}

	// Skip install if node_modules was built for this exact dependency spec
	modulesPath := filepath.Join(funcDir, "node_modules")
	if got, err := os.ReadFile(filepath.Join(modulesPath, depsHashFile)); err == nil && string(got) == want {
		return false, nil
	}

	// bun does not remove from node_modules what package.json no longer declares:
	// it updates the lockfile, announces "N packages removed", and leaves the
	// directories in place, importable. The code would then run against a tree its
	// own spec no longer describes — until a restart on a fresh filesystem, where
	// the tree is rebuilt from package.json alone and the import breaks. Starting
	// over is the only remedy: bun exposes no equivalent to `npm prune`, and
	// neither --force nor --linker=isolated cleans up.
	//
	// os.RemoveAll on a missing path returns nil, so the "no node_modules yet"
	// case needs no prior check.
	if err := os.RemoveAll(modulesPath); err != nil {
		// Best effort: failing here would break a function for a reason foreign to
		// its dependencies. We lose the guarantee, not the install.
		log.Printf("faasbox: cannot clear %s before install: %v", modulesPath, err)
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

		// Cancelled rather than expired: the caller went away. Qualified here, ahead
		// of the SIGKILL branch that would otherwise accuse the memory — the kill
		// came from this server.
		//
		// The two are distinguishable because neither caller context carries a
		// deadline of its own: the server lifecycle context has none, and an HTTP
		// request context is cancelled, never expired. Only the WithTimeout derived
		// above sets one. So DeadlineExceeded names the install budget, and Canceled
		// names the caller — there is no overlap to arbitrate.
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, fmt.Errorf("%w: %s", errDepsInterrupted, output)
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
	if !validName.MatchString(record.Id) || len(record.Id) > 64 {
		return
	}
	name := record.GetString("name")

	if record.GetString("packageJson") == "" {
		// No dependency spec: the state goes back to empty. Clearing unconditionally
		// covers the package.json that was just removed, where a leftover "ready"
		// would outlive its subject.
		setDepsState(app, record.Id, name, "", "")
		return
	}

	setDepsState(app, record.Id, name, depsStatusPending, "")

	recordId := record.Id
	go runDepsInstall(ctx, app, functionsDir, recordId, name)
}

// runDepsInstall installs one function's dependencies and publishes the outcome
// on its record. It is shared by the save trigger and the startup pass, which
// differ in what brings them here, not in what happens once they arrive.
//
// interrupted reports a shutdown rather than a failure, which is what the startup
// pass reads to stop where it stands. A save has a single function to install and
// ignores it.
//
// recordId names the directory as well as the row; name is carried for the log
// lines and the broadcast alone.
//
// The install budget is not set here: ensureDeps owns it (see depsTimeout).
func runDepsInstall(ctx context.Context, app core.App, functionsDir, recordId, name string) (interrupted bool) {
	setDepsState(app, recordId, name, depsStatusInstalling, "")

	// ensureDeps returns on the hash check alone when the spec is unchanged, so a
	// save that only touched the script costs nothing here. Whether it installed is
	// of no use: this path publishes the state either way, having claimed it from
	// installing onwards.
	if _, err := ensureDeps(ctx, filepath.Join(functionsDir, recordId)); err != nil {
		// A shutdown interrupted the install, it did not fail: the work is still
		// owed, and the safety net on the invocation path will do it. Reporting
		// an error here would outlive the restart and accuse the dependencies.
		//
		// Read off the qualified cause rather than ctx.Err(): the same signal the
		// invocation path reads, so the two cannot drift. It is also the narrower
		// test — a genuine bun failure that a shutdown happens to follow by a
		// microsecond is a failure, and stays one.
		if errors.Is(err, errDepsInterrupted) {
			setDepsState(app, recordId, name, depsStatusPending, "")
			return true
		}
		app.Logger().Error("faasbox: dependency install failed", "function", name, "error", err)
		msg, _ := truncateForLog(err.Error(), maxDepsError)
		setDepsState(app, recordId, name, depsStatusError, msg)
		return false
	}

	// Before the state, so that a record announcing "ready" already carries
	// what the install resolved.
	persistLockfile(app, functionsDir, recordId)
	setDepsState(app, recordId, name, depsStatusReady, "")
	return false
}
