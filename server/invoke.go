package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

const (
	defaultMaxConcurrency = 4
	maxBodySize           = 1 << 20 // 1 MB
)

// maxConcurrency is the maximum number of concurrent Bun processes.
// Configurable via FAASBOX_MAX_CONCURRENCY environment variable.
var maxConcurrency = func() int {
	s := os.Getenv("FAASBOX_MAX_CONCURRENCY")
	if s == "" {
		return defaultMaxConcurrency
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		log.Printf("faasbox: invalid FAASBOX_MAX_CONCURRENCY=%q, using default %d", s, defaultMaxConcurrency)
		return defaultMaxConcurrency
	}
	return n
}()

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
	body, err := io.ReadAll(io.LimitReader(e.Request.Body, maxBodySize+1))
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

	// 5. Log execution to faasbox_logs (skip setup errors like not-found)
	var notFound *errNotFound
	var depsFailed *errDepsFailed
	if !errors.As(res.Err, &notFound) && !errors.As(res.Err, &depsFailed) {
		status := "success"
		if res.TimedOut {
			status = "timeout"
		} else if res.Err != nil {
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

	// 6. Format HTTP response
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

	var result any
	if err := json.Unmarshal([]byte(res.Stdout), &result); err != nil {
		result = res.Stdout
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
