package main

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// This file carries the management contract: create, read, replace and delete a
// function over HTTP, with an API key rather than a superuser token.
//
// It exists because the only way to write a function used to be the PocketBase
// collections API, which needs full powers over the instance — dropping
// collections, reading every secret in clear, changing the password. It also
// exposed the internal schema (the encrypted env, the lockfile, the install
// state) as if it were a contract, so any schema change broke the caller.
//
// What is published here is therefore deliberately narrower than the record:
// never `env`, never `bunLock`. Those are mechanics, and a caller that never saw
// them cannot be broken by them moving.
//
// The request body is bounded by the router's own BodyLimit (32 MB by default),
// which every route inherits — this file adds no unbounded read.

// manageRequest is the body POST and PUT accept.
//
// Three fields distinguish *absent* from *empty*, and that distinction is the
// whole contract:
//   - PlainEnv nil means untouched, `{}` means erase every secret;
//   - Crons nil means the triggers stay, `[]` means remove them all.
//
// Script and PackageJson do not: PUT replaces the function, so what the body
// does not carry is what the function no longer has.
type manageRequest struct {
	Name        string          `json:"name"`
	Script      string          `json:"script"`
	PackageJson string          `json:"packageJson"`
	PlainEnv    json.RawMessage `json:"plainEnv"`
	Crons       []manageCron    `json:"crons"`
}

// functionContract is what the three reading routes answer with. The id is here
// because the id is the identity: it is what survives a rename, what a key scope
// lists, and what an integration should be wired on.
type functionContract struct {
	Id          string         `json:"id"`
	Name        string         `json:"name"`
	Script      string         `json:"script"`
	PackageJson string         `json:"packageJson"`
	DepsStatus  string         `json:"depsStatus"`
	DepsError   string         `json:"depsError"`
	Crons       []cronContract `json:"crons"`
}

// createFunctionHandler answers POST /api/faasbox/functions.
func createFunctionHandler(e *core.RequestEvent) error {
	// The scope cannot be checked by the middleware here: this route carries no
	// function in its path, and there is nothing to compare a scope against for a
	// function that does not exist yet. A key naming precise functions therefore
	// changes and deletes those, and creates none — the alternative would let it
	// grant itself a function it was never given.
	allowed, err := requestKeyScope(e)
	if err != nil {
		e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying creation", "error", err)
		return e.JSON(http.StatusForbidden, map[string]string{"error": "API key scope cannot be read"})
	}
	if !scopeIsUnrestricted(allowed) {
		return e.JSON(http.StatusForbidden, map[string]string{
			"error": "API key with a restricted scope cannot create a function",
		})
	}

	var body manageRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
	}
	if !validName.MatchString(body.Name) || len(body.Name) > 64 {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid function name"})
	}
	if _, err := e.App.FindFirstRecordByData(faasboxFunctionsCollection, "name", body.Name); err == nil {
		return e.JSON(http.StatusConflict, map[string]string{"error": "A function already carries this name"})
	}

	col, err := e.App.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		e.App.Logger().Error("faasbox: functions collection not found", "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save the function"})
	}

	record := core.NewRecord(col)
	record.Set("name", body.Name)
	record.Set("script", body.Script)
	record.Set("packageJson", body.PackageJson)
	if err := applyPlainEnv(record, body.PlainEnv); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	return saveAndAnswer(e, record, http.StatusCreated, body.Crons)
}

// getFunctionHandler answers GET /api/faasbox/functions/{idOrName}.
func getFunctionHandler(e *core.RequestEvent) error {
	record, err := functionFromPath(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}
	return answerFunctionContract(e, http.StatusOK, record)
}

// replaceFunctionHandler answers PUT /api/faasbox/functions/{idOrName}.
//
// It replaces, it never creates: a PUT on a segment designating nothing is a
// 404. Creation has its own route, and its own scope rule.
func replaceFunctionHandler(e *core.RequestEvent) error {
	record, err := functionFromPath(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	var body manageRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
	}

	// The path identifies. A name in the body may repeat that identity — either
	// spelling of it, so a GET response can be edited and sent straight back —
	// but it never changes it: a silent rename would break every URL wired on the
	// old one, with nothing in the exchange saying so.
	if body.Name != "" && body.Name != record.GetString("name") && body.Name != record.Id {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": `"name" does not designate the function in the path; this route never renames`,
		})
	}

	record.Set("script", body.Script)
	record.Set("packageJson", body.PackageJson)
	if err := applyPlainEnv(record, body.PlainEnv); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	return saveAndAnswer(e, record, http.StatusOK, body.Crons)
}

// deleteFunctionHandler answers DELETE /api/faasbox/functions/{idOrName}.
//
// Nothing is cleaned up here on purpose: the cron and log relations carry
// CascadeDelete, so the triggers and the history go with the record, and the
// AfterDeleteSuccess hook removes the directory from disk.
func deleteFunctionHandler(e *core.RequestEvent) error {
	record, err := functionFromPath(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	if err := e.App.Delete(record); err != nil {
		e.App.Logger().Error("faasbox: failed to delete a function",
			"functionId", record.Id, "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to delete the function",
		})
	}

	return e.NoContent(http.StatusNoContent)
}

// saveAndAnswer persists the function, reconciles its triggers when the body
// carried any, and answers with the contract. Create and replace differ only by
// the record they hand over and the status they answer with.
//
// The record and its triggers go in **one** transaction. Writing the record
// first and reconciling afterwards made a refused trigger answer 400 with the
// function already written: a POST left behind a function that turned the
// corrected retry into a 409, and a PUT carrying `"plainEnv": {}` destroyed the
// secrets while reporting a failure. What the caller is told did not happen must
// not have happened.
func saveAndAnswer(e *core.RequestEvent, record *core.Record, status int, crons []manageCron) error {
	err := e.App.RunInTransaction(func(txApp core.App) error {
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
		return answerSaveFailure(e, record, err)
	}

	// Re-read, and *after the commit*. Two reasons in one: the in-memory row
	// never sees depsStatus, which the install publishes by direct SQL, and
	// PocketBase defers the AfterSuccess hooks to the end of the transaction, so
	// the install is only scheduled once we are out of it. The response says what
	// the record says at that instant and does not wait for the install — the
	// caller polls with GET, or simply invokes and lets the safety net absorb it.
	if fresh, err := e.App.FindRecordById(faasboxFunctionsCollection, record.Id); err == nil {
		record = fresh
	}

	return answerFunctionContract(e, status, record)
}

// answerSaveFailure turns a refused write into its response, for the function
// record and its triggers alike. One mapping rather than one per caller: the two
// had drifted apart, and a trigger refused on a required field answered 500
// where the same refusal on the function answered 400.
//
// Three cases, and only the last is ours:
//
//   - an ApiError carries its own status and wording — that is how the cron hook
//     reports an invalid expression, and its message names the expression;
//   - field validation is the caller's data. 400 naming the fields, never 500,
//     which would blame the server for a body the caller can fix and hide which
//     field to fix;
//   - anything else is a fault of ours, logged and answered 500.
func answerSaveFailure(e *core.RequestEvent, record *core.Record, err error) error {
	var apiErr *router.ApiError
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
		return e.JSON(apiErr.Status, map[string]string{"error": apiErr.Message})
	}

	var refused validation.Errors
	if errors.As(err, &refused) {
		// A function and a trigger share field names — both carry a "name" — so
		// the answer has to say which one it is about, and which trigger.
		subject := "The function was refused"
		var trigger *refusedTrigger
		if errors.As(err, &trigger) {
			subject = trigger.subject() + " was refused"
		}
		return e.JSON(http.StatusBadRequest, map[string]any{
			"error":  subject,
			"fields": contractFields(refused),
		})
	}

	e.App.Logger().Error("faasbox: failed to save a function",
		"function", record.GetString("name"), "error", err)
	return e.JSON(http.StatusInternalServerError, map[string]string{
		"error": "Failed to save the function",
	})
}

// contractFields renames the refused columns the caller never wrote.
//
// The secrets arrive as `plainEnv` and are stored, encrypted, in `env`. A
// refusal on the stored column would answer about a field the contract never
// publishes and the caller cannot set — and `env` is precisely what this
// contract hides. It speaks of what was sent instead.
func contractFields(refused validation.Errors) validation.Errors {
	message, ok := refused["env"]
	if !ok {
		return refused
	}

	renamed := maps.Clone(refused)
	delete(renamed, "env")
	renamed["plainEnv"] = message
	return renamed
}

// applyPlainEnv relays the secrets the caller sent, and only those.
//
// The three cases belong to encryptPlainEnvHook and are reproduced nowhere:
// absent or null preserves what is stored, `{}` erases every variable, a
// non-empty object replaces the lot. Writing the field on the handler's own
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
		return errors.New(`"plainEnv" must be an object whose values are strings`)
	}

	record.Set("plainEnv", env)
	return nil
}

// answerFunctionContract writes the public shape of a function record.
func answerFunctionContract(e *core.RequestEvent, status int, record *core.Record) error {
	crons, err := functionCronContracts(e.App, record.Id)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to read the triggers of a function",
			"functionId", record.Id, "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to read the triggers of this function",
		})
	}

	return e.JSON(status, functionContract{
		Id:          record.Id,
		Name:        record.GetString("name"),
		Script:      record.GetString("script"),
		PackageJson: record.GetString("packageJson"),
		DepsStatus:  record.GetString("depsStatus"),
		DepsError:   record.GetString("depsError"),
		Crons:       crons,
	})
}
