package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// execTimeout bounds the function subprocess. The dependency install has its own
// budget, declared and applied in deps.go.
const execTimeout = 30 * time.Second

// errNotFound indicates the function script does not exist.
type errNotFound struct{ Name string }

func (e *errNotFound) Error() string { return fmt.Sprintf("function %q not found", e.Name) }

// errDepsFailed indicates dependency installation failed.
type errDepsFailed struct{ Cause error }

func (e *errDepsFailed) Error() string {
	return fmt.Sprintf("failed to install dependencies: %s", e.Cause)
}
func (e *errDepsFailed) Unwrap() error { return e.Cause }

type execResult struct {
	Stdout   string
	Stderr   string
	Duration time.Duration
	Err      error
	TimedOut bool
	// Truncated reports that either stream hit the capture cap; it is the flag
	// surfaced to callers. StdoutTruncated isolates the stream carrying the
	// result, the only one whose loss makes the output unusable.
	Truncated       bool
	StdoutTruncated bool
	ExitCode        int
	// DepsInstalled reports that ensureDeps actually ran bun install rather than
	// leaving on the hash check. Callers publish the installation state on the
	// record only then; the engine itself does not know the database.
	DepsInstalled bool
}

// executeFunction validates, installs deps, and runs a function in a Bun subprocess.
// It returns an execResult with the outcome. Validation and setup errors are returned
// in execResult.Err with a sentinel prefix so callers can distinguish them.
// extraEnv is an optional list of "KEY=value" strings injected into the subprocess environment.
//
// Only functionId is checked, because it is the only one that builds a path.
// The name is not: it reaches the subprocess environment as FUNCTION_NAME and
// the text of a not-found message, neither of which a strange name can subvert —
// execve does not reparse an entry, so an "=" or a newline inside the value stays
// inside it, and e.JSON escapes the message.
//
// Rejecting on the name is what this function used to do, and it made a function
// the editor accepted uninvocable by *any* spelling, id included: an ordinary
// error, so a 500 rather than a 404, recorded in faasbox_logs on every call —
// once a minute for anything carrying a trigger. An identifier that cannot build
// a path must not be able to stop an execution. Refusing such a name at save
// time is the other half of the question, and it belongs to the collection, not
// to the engine.
func executeFunction(ctx context.Context, functionsDir, functionId, name, payload string, extraEnv []string) execResult {
	// 1. Validate the identifier that builds the path
	if !validName.MatchString(functionId) || len(functionId) > 64 {
		return execResult{Err: fmt.Errorf("invalid function id: %s", functionId)}
	}

	// 2. Resolve and verify the script path
	absDir, err := filepath.Abs(functionsDir)
	if err != nil {
		return execResult{Err: fmt.Errorf("cannot resolve functions directory: %w", err)}
	}
	scriptPath := filepath.Join(absDir, functionId, "index.ts")
	if _, err := os.Stat(scriptPath); err != nil {
		return execResult{Err: &errNotFound{Name: name}}
	}

	// 3. Install dependencies if needed. No deadline is set here: ensureDeps takes
	// its own once it holds the directory lock, so that queueing behind another
	// install does not eat this caller's budget.
	funcDir := filepath.Join(absDir, functionId)
	// Timed like the subprocess below: an error response publishes duration_ms,
	// and a failure that waited a full minute must not report zero. The wait for
	// the lock is inside ensureDeps, hence inside this measure — the caller really
	// did wait that long.
	depsStart := time.Now()
	depsInstalled, err := ensureDeps(ctx, funcDir)
	if err != nil {
		return execResult{Err: &errDepsFailed{Cause: err}, Duration: time.Since(depsStart)}
	}

	// 4. Execute with timeout
	execCtx, execCancel := context.WithTimeout(ctx, execTimeout)
	defer execCancel()

	cmd := exec.CommandContext(execCtx, "bun", "run", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Stdin = bytes.NewReader([]byte(payload))
	cmd.Dir = absDir
	reserved := map[string]bool{"HOME": true, "PATH": true, "NODE_ENV": true, "FUNCTION_NAME": true}
	var safeEnv []string
	for _, e := range extraEnv {
		key, _, _ := strings.Cut(e, "=")
		if !reserved[key] {
			safeEnv = append(safeEnv, e)
		}
	}
	cmd.Env = append([]string{
		"HOME=/tmp",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"NODE_ENV=production",
		"FUNCTION_NAME=" + name,
	}, safeEnv...)

	stdout := newLimitedWriter()
	stderr := newLimitedWriter()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	err = cmd.Run()
	duration := time.Since(start)

	res := execResult{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		Duration:        duration,
		Truncated:       stdout.truncated || stderr.truncated,
		StdoutTruncated: stdout.truncated,
		DepsInstalled:   depsInstalled,
	}

	if err != nil {
		res.Err = err
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}

	return res
}
