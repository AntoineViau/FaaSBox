package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

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
	// Read request body (payload passed to the function via stdin). One extra
	// byte beyond the limit, to detect oversized payloads: without it,
	// LimitReader silently truncates the body and the function receives broken
	// JSON, producing a confusing parsing error.
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

	allowed, err := requestKeyScope(e)
	if err != nil {
		e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying invocation", "error", err)
		return e.JSON(http.StatusForbidden, map[string]string{"error": errScopeUnreadable.Error()})
	}

	outcome, err := invokeFunction(e.Request.Context(), e.App, functionsDir, allowed,
		e.Request.PathValue("name"), body)
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
