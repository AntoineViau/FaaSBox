package main

import (
	"errors"
	"net/http"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// This file carries the one rule that says what designates a function.
//
// The id is the identity: it names the directory on disk, it is what a key's
// scope lists, and it is what the cron and log relations point at. The name is a
// label — free to change, and every reference built on it broke silently when it
// did. What survives of the name is the URL: an integration wired on
// /invoke/echo must keep working, so a segment is accepted as either.

// errInvalidFunctionName marks a path segment that never had a chance of
// designating anything: it fails validName or the length cap. It is a refusal,
// not an absence, and callers answer 400 rather than 404.
var errInvalidFunctionName = errors.New("invalid function name")

// resolveFunction returns the function a URL segment designates.
//
// A single query asks for both spellings and the preference is applied on the
// result: a record whose *id* is the segment wins over a record whose *name* is.
// The rule matters because validName allows [a-zA-Z0-9-], so a name can look
// exactly like an id — nothing about the shape of a segment settles which one it
// is, and deciding from that shape would make the answer depend on a regex
// instead of on what the database holds.
//
// The consequence is stated rather than hidden: a function named after another
// one's id becomes unreachable by name. Refusing 15-character [a-z0-9] names at
// creation would be the stricter cure, and is deliberately not done here.
//
// The segment is expected to have been validated by the caller (validName, 64
// characters), exactly as before — resolution replaces the lookup, not the guard.
func resolveFunction(app core.App, idOrName string) (*core.Record, error) {
	if idOrName == "" {
		return nil, &errNotFound{Name: idOrName}
	}

	// Two rows at most: id is the primary key and name carries a unique index.
	records, err := app.FindRecordsByFilter(
		faasboxFunctionsCollection,
		"id = {:v} || name = {:v}",
		"", 2, 0,
		dbx.Params{"v": idOrName},
	)
	if err != nil {
		return nil, err
	}

	for _, record := range records {
		if record.Id == idOrName {
			return record, nil
		}
	}
	if len(records) > 0 {
		return records[0], nil
	}
	return nil, &errNotFound{Name: idOrName}
}

// functionFromPath resolves the {name} path value of a request into a record.
// The routes it serves all take a segment that may be an id or a name, and they
// all had this same preamble before there was anything to resolve.
func functionFromPath(e *core.RequestEvent) (*core.Record, error) {
	segment := e.Request.PathValue("name")
	if !validName.MatchString(segment) || len(segment) > 64 {
		return nil, errInvalidFunctionName
	}
	return resolveFunction(e.App, segment)
}

// answerFunctionLookup turns a functionFromPath failure into its response. The
// three refusals are distinct on purpose: a malformed segment is a rule (400), a
// segment designating nothing is an absence (404), and anything else is ours to
// own (500) rather than to disguise as either.
func answerFunctionLookup(e *core.RequestEvent, err error) error {
	if errors.Is(err, errInvalidFunctionName) {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid function name"})
	}
	var notFound *errNotFound
	if errors.As(err, &notFound) {
		return e.JSON(http.StatusNotFound, map[string]string{"error": "Function not found"})
	}
	e.App.Logger().Error("faasbox: failed to resolve the function", "error", err)
	return e.JSON(http.StatusInternalServerError, map[string]string{
		"error": "Failed to resolve the function",
	})
}
