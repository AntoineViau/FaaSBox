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

func invokeHandler(e *core.RequestEvent, functionsDir string) error {
	name := e.Request.PathValue("name")

	// 1. Validate function name early for a proper 400
	if !validName.MatchString(name) || len(name) > 64 {
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

	// 4. Execute function
	env := lookupFunctionEnv(e.App, name)
	res := executeFunction(e.Request.Context(), functionsDir, name, string(body), env)

	// 5. Decode stdout before logging: an execution whose output did not survive
	// the capture cap is a failure, and the log has to say so.
	var result any
	outputUsable := true
	if res.Err == nil {
		result, outputUsable = parseFunctionOutput(res.Stdout, res.StdoutTruncated)
	}

	// 6. Log execution to faasbox_logs (skip setup errors like not-found)
	var notFound *errNotFound
	var depsFailed *errDepsFailed
	if !errors.As(res.Err, &notFound) && !errors.As(res.Err, &depsFailed) {
		status := "success"
		if res.TimedOut {
			status = "timeout"
		} else if res.Err != nil || !outputUsable {
			status = "error"
		}
		recordExecution(e.App, logEntry{
			FunctionName:   name,
			Trigger:        "http",
			Status:         status,
			DurationMs:     res.Duration.Milliseconds(),
			Stdout:         res.Stdout,
			Stderr:         res.Stderr,
			RequestPayload: string(body),
			ExitCode:       res.ExitCode,
		})
	}

	// 7. Format HTTP response
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

func listFunctionsHandler(e *core.RequestEvent, functionsDir string) error {
	entries, err := os.ReadDir(functionsDir)
	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "cannot read functions directory",
		})
	}

	functions := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		indexPath := filepath.Join(functionsDir, entry.Name(), "index.ts")
		if _, err := os.Stat(indexPath); err == nil {
			functions = append(functions, map[string]string{
				"name":   entry.Name(),
				"invoke": fmt.Sprintf("/invoke/%s", entry.Name()),
			})
		}
	}

	return e.JSON(http.StatusOK, map[string]any{
		"functions": functions,
		"count":     len(functions),
	})
}
