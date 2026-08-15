package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// These tests call the operations directly, with no request anywhere. That is
// the whole point of the file: the HTTP suites already pin the statuses, and
// what has to be pinned here is that the verbs decide the same things when
// nobody built a *core.RequestEvent for them.
//
// The scope in particular. On a route carrying a {name} segment, requireAPIKey
// refuses out-of-scope calls before a handler runs, so an HTTP scenario cannot
// tell whether the operation checks anything at all. A caller with no path gets
// no such barrier, which is exactly the situation these tests reproduce.

// unrestricted is the scope a superuser session and an unscoped key both carry.
var unrestricted []string

func TestCreateFunctionOperation(t *testing.T) {
	t.Run("creates the function, its triggers and its secrets", func(t *testing.T) {
		app, _, _ := manageApp(t)

		contract, err := createFunction(app, unrestricted, manageRequest{
			Name:        "greet",
			Script:      "console.log('{}')",
			PackageJson: `{"dependencies":{}}`,
			PlainEnv:    json.RawMessage(`{"TOKEN":"v"}`),
			Crons: []manageCron{
				{Name: "nightly", Schedule: "0 3 * * *"},
			},
		})
		if err != nil {
			t.Fatalf("createFunction() refused: %v", err)
		}

		if contract.Name != "greet" || contract.Id == "" {
			t.Errorf("contract = %+v, want a named function carrying an id", contract)
		}
		if len(contract.Crons) != 1 || contract.Crons[0].Name != "nightly" {
			t.Errorf("contract.Crons = %+v, want the nightly trigger", contract.Crons)
		}
		if got := secretsOf(t, app, contract.Id); got["TOKEN"] != "v" {
			t.Errorf("secrets = %v, want TOKEN stored", got)
		}
		if got := cronsOf(t, app, contract.Id); len(got) != 1 {
			t.Errorf("triggers = %v, want exactly one", got)
		}
	})

	t.Run("refuses a name another function already carries", func(t *testing.T) {
		app, _, _ := manageApp(t)

		_, err := createFunction(app, unrestricted, manageRequest{Name: "echo"})
		if !errors.Is(err, errNameTaken) {
			t.Fatalf("createFunction() error = %v, want errNameTaken", err)
		}
	})

	t.Run("refuses a restricted scope, whatever it names", func(t *testing.T) {
		app, _, fn := manageApp(t)

		// Restricted *on the very function the caller already owns*: there is
		// still nothing to compare a scope against for a function that does not
		// exist yet, so the refusal does not depend on what the scope lists.
		_, err := createFunction(app, []string{fn.Id}, manageRequest{Name: "greet"})
		if !errors.Is(err, errScopeRestricted) {
			t.Fatalf("createFunction() error = %v, want errScopeRestricted", err)
		}
		if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "nameHash", blindIndex("greet")); err == nil {
			t.Error("the refused function was written anyway")
		}
	})

	t.Run("refuses secrets that are not an object of strings", func(t *testing.T) {
		app, _, _ := manageApp(t)

		_, err := createFunction(app, unrestricted, manageRequest{
			Name:     "greet",
			PlainEnv: json.RawMessage(`["A"]`),
		})
		if !errors.Is(err, errBadPlainEnv) {
			t.Fatalf("createFunction() error = %v, want errBadPlainEnv", err)
		}
	})
}

func TestGetFunctionOperation(t *testing.T) {
	t.Run("answers on either spelling of the identity", func(t *testing.T) {
		app, _, fn := manageApp(t)

		for _, segment := range []string{"echo", fn.Id} {
			contract, err := getFunction(app, unrestricted, segment)
			if err != nil {
				t.Fatalf("getFunction(%q) refused: %v", segment, err)
			}
			if contract.Id != fn.Id || contract.Name != "echo" {
				t.Errorf("getFunction(%q) = %+v, want the echo function", segment, contract)
			}
		}
	})

	t.Run("distinguishes a malformed segment from an absent function", func(t *testing.T) {
		app, _, _ := manageApp(t)

		if _, err := getFunction(app, unrestricted, "nom_invalide"); !errors.Is(err, errInvalidFunctionName) {
			t.Errorf("getFunction() error = %v, want errInvalidFunctionName", err)
		}

		var notFound *errNotFound
		if _, err := getFunction(app, unrestricted, "nope"); !errors.As(err, &notFound) {
			t.Errorf("getFunction() error = %v, want errNotFound", err)
		}
	})
}

func TestReplaceFunctionOperation(t *testing.T) {
	t.Run("replaces the source and leaves the secrets alone", func(t *testing.T) {
		app, _, fn := manageApp(t)

		contract, err := replaceFunction(app, unrestricted, "echo", manageRequest{
			Script: "console.log('replaced')",
		})
		if err != nil {
			t.Fatalf("replaceFunction() refused: %v", err)
		}
		if contract.Script != "console.log('replaced')" {
			t.Errorf("contract.Script = %q, want the new source", contract.Script)
		}
		if contract.PackageJson != "" {
			t.Errorf("contract.PackageJson = %q, want the absent field emptied", contract.PackageJson)
		}
		if got := secretsOf(t, app, fn.Id); got["TOKEN"] != "s3cr3t" {
			t.Errorf("secrets = %v, want TOKEN preserved", got)
		}
	})

	t.Run("refuses a name designating another function", func(t *testing.T) {
		app, _, _ := manageApp(t)

		_, err := replaceFunction(app, unrestricted, "echo", manageRequest{Name: "echo-v2"})
		if !errors.Is(err, errNameNotTheTarget) {
			t.Fatalf("replaceFunction() error = %v, want errNameNotTheTarget", err)
		}
		if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "nameHash", blindIndex("echo")); err != nil {
			t.Error("the function was renamed despite the refusal")
		}
	})
}

func TestDeleteFunctionOperation(t *testing.T) {
	app, _, fn := manageApp(t)

	if err := deleteFunction(app, unrestricted, "echo"); err != nil {
		t.Fatalf("deleteFunction() refused: %v", err)
	}
	if _, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id); err == nil {
		t.Error("the function is still there after the delete")
	}

	var notFound *errNotFound
	if err := deleteFunction(app, unrestricted, "echo"); !errors.As(err, &notFound) {
		t.Errorf("deleteFunction() on a gone function = %v, want errNotFound", err)
	}
}

func TestListFunctionsOperation(t *testing.T) {
	app, dir, fn := manageApp(t)
	other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")

	t.Run("lists what the disk can run", func(t *testing.T) {
		functions, err := listFunctions(app, dir, unrestricted)
		if err != nil {
			t.Fatalf("listFunctions() failed: %v", err)
		}
		if len(functions) != 2 {
			t.Fatalf("listFunctions() = %+v, want the two functions", functions)
		}
		for _, f := range functions {
			if f.Invoke != "/invoke/"+f.Name {
				t.Errorf("summary %+v carries the wrong invocation path", f)
			}
		}
	})

	t.Run("filters on the scope it is handed", func(t *testing.T) {
		functions, err := listFunctions(app, dir, []string{fn.Id})
		if err != nil {
			t.Fatalf("listFunctions() failed: %v", err)
		}
		if len(functions) != 1 || functions[0].Id != fn.Id {
			t.Errorf("listFunctions() = %+v, want only the allowed function", functions)
		}
		if functions[0].Id == other.Id {
			t.Error("a function outside the scope was listed")
		}
	})
}

func TestFunctionLogsOperation(t *testing.T) {
	app, _, fn := manageApp(t)
	seedLogs(t, app, fn, 3)

	t.Run("answers with the last runs, newest first", func(t *testing.T) {
		logs, err := functionLogs(app, unrestricted, "echo", 2)
		if err != nil {
			t.Fatalf("functionLogs() refused: %v", err)
		}
		if len(logs) != 2 {
			t.Fatalf("functionLogs() returned %d entries, want 2", len(logs))
		}
		if logs[0].Stdout != `{"n":2}` {
			t.Errorf("first entry stdout = %q, want the most recent run", logs[0].Stdout)
		}
	})

	t.Run("bounds the limit whatever it is handed", func(t *testing.T) {
		// The route caps the ask before it gets here; an operation that trusted
		// its caller for that would be an unbounded read waiting for its second
		// one.
		for _, limit := range []int{0, -1, 10_000} {
			logs, err := functionLogs(app, unrestricted, "echo", limit)
			if err != nil {
				t.Fatalf("functionLogs(limit=%d) refused: %v", limit, err)
			}
			if len(logs) != 3 {
				t.Errorf("functionLogs(limit=%d) returned %d entries, want the three seeded", limit, len(logs))
			}
		}
	})
}

func TestInvokeFunctionOperation(t *testing.T) {
	app, dir, _ := manageApp(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	outcome, err := invokeFunction(ctx, app, dir, unrestricted, "echo", []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("invokeFunction() failed: %v", err)
	}
	if outcome.Function != "echo" {
		t.Errorf("outcome.Function = %q, want %q", outcome.Function, "echo")
	}
	result, ok := outcome.Result.(map[string]any)
	if !ok || result["echo"] != `{"n":1}` {
		t.Errorf("outcome.Result = %v, want the payload echoed back", outcome.Result)
	}
	if outcome.Duration <= 0 {
		t.Errorf("outcome.Duration = %v, want the time the run took", outcome.Duration)
	}

	// The run is recorded, exactly as it is on the scheduled path: an invocation
	// that left no trace is the blind spot this shares with runFunction.
	if got := countExecutionLogs(t, app, "echo"); got != 1 {
		t.Errorf("execution logs = %d, want 1", got)
	}

	var notFound *errNotFound
	if _, err := invokeFunction(ctx, app, dir, unrestricted, "nope", nil); !errors.As(err, &notFound) {
		t.Errorf("invokeFunction() on an unknown function = %v, want errNotFound", err)
	}
}

// TestOperationsRefuseAFunctionOutOfScope is the reason the scope is a parameter
// at all. Each of the five per-function verbs is called with a scope naming
// another function, the way an MCP tool would reach them: no path, so no
// middleware, so the operation is the only thing standing between a restricted
// key and the whole instance.
func TestOperationsRefuseAFunctionOutOfScope(t *testing.T) {
	app, dir, _ := manageApp(t)
	other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")
	elsewhere := []string{other.Id}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cases := []struct {
		name string
		call func() error
	}{
		{"getFunction", func() error {
			_, err := getFunction(app, elsewhere, "echo")
			return err
		}},
		{"replaceFunction", func() error {
			_, err := replaceFunction(app, elsewhere, "echo", manageRequest{Script: "console.log('nope')"})
			return err
		}},
		{"deleteFunction", func() error {
			return deleteFunction(app, elsewhere, "echo")
		}},
		{"functionLogs", func() error {
			_, err := functionLogs(app, elsewhere, "echo", 10)
			return err
		}},
		{"invokeFunction", func() error {
			_, err := invokeFunction(ctx, app, dir, elsewhere, "echo", nil)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, errScopeRestricted) {
				t.Fatalf("%s() error = %v, want errScopeRestricted", tc.name, err)
			}
			if !strings.Contains(err.Error(), `"echo"`) {
				t.Errorf("%s() says %q, want the refused function named", tc.name, err)
			}
		})
	}

	// Nothing was written, run or read along the way.
	if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "nameHash", blindIndex("echo")); err != nil {
		t.Error("the function was deleted despite the refusals")
	}
	if got := countExecutionLogs(t, app, "echo"); got != 0 {
		t.Errorf("execution logs = %d, want none: the refused invocation must not have run", got)
	}
}

// TestScopedFunctionHidesWhatIsOutOfScope pins the half of the inventory a scope
// withholds: a restricted caller naming a function that does not exist is
// refused rather than told it does not exist, which is the rule requireAPIKey
// applies on the routes that have a segment.
func TestScopedFunctionHidesWhatIsOutOfScope(t *testing.T) {
	app, dir, _ := manageApp(t)
	other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")

	_, err := scopedFunction(app, []string{other.Id}, "nope")
	if !errors.Is(err, errScopeRestricted) {
		t.Fatalf("scopedFunction() error = %v, want errScopeRestricted", err)
	}

	// An unrestricted caller is told plainly, because it had the right to know.
	var notFound *errNotFound
	if _, err := scopedFunction(app, unrestricted, "nope"); !errors.As(err, &notFound) {
		t.Errorf("scopedFunction() error = %v, want errNotFound", err)
	}
}

// TestOperationsNeverSeeARequest is the structural half of the extraction: the
// operations must stay reachable without HTTP, and a *core.RequestEvent slipping
// back into one of their signatures would be the regression. Asserting it on the
// types is cheap and catches it at compile time.
func TestOperationsNeverSeeARequest(t *testing.T) {
	var (
		_ func(core.App, []string, manageRequest) (functionContract, error)         = createFunction
		_ func(core.App, []string, string) (functionContract, error)                = getFunction
		_ func(core.App, []string, string, manageRequest) (functionContract, error) = replaceFunction
		_ func(core.App, []string, string) error                                    = deleteFunction
		_ func(core.App, string, []string) ([]functionSummary, error)               = listFunctions
		_ func(core.App, []string, string, int) ([]logContract, error)              = functionLogs

		_ func(context.Context, core.App, string, []string, string, []byte) (invokeOutcome, error) = invokeFunction
	)
}
