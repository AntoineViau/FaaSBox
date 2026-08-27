package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/core"
)

const (
	defaultMaxConcurrency = 4
	defaultMaxBodySize    = 1 << 20 // 1 MB
)

// maxConcurrency is the maximum number of concurrent Bun processes.
// Configurable via FAASBOX_MAX_CONCURRENCY environment variable.
var maxConcurrency = envInt("FAASBOX_MAX_CONCURRENCY", defaultMaxConcurrency)

// maxBodySize is the number of bytes accepted in a request body.
// Configurable via FAASBOX_MAX_BODY_SIZE environment variable.
var maxBodySize = envInt("FAASBOX_MAX_BODY_SIZE", defaultMaxBodySize)

// sem limits the number of concurrent Bun processes. Its two acquirers are
// invokeFunction, which refuses when it is full, and runFunction, which waits.
var sem = make(chan struct{}, maxConcurrency)

// validName whitelists function names: alphanumeric + hyphens, no path traversal.
var validName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?$`)

// invokeHandler answers POST /invoke/{idOrName}. It reads the body, hands the
// invocation over, and turns what comes back into a response — the deciding is
// in invokeops.go, where a caller that is not a request can reach it.
func invokeHandler(e *core.RequestEvent, functionsDir string) error {
	// Read the request body, which becomes the "body" field of the envelope
	// handed to the function. One extra byte beyond the limit, to detect
	// oversized payloads: without it, LimitReader silently truncates the body
	// and the function receives half a document, producing a confusing parsing
	// error.
	//
	// The bound is on **what the caller sent**, and stays there: the envelope
	// built around it is ours, and the headers it carries are already bounded by
	// the HTTP server itself.
	body, err := io.ReadAll(io.LimitReader(e.Request.Body, int64(maxBodySize)+1))
	if err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "failed to read request body",
		})
	}
	if len(body) > maxBodySize {
		return e.JSON(http.StatusRequestEntityTooLarge, map[string]string{
			"error": fmt.Sprintf("request body exceeds %d bytes", maxBodySize),
		})
	}
	// The body travels inside a JSON envelope, and JSON carries text. Arbitrary
	// bytes cannot go through it: encoding/json would swap every invalid
	// sequence for U+FFFD and the function would receive a body that is not the
	// one that was sent — silently, which is the worst way to lose a signature.
	// The refusal is explicit instead, and a genuinely binary body is a use case
	// this server does not claim to serve.
	if !utf8.Valid(body) {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "request body must be valid UTF-8",
		})
	}

	allowed, err := requestKeyScope(e)
	if err != nil {
		e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying invocation", "error", err)
		return e.JSON(http.StatusForbidden, map[string]string{"error": errScopeUnreadable.Error()})
	}

	// The envelope is built here, where the request is: the operation must not
	// know what a *http.Request is, and the transport is the only place that
	// does (cf. the cut between invoke.go and invokeops.go).
	outcome, err := invokeFunction(e.Request.Context(), e.App, functionsDir, allowed,
		e.Request.PathValue("name"), newHTTPInput(e.Request, body))
	if err != nil {
		return answerInvokeFailure(e, outcome, err)
	}

	resp := map[string]any{
		"function":    outcome.Function,
		"result":      outcome.Result,
		"duration_ms": outcome.Duration.Milliseconds(),
	}
	if outcome.Stderr != "" {
		resp["stderr"] = outcome.Stderr
	}
	if outcome.Truncated {
		resp["truncated"] = true
	}
	return e.JSON(http.StatusOK, resp)
}

// answerInvokeFailure turns a refused or failed invocation into its response.
//
// The wording is the operation's, deliberately: an execution error, a timeout
// budget, an output that did not survive the capture cap are all things the
// caller has to read, and paraphrasing them here would be a second place to keep
// in step. What this function decides is the status, and what the body carries
// alongside — an execution that ran publishes its streams and its duration, a
// refusal has none to publish.
func answerInvokeFailure(e *core.RequestEvent, outcome invokeOutcome, err error) error {
	switch {
	case errors.Is(err, errInvalidFunctionName):
		return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, errScopeRestricted):
		return e.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, errTooBusy):
		return e.JSON(http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	case errors.Is(err, errResolveFailed):
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	var notFound *errNotFound
	if errors.As(err, &notFound) {
		return e.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}

	var unusable *errOutputUnusable
	if errors.As(err, &unusable) {
		// The function ran to completion, but what came back is a fragment. 502
		// treats the execution engine as a failing upstream rather than blaming
		// the caller's request or the server itself.
		return e.JSON(http.StatusBadGateway, map[string]any{
			"function":    outcome.Function,
			"error":       err.Error(),
			"truncated":   true,
			"stderr":      outcome.Stderr,
			"duration_ms": outcome.Duration.Milliseconds(),
		})
	}

	status := http.StatusInternalServerError
	if errors.Is(err, errExecTimedOut) {
		status = http.StatusGatewayTimeout
	}
	resp := map[string]any{
		"error":       err.Error(),
		"stdout":      outcome.Stdout,
		"stderr":      outcome.Stderr,
		"duration_ms": outcome.Duration.Milliseconds(),
	}
	if outcome.Truncated {
		resp["truncated"] = true
	}
	return e.JSON(status, resp)
}

// listFunctionsHandler answers GET /functions with the functions the presented
// key may invoke, and nothing more. The route carries no {name} path value, so
// the middleware cannot apply the scope for it — the operation does, which is
// what keeps a restricted key from learning the full inventory of the instance.
func listFunctionsHandler(e *core.RequestEvent, functionsDir string) error {
	allowed, err := requestKeyScope(e)
	if err != nil {
		e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying listing", "error", err)
		return e.JSON(http.StatusForbidden, map[string]string{"error": errScopeUnreadable.Error()})
	}

	functions, err := listFunctions(e.App, functionsDir, allowed)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to list the functions", "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "cannot read the functions",
		})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"functions": functions,
		"count":     len(functions),
	})
}
