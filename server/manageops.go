package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// This file carries the management verbs as operations: what creating, reading,
// replacing, deleting, listing a function and reading its history *are*, once
// the request that asked for it is out of the picture. manage.go, invoke.go and
// logsread.go keep the transport — read a path segment, decode a body, turn what
// comes back into a status and some JSON — and nothing else.
//
// The cut exists because a second caller is arriving. The verbs only existed as
// HTTP handlers, each taking a *core.RequestEvent and writing its own response,
// so a caller that is not a request had three ways in and all three were bad:
// copy the body of a handler, which diverges at the first fix; write to the
// database directly, which walks past the hooks that validate the name, encrypt
// the secrets and reconcile the triggers; or fake a request event and read its
// JSON back, which routes every call through an imaginary HTTP exchange. This is
// the extraction executeFunction went through when the cron path became the
// second caller of the engine.
//
// **The scope is a parameter, and that is the point.** requireAPIKey applies a
// key's scope only when the route carries a {name} segment (apikeys.go, step 6);
// a caller that has no path — naming its target in an argument instead — would
// inherit no check at all, and a key restricted to one function could reach
// every function of the instance. Each operation therefore checks the scope it
// is handed rather than assume someone upstream did. The middleware keeps its
// own check: it is the first barrier on the routes that carry a segment, and
// removing it would be a weakening, not a de-duplication.
//
// Past the 300-line guideline, deliberately. The domain was cut once already —
// invoking is in invokeops.go, because running user code is not managing a
// record — and cutting again would split the six verbs from the scope check and
// the transaction they all go through, which is the one thing a reader has to
// find. What is left is one responsibility carried at the file's usual comment
// density.

// errNameTaken marks a creation refused because another function already carries
// the name. It is the caller's to fix, and every transport answers 409.
var errNameTaken = errors.New("A function already carries this name")

// errBadPlainEnv marks secrets the server cannot make sense of. It names the
// field the caller sent, never the column they are stored in.
var errBadPlainEnv = errors.New(`"plainEnv" must be an object whose values are strings`)

// errNameNotTheTarget marks a body whose "name" designates something other than
// the function being replaced. A replacement never renames: a silent rename
// would break every URL wired on the old name, with nothing in the exchange
// saying so.
var errNameNotTheTarget = errors.New(`"name" does not designate the function being replaced; a replacement never renames`)

// errTriggersUnreadable marks a contract that could not be rendered because the
// triggers of the function could not be read. It is a fault of ours: the write,
// if there was one, did happen.
var errTriggersUnreadable = errors.New("Failed to read the triggers of this function")

// functionSummary is one entry of the invocable inventory. A type rather than
// the map the listing used to build: a caller that is not a JSON response needs
// fields it can name.
type functionSummary struct {
	Id     string `json:"id"`
	Name   string `json:"name"`
	Invoke string `json:"invoke"`
}

// createFunction writes a function, its triggers and its secrets, and answers
// with the contract.
//
// Its scope rule is not the one the five other verbs apply, and cannot be: there
// is nothing to compare a scope against for a function that does not exist yet.
// A key naming precise functions therefore changes and deletes those, and
// creates none — the alternative would let it grant itself a function it was
// never given.
func createFunction(app core.App, allowed []string, req manageRequest) (functionContract, error) {
	if !scopeIsUnrestricted(allowed) {
		return functionContract{}, &refusedScope{
			message: "API key with a restricted scope cannot create a function",
		}
	}

	if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", req.Name); err == nil {
		return functionContract{}, errNameTaken
	}

	// The name is not checked here. validateFunctionNameHook refuses it at the
	// write, for this path as for the editor, and its ApiError carries a message
	// naming the rule — which a second check here could only repeat and let
	// drift.
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		return functionContract{}, fmt.Errorf("collection %s not found: %w", faasboxFunctionsCollection, err)
	}

	record := core.NewRecord(col)
	record.Set("name", req.Name)
	record.Set("script", req.Script)
	record.Set("packageJson", req.PackageJson)
	if err := applyPlainEnv(record, req.PlainEnv); err != nil {
		return functionContract{}, err
	}

	return saveFunction(app, record, req.Crons)
}

// getFunction answers with the contract of the function a caller designates, by
// id or by name.
func getFunction(app core.App, allowed []string, idOrName string) (functionContract, error) {
	record, err := scopedFunction(app, allowed, idOrName)
	if err != nil {
		return functionContract{}, err
	}
	return functionContractOf(app, record)
}

// replaceFunction replaces the function a caller designates. It replaces, it
// never creates: a replacement aimed at nothing is an absence, not a creation.
// Creation has its own verb, and its own scope rule.
func replaceFunction(app core.App, allowed []string, idOrName string, req manageRequest) (functionContract, error) {
	record, err := scopedFunction(app, allowed, idOrName)
	if err != nil {
		return functionContract{}, err
	}

	// The target identifies. A name in the body may repeat that identity —
	// either spelling of it, so a contract read back can be edited and sent
	// straight in — but it never changes it.
	if req.Name != "" && req.Name != record.GetString("name") && req.Name != record.Id {
		return functionContract{}, errNameNotTheTarget
	}

	record.Set("script", req.Script)
	record.Set("packageJson", req.PackageJson)
	if err := applyPlainEnv(record, req.PlainEnv); err != nil {
		return functionContract{}, err
	}

	return saveFunction(app, record, req.Crons)
}

// deleteFunction removes the function a caller designates.
//
// Nothing is cleaned up here on purpose: the cron and log relations carry
// CascadeDelete, so the triggers and the history go with the record, and the
// AfterDeleteSuccess hook removes the directory from disk.
func deleteFunction(app core.App, allowed []string, idOrName string) error {
	record, err := scopedFunction(app, allowed, idOrName)
	if err != nil {
		return err
	}

	if err := app.Delete(record); err != nil {
		return fmt.Errorf("failed to delete function %s: %w", record.Id, err)
	}
	return nil
}

// listFunctions answers with the functions the scope allows, and nothing more.
// Enforcing the scope here is what keeps a restricted caller from learning the
// full inventory of the instance.
//
// The inventory comes from the database. The directory can no longer supply it:
// its entries are named by id, and a listing has to answer with the name. The
// disk still has the last word on whether a function is invocable — an index.ts
// under the id directory — which is what the listing has always meant.
func listFunctions(app core.App, functionsDir string, allowed []string) ([]functionSummary, error) {
	records, err := app.FindAllRecords(faasboxFunctionsCollection)
	if err != nil {
		return nil, err
	}

	functions := make([]functionSummary, 0, len(records))
	for _, record := range records {
		if !scopeAllows(allowed, record.Id) {
			continue
		}
		indexPath := filepath.Join(functionsDir, record.Id, "index.ts")
		if _, err := os.Stat(indexPath); err != nil {
			continue
		}
		name := record.GetString("name")
		functions = append(functions, functionSummary{
			Id:     record.Id,
			Name:   name,
			Invoke: fmt.Sprintf("/invoke/%s", name),
		})
	}
	return functions, nil
}

// functionLogs answers with the last runs of the function a caller designates.
//
// The limit is bounded here and not only where a query string is parsed: a route
// hands back as many records as it is asked for only because logsLimit already
// capped the ask, and an operation that trusts its caller for that is an
// unbounded path waiting for its second caller. A limit below 1 is read as
// "unspecified" rather than refused — the refusal belongs to whoever received
// something unreadable, which is a transport's business.
func functionLogs(app core.App, allowed []string, idOrName string, limit int) ([]logContract, error) {
	record, err := scopedFunction(app, allowed, idOrName)
	if err != nil {
		return nil, err
	}

	if limit < 1 {
		limit = defaultLogsLimit
	}
	if limit > maxLogsLimit {
		limit = maxLogsLimit
	}

	// The filter is on the *relation*, never on functionName: that is what keeps
	// the history attached to the function across a rename. Descending on
	// created — whoever is debugging wants the last run, not the first.
	records, err := app.FindRecordsByFilter(
		faasboxLogsCollection,
		"function = {:id}",
		"-created", limit, 0,
		dbx.Params{"id": record.Id},
	)
	if err != nil {
		return nil, err
	}

	logs := make([]logContract, 0, len(records))
	for _, entry := range records {
		logs = append(logs, logContractOf(app, entry))
	}
	return logs, nil
}

// scopedFunction resolves what a caller designates and weighs the scope it was
// handed against it. Every per-function operation goes through it, which is what
// makes the scope check independent of who is calling.
//
// A segment that resolves to nothing is weighed as it stands, exactly as
// requireAPIKey does: a restricted caller is refused before it learns whether
// the function exists, which is the half of the inventory the scope withholds.
func scopedFunction(app core.App, allowed []string, idOrName string) (*core.Record, error) {
	if !validName.MatchString(idOrName) || len(idOrName) > 64 {
		return nil, errInvalidFunctionName
	}

	record, err := resolveFunction(app, idOrName)
	if err != nil {
		if !scopeAllows(allowed, idOrName) {
			return nil, scopeRefusalFor(idOrName)
		}
		return nil, err
	}
	if !scopeAllows(allowed, record.Id) {
		return nil, scopeRefusalFor(idOrName)
	}
	return record, nil
}

// saveFunction persists the function and reconciles its triggers, then answers
// with the contract. Create and replace differ only by the record handed over.
//
// The record and its triggers go in **one** transaction. Writing the record
// first and reconciling afterwards made a refused trigger report a failure with
// the function already written: a creation left behind a function that turned
// the corrected retry into a conflict, and a replacement carrying an empty
// plainEnv destroyed the secrets while reporting a failure. What the caller is
// told did not happen must not have happened.
func saveFunction(app core.App, record *core.Record, crons []manageCron) (functionContract, error) {
	err := app.RunInTransaction(func(txApp core.App) error {
		if err := txApp.Save(record); err != nil {
			return err
		}
		// A nil list is an absent key: the triggers are left alone. An empty one
		// is an instruction, and removes them all.
		if crons != nil {
			return replaceCronJobs(txApp, record, crons)
		}
		return nil
	})
	if err != nil {
		return functionContract{}, err
	}

	// Re-read, and *after the commit*. Two reasons in one: the in-memory row
	// never sees depsStatus, which the install publishes by direct SQL, and
	// PocketBase defers the AfterSuccess hooks to the end of the transaction, so
	// the install is only scheduled once we are out of it. The contract says what
	// the record says at that instant and does not wait for the install — the
	// caller reads it again later, or simply invokes and lets the safety net
	// absorb it.
	if fresh, err := app.FindRecordById(faasboxFunctionsCollection, record.Id); err == nil {
		record = fresh
	}

	return functionContractOf(app, record)
}

// functionContractOf renders the public shape of a function record.
func functionContractOf(app core.App, record *core.Record) (functionContract, error) {
	crons, err := functionCronContracts(app, record.Id)
	if err != nil {
		app.Logger().Error("faasbox: failed to read the triggers of a function",
			"functionId", record.Id, "error", err)
		return functionContract{}, errTriggersUnreadable
	}

	return functionContract{
		Id:          record.Id,
		Name:        record.GetString("name"),
		Script:      functionScript(app, record),
		PackageJson: functionPackageJson(app, record),
		DepsStatus:  record.GetString("depsStatus"),
		DepsError:   functionDepsError(app, record),
		Crons:       crons,
	}, nil
}

// applyPlainEnv relays the secrets the caller sent, and only those.
//
// The three cases belong to encryptPlainEnvHook and are reproduced nowhere:
// absent or null preserves what is stored, `{}` erases every variable, a
// non-empty object replaces the lot. Writing the field on the operation's own
// initiative would turn an ordinary save into one of those three answers by
// accident — which is why nothing is set when the body says nothing.
func applyPlainEnv(record *core.Record, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	// Decoded into a map of strings rather than relayed verbatim: the hook
	// encrypts whatever it is handed, and a list or a nested object would only
	// come back as an undecryptable environment much later.
	var env map[string]string
	if err := json.Unmarshal(raw, &env); err != nil {
		return errBadPlainEnv
	}

	record.Set("plainEnv", env)
	return nil
}
