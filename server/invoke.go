package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// sem limits the number of concurrent Bun processes.
var sem = make(chan struct{}, maxConcurrency)

// validName whitelists function names: alphanumeric + hyphens, no path traversal.
var validName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?$`)

// invokeHandler answers POST /invoke/{idOrName}. The path segment designates the
// function by either spelling; resolveFunction carries the rule and the tie-break.
func invokeHandler(e *core.RequestEvent, functionsDir string) error {
	segment := e.Request.PathValue("name")

	// 1. Validate the segment early for a proper 400. Unchanged: the same rule
	// covers a name and an id, ids being 15 lowercase alphanumerics.
	if !validName.MatchString(segment) || len(segment) > 64 {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid function name",
		})
	}

	// 2. Read request body (payload passed to the function via stdin)
	// Read one extra byte beyond the limit to detect oversized payloads.
	// Without this, LimitReader silently truncates the body and the function
	// receives broken JSON, producing a confusing parsing error.
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

	// 3. Acquire semaphore (non-blocking → 429 if full)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		return e.JSON(http.StatusTooManyRequests, map[string]string{
			"error": "too many concurrent invocations, try again later",
		})
	}

	// 4. Resolve the function, then execute it. The record replaces the lookup
	// lookupFunctionEnv used to make: the secrets come off the row already read.
	fn, err := resolveFunction(e.App, segment)
	if err != nil {
		var notFound *errNotFound
		if !errors.As(err, &notFound) {
			e.App.Logger().Error("faasbox http: failed to resolve the function",
				"segment", segment, "error", err)
			return e.JSON(http.StatusInternalServerError, map[string]string{
				"error": "failed to resolve the function",
			})
		}
		// Nothing ran, so nothing is recorded — same rule as an absent index.ts.
		return e.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	name := fn.GetString("name")

	env := functionEnv(e.App, fn)
	res := executeFunction(e.Request.Context(), functionsDir, fn.Id, name, string(body), env)

	// 5. Publish what the dependency safety net did, if anything. The cron path
	// does the same right after its own call — the two must not diverge.
	publishDepsOutcome(e.App, functionsDir, fn.Id, name, res)

	// 6. Decode stdout before logging: an execution whose output did not survive
	// the capture cap is a failure, and the log has to say so.
	var result any
	outputUsable := true
	if res.Err == nil {
		result, outputUsable = parseFunctionOutput(res.Stdout, res.StdoutTruncated)
	}

	// 7. Log execution to faasbox_logs. Only errNotFound stays out: nothing ran,
	// so there is no execution to record. A dependency install that failed did
	// take place and earns its line, at the same title as on the cron path — the
	// path where someone is waiting for an answer was the one explaining nothing.
	var notFound *errNotFound
	if !errors.As(res.Err, &notFound) {
		status := "success"
		if res.TimedOut {
			status = "timeout"
		} else if res.Err != nil || !outputUsable {
			status = "error"
		}
		recordExecution(e.App, logEntry{
			FunctionId:     fn.Id,
			FunctionName:   name,
			Trigger:        "http",
			Status:         status,
			DurationMs:     res.Duration.Milliseconds(),
			Stdout:         res.Stdout,
			Stderr:         res.Stderr,
			RequestPayload: string(body),
			ExitCode:       res.ExitCode,
		})

		// Same rule for the server log, and the same reason: a failure the caller
		// is told about still has to leave something behind for whoever reads the
		// logs during an incident. runFunction has done this all along.
		if res.Err != nil {
			e.App.Logger().Error("faasbox http: execution failed",
				"function", name, "error", res.Err, "stdout", res.Stdout, "stderr", res.Stderr, "truncated", res.Truncated)
		}
	}

	// 8. Format HTTP response
	if res.Err != nil {
		if errors.As(res.Err, &notFound) {
			return e.JSON(http.StatusNotFound, map[string]string{
				"error": res.Err.Error(),
			})
		}

		status := http.StatusInternalServerError
		errMsg := res.Err.Error()
		if res.TimedOut {
			status = http.StatusGatewayTimeout
			errMsg = fmt.Sprintf("function timed out after %s", execTimeout)
		}
		errResp := map[string]any{
			"error":       errMsg,
			"stdout":      res.Stdout,
			"stderr":      res.Stderr,
			"duration_ms": res.Duration.Milliseconds(),
		}
		if res.Truncated {
			errResp["truncated"] = true
		}
		return e.JSON(status, errResp)
	}

	if !outputUsable {
		// The function ran to completion, but what came back is a fragment.
		// 502 treats the execution engine as a failing upstream rather than
		// blaming the caller's request or the server itself.
		return e.JSON(http.StatusBadGateway, map[string]any{
			"function": name,
			"error": fmt.Sprintf(
				"function output exceeded the %d bytes capture limit and the truncated result is not valid JSON; raise FAASBOX_MAX_OUTPUT_SIZE or return less data",
				maxOutputSize,
			),
			"truncated":   true,
			"stderr":      res.Stderr,
			"duration_ms": res.Duration.Milliseconds(),
		})
	}

	resp := map[string]any{
		"function":    name,
		"result":      result,
		"duration_ms": res.Duration.Milliseconds(),
	}
	if res.Stderr != "" {
		resp["stderr"] = res.Stderr
	}
	if res.Truncated {
		resp["truncated"] = true
	}

	return e.JSON(http.StatusOK, resp)
}

// parseFunctionOutput decodes a function's stdout into the value returned as
// "result". Non-JSON output is handed back verbatim — writing free text is a
// legitimate way to answer. That fallback is refused when the capture was
// truncated: the function may well have produced valid JSON, and returning the
// surviving fragment as a plain string would pass a mutilated answer off as a
// good one. A truncation that happens to leave valid JSON behind is
// undetectable, and the "truncated" flag remains its only signal.
func parseFunctionOutput(stdout string, truncated bool) (result any, usable bool) {
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		if truncated {
			return nil, false
		}
		return stdout, true
	}
	return result, true
}

// listFunctionsHandler answers GET /functions with the functions the presented
// key may invoke, and nothing more. The route carries no {name} path value, so
// the middleware cannot apply the scope for it — enforcing it here is what
// keeps a restricted key from learning the full inventory of the instance.
//
// The inventory comes from the database. The directory can no longer supply it:
// its entries are named by id, and a listing has to answer with the name. The
// disk still has the last word on whether a function is invocable — an index.ts
// under the id directory — which is what the listing has always meant.
func listFunctionsHandler(e *core.RequestEvent, functionsDir string) error {
	allowed, err := requestKeyScope(e)
	if err != nil {
		e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying listing", "error", err)
		return e.JSON(http.StatusForbidden, map[string]string{
			"error": "API key scope cannot be read",
		})
	}

	records, err := e.App.FindAllRecords(faasboxFunctionsCollection)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to list the functions", "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "cannot read the functions",
		})
	}

	functions := make([]map[string]string, 0, len(records))
	for _, record := range records {
		if !scopeAllows(allowed, record.Id) {
			continue
		}
		indexPath := filepath.Join(functionsDir, record.Id, "index.ts")
		if _, err := os.Stat(indexPath); err != nil {
			continue
		}
		name := record.GetString("name")
		functions = append(functions, map[string]string{
			"id":     record.Id,
			"name":   name,
			"invoke": fmt.Sprintf("/invoke/%s", name),
		})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"functions": functions,
		"count":     len(functions),
	})
}
