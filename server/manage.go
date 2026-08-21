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

// This file carries the management contract over HTTP: create, read, replace and
// delete a function with an API key rather than a superuser token.
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
// What is left in this file is transport, and only transport: read the path
// segment, decode the body, read the scope off the request context, call the
// operation, turn what comes back into a status and some JSON. The deciding is
// in manageops.go, where a caller that is not a request can reach it.
//
// One exception, and it is deliberate: classifyManageFailure below knows no
// transport. It answers "may this failure be published, and as what" — a
// question the MCP tools ask too, and the one thing the two transports must not
// answer separately.
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
	allowed, err := requestKeyScope(e)
	if err != nil {
		e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying creation", "error", err)
		return answerFunctionLookup(e, err)
	}

	var body manageRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
	}

	contract, err := createFunction(e.App, allowed, body)
	if err != nil {
		return answerManageFailure(e, err, "Failed to save the function")
	}
	return e.JSON(http.StatusCreated, contract)
}

// getFunctionHandler answers GET /api/faasbox/functions/{idOrName}.
func getFunctionHandler(e *core.RequestEvent) error {
	allowed, err := requestKeyScope(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	contract, err := getFunction(e.App, allowed, e.Request.PathValue("name"))
	if err != nil {
		return answerFunctionLookup(e, err)
	}
	return e.JSON(http.StatusOK, contract)
}

// replaceFunctionHandler answers PUT /api/faasbox/functions/{idOrName}.
func replaceFunctionHandler(e *core.RequestEvent) error {
	allowed, err := requestKeyScope(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	var body manageRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
	}

	contract, err := replaceFunction(e.App, allowed, e.Request.PathValue("name"), body)
	if err != nil {
		return answerManageFailure(e, err, "Failed to save the function")
	}
	return e.JSON(http.StatusOK, contract)
}

// deleteFunctionHandler answers DELETE /api/faasbox/functions/{idOrName}.
func deleteFunctionHandler(e *core.RequestEvent) error {
	allowed, err := requestKeyScope(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	if err := deleteFunction(e.App, allowed, e.Request.PathValue("name")); err != nil {
		return answerManageFailure(e, err, "Failed to delete the function")
	}
	return e.NoContent(http.StatusNoContent)
}

// manageRefusal is what a refused management operation publishes, once the
// transport is out of the picture: the status an HTTP caller is answered with,
// the wording written for whoever asked, and the fields a refused record names.
type manageRefusal struct {
	status  int
	message string
	fields  validation.Errors
}

// classifyManageFailure says what a failed operation may publish, and returns
// nil when it may publish nothing.
//
// **It is the only enumeration of the refusals**, and that is why it exists
// apart from the handler it used to sit inside. There are two transports now —
// the routes of this file and the MCP tools of mcptools.go — and they render
// differently: one picks a status and a JSON body, the other hands the error
// back to an agent that reads it and corrects itself. What they must not decide
// separately is *which* failures are the caller's business. A second list would
// drift, and it would drift towards relaying a database error to whoever asked.
//
// A nil result is the fault-of-ours class — the fall-through below. Nothing of
// it is publishable: the cause belongs in the server log and the caller gets a
// generic label. errTriggersUnreadable is not part of that class despite its
// 500; its wording is written for the caller, and its cause is already logged
// where it happens (manageops.go).
//
// The two record-level cases at the end were once a function of their own,
// shared by the record and its triggers. They stay together for the same reason
// they were merged: two unwrappings had drifted, and a trigger refused on a
// required field answered 500 where the same refusal on the function answered
// 400.
func classifyManageFailure(err error) *manageRefusal {
	switch {
	case errors.Is(err, errInvalidFunctionName):
		return &manageRefusal{status: http.StatusBadRequest, message: "Invalid function name"}
	case errors.Is(err, errBadPlainEnv), errors.Is(err, errNameNotTheTarget):
		return &manageRefusal{status: http.StatusBadRequest, message: err.Error()}
	case errors.Is(err, errScopeRestricted):
		return &manageRefusal{status: http.StatusForbidden, message: err.Error()}
	case errors.Is(err, errScopeUnreadable):
		return &manageRefusal{status: http.StatusForbidden, message: errScopeUnreadable.Error()}
	case errors.Is(err, errNameTaken):
		return &manageRefusal{status: http.StatusConflict, message: errNameTaken.Error()}
	case errors.Is(err, errTriggersUnreadable):
		return &manageRefusal{status: http.StatusInternalServerError, message: errTriggersUnreadable.Error()}
	}

	var notFound *errNotFound
	if errors.As(err, &notFound) {
		return &manageRefusal{status: http.StatusNotFound, message: "Function not found"}
	}

	// An ApiError carries its own status and wording — that is how the trigger
	// hook reports what it refused, and its message says why.
	var apiErr *router.ApiError
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
		message := apiErr.Message
		// Named the same way the field-validation branch below names it. The
		// hook now refuses cases with nothing distinctive in their wording — a
		// blank schedule reads the same whichever trigger carried it — and a
		// body may well hold several.
		var trigger *refusedTrigger
		if errors.As(err, &trigger) {
			message = trigger.subject() + " was refused: " + message
		}
		return &manageRefusal{status: apiErr.Status, message: message}
	}

	// Field validation is the caller's data. 400 naming the fields, never 500,
	// which would blame the server for a body the caller can fix and hide which
	// field to fix.
	var refused validation.Errors
	if errors.As(err, &refused) {
		// A function and a trigger share field names — both carry a "name" — so
		// the answer has to say which one it is about, and which trigger.
		subject := "The function was refused"
		var trigger *refusedTrigger
		if errors.As(err, &trigger) {
			subject = trigger.subject() + " was refused"
		}
		return &manageRefusal{
			status:  http.StatusBadRequest,
			message: subject,
			fields:  contractFields(refused),
		}
	}

	return nil
}

// answerManageFailure is where an operation's refusal becomes a response, for
// every management route. One rendering rather than one per handler: the
// refusals are the operations' vocabulary, and a route that invented its own
// status for one of them would be a divergence waiting to happen.
//
// fallback is the wording the route uses for a fault of ours — the only case
// classifyManageFailure does not cover, and the one thing each route names
// differently.
func answerManageFailure(e *core.RequestEvent, err error, fallback string) error {
	refusal := classifyManageFailure(err)
	if refusal == nil {
		e.App.Logger().Error("faasbox: a management operation failed",
			"answer", fallback, "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": fallback})
	}

	if refusal.fields != nil {
		return e.JSON(refusal.status, map[string]any{
			"error":  refusal.message,
			"fields": refusal.fields,
		})
	}
	return e.JSON(refusal.status, map[string]string{"error": refusal.message})
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
