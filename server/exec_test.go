package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBunEnvDump puts a stub "bun" writing its own environment to a file on
// PATH, and returns a function parsing that dump.
//
// env prints the environment block as it was received, duplicates included, and
// in order. The parsed map therefore keeps the *last* occurrence of a key: a
// secret that escaped the reserved-name filter shows up here instead of being
// hidden behind the reserved value set before it.
func fakeBunEnvDump(t *testing.T) func(*testing.T) map[string]string {
	t.Helper()
	// env is called by absolute path: a test injecting a rogue PATH must observe
	// the filter, not a stub that failed to find its own tools.
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("env is not available: %v", err)
	}
	dumpPath := filepath.Join(t.TempDir(), "env")
	fakeBun(t, "\""+envBin+"\" > \""+dumpPath+"\"")

	return func(t *testing.T) map[string]string {
		t.Helper()
		data, err := os.ReadFile(dumpPath)
		if err != nil {
			t.Fatalf("the stub bun wrote no environment dump: %v", err)
		}
		env := map[string]string{}
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if key, value, ok := strings.Cut(line, "="); ok {
				env[key] = value
			}
		}
		return env
	}
}

// fileSize returns the size of path, failing the test if it cannot be read.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cannot stat %s: %v", path, err)
	}
	return info.Size()
}

// waitFileGrows waits until path holds at least min bytes.
func waitFileGrows(t *testing.T, path string, min int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() >= min {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never reached %d bytes: the subprocess never started", path, min)
}

func TestExecuteFunction_NominalRun(t *testing.T) {
	observed := t.TempDir()
	argsPath := filepath.Join(observed, "args")
	stdinPath := filepath.Join(observed, "stdin")
	runs := fakeBun(t, "echo \"$*\" > \""+argsPath+"\"\n"+
		"cat > \""+stdinPath+"\"\n"+
		"echo result\necho warning >&2\nexit 0")

	dir := setupTestFunctionsDir(t, map[string]string{"echo": ""})
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	res := executeFunction(context.Background(), dir, "echo", `{"ping":1}`, nil)

	if res.Err != nil {
		t.Fatalf("Err = %v, want nil (stderr: %s)", res.Err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.TimedOut || res.Truncated || res.StdoutTruncated {
		t.Errorf("TimedOut=%v Truncated=%v StdoutTruncated=%v, want all false",
			res.TimedOut, res.Truncated, res.StdoutTruncated)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %s, want a measured run", res.Duration)
	}
	if got := runs(); got != 1 {
		t.Errorf("the stub bun ran %d times, want 1", got)
	}

	// The script path is built from the resolved functions directory, so a
	// relative --functionsDir cannot make the subprocess run from elsewhere.
	wantArgs := "run " + filepath.Join(absDir, "echo", "index.ts")
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != wantArgs {
		t.Errorf("bun args = %q, want %q", got, wantArgs)
	}

	payload, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(payload); got != `{"ping":1}` {
		t.Errorf("stdin = %q, want the payload forwarded verbatim", got)
	}
	if got := strings.TrimSpace(res.Stdout); got != "result" {
		t.Errorf("Stdout = %q, want %q", got, "result")
	}
	if got := strings.TrimSpace(res.Stderr); got != "warning" {
		t.Errorf("Stderr = %q, want %q", got, "warning")
	}
}

func TestExecuteFunction_RejectsInvalidName(t *testing.T) {
	runs := fakeBun(t, "exit 0")

	// The over-long name gets a real script on disk: the rejection must happen on
	// the name alone, before the path is ever built or looked up.
	tooLong := strings.Repeat("a", 65)
	dir := setupTestFunctionsDir(t, map[string]string{tooLong: "", "ok": ""})

	cases := map[string]string{
		"empty":        "",
		"traversal":    "../etc",
		"nested path":  "a/b",
		"absolute":     "/etc/passwd",
		"leading dash": "-evil",
		"space":        "my func",
		"too long":     tooLong,
	}

	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			res := executeFunction(context.Background(), dir, name, "", nil)

			if res.Err == nil {
				t.Fatalf("Err = nil for name %q, want a rejection", name)
			}
			if !strings.Contains(res.Err.Error(), "invalid function name") {
				t.Errorf("Err = %v, want an invalid name error", res.Err)
			}
			var notFound *errNotFound
			if errors.As(res.Err, &notFound) {
				t.Errorf("Err = %v, want a name rejection rather than a missing script", res.Err)
			}
		})
	}

	if got := runs(); got != 0 {
		t.Errorf("the stub bun ran %d times, want 0 for names that must never reach it", got)
	}
}

func TestExecuteFunction_MissingScript(t *testing.T) {
	runs := fakeBun(t, "exit 0")
	dir := setupTestFunctionsDir(t, map[string]string{"present": ""})

	res := executeFunction(context.Background(), dir, "absent", "", nil)

	var notFound *errNotFound
	if !errors.As(res.Err, &notFound) {
		t.Fatalf("Err = %v, want *errNotFound", res.Err)
	}
	if notFound.Name != "absent" {
		t.Errorf("errNotFound.Name = %q, want %q", notFound.Name, "absent")
	}
	if got := runs(); got != 0 {
		t.Errorf("the stub bun ran %d times, want 0 when the script is missing", got)
	}
}

func TestExecuteFunction_DependencyFailurePreventsTheRun(t *testing.T) {
	runs := fakeBun(t, "echo \"error: package nope@1.0.0 not found\" >&2\nexit 1")
	dir := setupTestFunctionsDir(t, map[string]string{"needs-deps": ""})
	pkgPath := filepath.Join(dir, "needs-deps", "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{"nope":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := executeFunction(context.Background(), dir, "needs-deps", "", nil)

	var depsFailed *errDepsFailed
	if !errors.As(res.Err, &depsFailed) {
		t.Fatalf("Err = %v, want *errDepsFailed", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "nope@1.0.0 not found") {
		t.Errorf("Err = %v, want the install output carried to the caller", res.Err)
	}
	// One invocation of the stub: the install. A function whose dependencies are
	// missing must not run against an incomplete tree.
	if got := runs(); got != 1 {
		t.Errorf("the stub bun ran %d times, want 1 (the install alone)", got)
	}
}

// TestExecuteFunction_DependencyFailureIsTimed guards the field the error
// response publishes: an install that failed after a long wait used to report
// duration_ms = 0, which read as if nothing had happened.
func TestExecuteFunction_DependencyFailureIsTimed(t *testing.T) {
	fakeBun(t, "sleep 0.05\nexit 1")
	dir := setupTestFunctionsDir(t, map[string]string{"needs-deps": ""})
	pkgPath := filepath.Join(dir, "needs-deps", "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{"nope":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := executeFunction(context.Background(), dir, "needs-deps", "", nil)

	var depsFailed *errDepsFailed
	if !errors.As(res.Err, &depsFailed) {
		t.Fatalf("Err = %v, want *errDepsFailed", res.Err)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %s, want the dependency step timed like the run", res.Duration)
	}
	if res.Duration.Milliseconds() < 1 {
		t.Errorf("duration_ms would publish %d for a 50 ms install", res.Duration.Milliseconds())
	}
}

// TestExecuteFunction_ReportsWhatTheSafetyNetDid carries to the callers the one
// fact they need to decide whether to write the record: exec.go holds no
// core.App, so the engine reports and they publish.
func TestExecuteFunction_ReportsWhatTheSafetyNetDid(t *testing.T) {
	fakeBun(t, "mkdir -p node_modules\nexit 0")
	dir := setupTestFunctionsDir(t, map[string]string{"needs-deps": ""})
	pkgPath := filepath.Join(dir, "needs-deps", "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{"left-pad":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := executeFunction(context.Background(), dir, "needs-deps", "", nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want a clean run", res.Err)
	}
	if !res.DepsInstalled {
		t.Error("DepsInstalled = false although the safety net ran the install")
	}

	// Second invocation: the hash check holds, and the caller must write nothing.
	res = executeFunction(context.Background(), dir, "needs-deps", "", nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want a clean run", res.Err)
	}
	if res.DepsInstalled {
		t.Error("DepsInstalled = true on the hash-check exit, want false")
	}
}

func TestExecuteFunction_ReservedEnvironment(t *testing.T) {
	readEnv := fakeBunEnvDump(t)
	dir := setupTestFunctionsDir(t, map[string]string{"envdump": ""})

	res := executeFunction(context.Background(), dir, "envdump", "", nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil (stderr: %s)", res.Err, res.Stderr)
	}

	env := readEnv(t)
	want := map[string]string{
		"HOME":          "/tmp",
		"PATH":          "/usr/local/bin:/usr/bin:/bin",
		"NODE_ENV":      "production",
		"FUNCTION_NAME": "envdump",
	}
	for key, value := range want {
		if got, ok := env[key]; !ok || got != value {
			t.Errorf("%s = %q (present=%v), want %q", key, got, ok, value)
		}
	}
}

func TestExecuteFunction_SecretCannotOverrideReservedVariable(t *testing.T) {
	readEnv := fakeBunEnvDump(t)
	dir := setupTestFunctionsDir(t, map[string]string{"hijack": ""})

	// Every reserved name is claimed by a secret. Letting PATH through would be
	// enough to have the function run binaries of its own choosing.
	extraEnv := []string{
		"PATH=/evil/bin",
		"HOME=/evil",
		"NODE_ENV=development",
		"FUNCTION_NAME=impostor",
		"KEPT=yes",
	}

	res := executeFunction(context.Background(), dir, "hijack", "", extraEnv)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil (stderr: %s)", res.Err, res.Stderr)
	}

	env := readEnv(t)
	want := map[string]string{
		"HOME":          "/tmp",
		"PATH":          "/usr/local/bin:/usr/bin:/bin",
		"NODE_ENV":      "production",
		"FUNCTION_NAME": "hijack",
	}
	for key, value := range want {
		if got := env[key]; got != value {
			t.Errorf("%s = %q, want %q: a secret overrode a reserved variable", key, got, value)
		}
	}
	// The filter drops the offending secrets only; the rest of the set stands.
	if got := env["KEPT"]; got != "yes" {
		t.Errorf("KEPT = %q, want %q", got, "yes")
	}
}

func TestExecuteFunction_PassesOrdinarySecrets(t *testing.T) {
	readEnv := fakeBunEnvDump(t)
	dir := setupTestFunctionsDir(t, map[string]string{"secrets": ""})

	// The second value carries '=' signs: the filter splits on the first one only,
	// so the value must reach the subprocess whole.
	extraEnv := []string{
		"API_TOKEN=s3cr3t",
		"API_URL=https://example.test/?a=b&c=d",
	}

	res := executeFunction(context.Background(), dir, "secrets", "", extraEnv)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil (stderr: %s)", res.Err, res.Stderr)
	}

	env := readEnv(t)
	if got := env["API_TOKEN"]; got != "s3cr3t" {
		t.Errorf("API_TOKEN = %q, want %q", got, "s3cr3t")
	}
	if got := env["API_URL"]; got != "https://example.test/?a=b&c=d" {
		t.Errorf("API_URL = %q, want the value kept whole", got)
	}
}

func TestExecuteFunction_ParentEnvironmentDoesNotLeak(t *testing.T) {
	// What the server holds and a function must never see.
	t.Setenv("FAASBOX_ENCRYPTION_KEY", "parent-encryption-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "parent-s3-secret")
	t.Setenv("FAASBOX_LEAK_CANARY", "parent-canary")

	readEnv := fakeBunEnvDump(t)
	dir := setupTestFunctionsDir(t, map[string]string{"nosy": ""})

	res := executeFunction(context.Background(), dir, "nosy", "", nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil (stderr: %s)", res.Err, res.Stderr)
	}

	env := readEnv(t)
	for _, key := range []string{"FAASBOX_ENCRYPTION_KEY", "AWS_SECRET_ACCESS_KEY", "FAASBOX_LEAK_CANARY"} {
		if got, ok := env[key]; ok {
			t.Errorf("%s = %q reached the subprocess: the parent environment is inherited", key, got)
		}
	}
	// PATH is the parent variable a leak would show first: the stub bun itself was
	// found through the test process PATH, so an inherited one would carry the
	// temporary directory it lives in.
	if got := env["PATH"]; got != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q, want the reserved value rather than the parent one", got)
	}
}

func TestExecuteFunction_TimeoutIsReportedAsSuch(t *testing.T) {
	fakeBun(t, "sleep 5")
	dir := setupTestFunctionsDir(t, map[string]string{"slow": ""})

	// The run ends on the earlier of execTimeout and the caller deadline, both
	// carried by the same context. A caller deadline exercises the very same
	// classification without holding the suite for execTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := executeFunction(ctx, dir, "slow", "", nil)
	elapsed := time.Since(start)

	if res.Err == nil {
		t.Fatal("Err = nil, want the expired run reported as a failure")
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want an expired deadline told apart from an application failure (Err = %v)", res.Err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("executeFunction returned after %s: the subprocess outlived its deadline", elapsed)
	}
}

func TestExecuteFunction_ExitCodeIsReported(t *testing.T) {
	fakeBun(t, "echo out\necho boom >&2\nexit 3")
	dir := setupTestFunctionsDir(t, map[string]string{"failing": ""})

	res := executeFunction(context.Background(), dir, "failing", "", nil)

	if res.Err == nil {
		t.Fatal("Err = nil, want a non-zero exit reported as a failure")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.TimedOut {
		t.Error("TimedOut = true, want an application failure told apart from an expired deadline")
	}
	if got := strings.TrimSpace(res.Stdout); got != "out" {
		t.Errorf("Stdout = %q, want it captured despite the failure", got)
	}
	if got := strings.TrimSpace(res.Stderr); got != "boom" {
		t.Errorf("Stderr = %q, want it captured despite the failure", got)
	}
}

func TestExecuteFunction_CancellationKillsTheProcessGroup(t *testing.T) {
	witness := filepath.Join(t.TempDir(), "alive")

	// The stub bun spawns a child that keeps signalling its existence, then waits.
	// Killing the direct child alone would leave that grandchild behind: the
	// process group and its group-wide SIGKILL exist for exactly this. The child
	// writes to the witness file rather than to the captured pipes, so it never
	// holds them open.
	//
	// Both loops are bounded so a failed kill leaves nothing running for the rest
	// of the suite, and the child outlives its parent by a wide margin on purpose:
	// were it to stop on its own before the check, a group kill that never happened
	// would read exactly like one that worked.
	fakeBun(t, "sh -c 'i=0; while [ $i -lt 400 ]; do echo tick >> \""+witness+"\"; sleep 0.05; i=$((i+1)); done' >/dev/null 2>&1 &\n"+
		"sleep 5")
	dir := setupTestFunctionsDir(t, map[string]string{"spawner": ""})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan execResult, 1)
	go func() { done <- executeFunction(ctx, dir, "spawner", "", nil) }()

	waitFileGrows(t, witness, 2*int64(len("tick\n")))
	cancel()

	var res execResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("executeFunction never returned after the context was cancelled")
	}
	if res.Err == nil {
		t.Error("Err = nil, want the cancelled run reported as a failure")
	}
	if res.TimedOut {
		t.Error("TimedOut = true, want a cancellation told apart from an expired deadline")
	}

	// Let a write already in flight land, then check the grandchild went silent.
	time.Sleep(200 * time.Millisecond)
	before := fileSize(t, witness)
	time.Sleep(500 * time.Millisecond)
	if after := fileSize(t, witness); after != before {
		t.Errorf("the witness file grew from %d to %d bytes after the cancellation: a child outlived the process group",
			before, after)
	}
}

func TestExecuteFunction_StdoutTruncationIsFlagged(t *testing.T) {
	original := maxOutputSize
	maxOutputSize = 32
	t.Cleanup(func() { maxOutputSize = original })

	fakeBun(t, "head -c 200 /dev/zero | tr \"\\0\" \"x\"\nexit 0")
	dir := setupTestFunctionsDir(t, map[string]string{"verbose": ""})

	res := executeFunction(context.Background(), dir, "verbose", "", nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil (stderr: %s)", res.Err, res.Stderr)
	}

	if !res.Truncated {
		t.Error("Truncated = false, want the capture cap reported")
	}
	if !res.StdoutTruncated {
		t.Error("StdoutTruncated = false, want the stream carrying the result flagged")
	}
	if got := len(res.Stdout); got != maxOutputSize {
		t.Errorf("len(Stdout) = %d, want the capture capped at %d", got, maxOutputSize)
	}
}

func TestExecuteFunction_StderrTruncationLeavesStdoutIntact(t *testing.T) {
	original := maxOutputSize
	maxOutputSize = 32
	t.Cleanup(func() { maxOutputSize = original })

	fakeBun(t, "echo ok\nhead -c 200 /dev/zero | tr \"\\0\" \"x\" >&2\nexit 0")
	dir := setupTestFunctionsDir(t, map[string]string{"noisy": ""})

	res := executeFunction(context.Background(), dir, "noisy", "", nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil", res.Err)
	}

	// The cap applies per stream: a flooded stderr is signalled, but it does not
	// invalidate a result that came through whole.
	if !res.Truncated {
		t.Error("Truncated = false, want the cap on stderr reported")
	}
	if res.StdoutTruncated {
		t.Error("StdoutTruncated = true, want stdout left intact by a stderr overflow")
	}
	if got := strings.TrimSpace(res.Stdout); got != "ok" {
		t.Errorf("Stdout = %q, want %q", got, "ok")
	}
	if got := len(res.Stderr); got != maxOutputSize {
		t.Errorf("len(Stderr) = %d, want the capture capped at %d", got, maxOutputSize)
	}
}
