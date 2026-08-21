package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// This file answers "how is a log read by the API"; logs.go answers "how is a
// log written and pruned". Same cut as deps.go / depsstate.go and manage.go /
// managetriggers.go — both live in package main, so it changes no visibility.
//
// It exists because faasbox_logs was only reachable through the PocketBase
// collections API, whose rules are nil: reading three lines of stderr took a
// superuser token, which is full power over the instance. The gap shows on a
// cron trigger — a scheduled run answers no HTTP response, so the log entry is
// the only trace it ever leaves.
//
// The right required is canManage, not mere invocation. A log carries
// requestPayload and the output of runs triggered by someone other than the
// key holder, which an invocation response never shows. Loosening later towards
// invoke-only keys stays possible; the reverse would be a tightening.

const (
	// defaultLogsLimit is what the editor's Logs panel already shows.
	defaultLogsLimit = 50
	// maxLogsLimit caps what one response may carry. A route handing back as
	// many records as it is asked for is an unbounded path.
	maxLogsLimit = 200
)

// errBadLogsLimit marks a ?limit the server cannot make sense of. It is a
// refusal, not a value to fall back from: ignoring it would answer something
// other than what was asked, the same reason an unreadable expiresAt is
// refused at key creation rather than dropped.
var errBadLogsLimit = errors.New("invalid limit")

// logContract is what the log route publishes, per entry.
//
// Narrower than the record, like the management contract. `function` is not
// here: the route is already per function and the identity is in the path.
// `functionName` stays, because it does not repeat it — it is the name the
// function carried *at the time of the run*, deliberately not refreshed.
type logContract struct {
	Id             string          `json:"id"`
	FunctionName   string          `json:"functionName"`
	Trigger        string          `json:"trigger"`
	Status         string          `json:"status"`
	DurationMs     int64           `json:"durationMs"`
	Stdout         string          `json:"stdout"`
	Stderr         string          `json:"stderr"`
	RequestPayload json.RawMessage `json:"requestPayload"`
	ExitCode       int             `json:"exitCode"`
	Truncated      bool            `json:"truncated"`
	Created        string          `json:"created"`
}

// functionLogsHandler answers GET /api/faasbox/functions/{idOrName}/logs.
//
// The query string is read here and the reading is done by functionLogs: what a
// `?limit` of "abc" means is a transport's question, what a limit of 3 returns
// is not.
func functionLogsHandler(e *core.RequestEvent) error {
	allowed, err := requestKeyScope(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	limit, err := logsLimit(e.Request.URL.Query().Get("limit"))
	if err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": `"limit" must be a whole number of at least 1`,
		})
	}

	logs, err := functionLogs(e.App, allowed, e.Request.PathValue("name"), limit)
	if err != nil {
		return answerManageFailure(e, err, "Failed to read the logs of this function")
	}

	return e.JSON(http.StatusOK, map[string]any{
		"logs":  logs,
		"count": len(logs),
	})
}

// logsLimit reads the ?limit query value.
//
// Two refusals, and the distinction is the point. A limit of 1000 is a
// legitimate ask the server bounds, so it is capped. A limit of "abc" is an ask
// nobody understands, so it is refused.
func logsLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLogsLimit, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errBadLogsLimit
	}
	if value > maxLogsLimit {
		return maxLogsLimit, nil
	}
	return value, nil
}

// logContractOf renders one stored entry into the published shape.
//
// created goes out as RFC3339 UTC rather than PocketBase's own layout: files.go
// already publishes modified that way, and two date formats in the same API
// would be a free trap.
//
// durationMs is camelCase, aligned on depsStatus of the management contract
// rather than on the duration_ms of the invocation response: the routes under
// /api/faasbox/ speak camelCase, and /invoke keeps its own published contract.
func logContractOf(app core.App, record *core.Record) logContract {
	return logContract{
		Id:             record.Id,
		FunctionName:   decryptedText(app, record, "functionName"),
		Trigger:        record.GetString("trigger"),
		Status:         record.GetString("status"),
		DurationMs:     int64(record.GetInt("duration")),
		Stdout:         decryptedText(app, record, "stdout"),
		Stderr:         decryptedText(app, record, "stderr"),
		RequestPayload: rawPayload(app, record),
		ExitCode:       record.GetInt("exitCode"),
		Truncated:      record.GetBool("truncated"),
		Created:        record.GetDateTime("created").Time().UTC().Format(time.RFC3339),
	}
}

// rawPayload hands back the payload of an entry as a JSON document, and nil —
// which marshals to `null` — for an entry that carries none.
//
// An empty non-nil json.RawMessage is not "no payload": it is a zero-byte
// document, and marshalling it fails outright. The distinction has to be made
// here, once, rather than surface as a 500 on the first entry without a body.
//
// **It has to open the column before deciding what shape it is looking at**, and
// getting that wrong degrades in silence where every other reader of a sealed
// column fails loudly. A payload cut at the storage cap is no longer valid JSON,
// so it is kept as an escaped **string** rather than an object — which is
// exactly the shape a ciphertext takes. Relayed as stored, the sealed value
// would go out looking like a legitimately truncated payload, a case the API
// documents as normal, and nothing would report anything. decryptedJSON opens it
// first and restores the two shapes the caller is told to expect.
func rawPayload(app core.App, record *core.Record) json.RawMessage {
	payload := decryptedJSON(app, record, "requestPayload")
	if payload == "" {
		return nil
	}
	return json.RawMessage(payload)
}
