package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

// This file carries what an API key is, end to end: its shape and its storage,
// the two dimensions of its authority, and the middleware that validates it —
// whether the caller proved itself with a key or with an OAuth access token,
// which deposits an API key record all the same (cf. oauthbearer.go).
//
// The two dimensions are not the same question and must not collapse into one.
// allowedFunctions says *which* functions a key reaches — the scope. canManage
// says *what it may do* with them. Losing an invoke-only key lets someone call
// code that already exists; losing a canManage key lets them write the code this
// server executes.
//
// The scope lives here whole: how it is read off a record (readKeyScope), how it
// is decided (scopeIsUnrestricted, scopeAllows), and how it is refused
// (errScopeUnreadable, errScopeRestricted, scopeRefusalFor). The refusals sit
// next to the rules that produce them on purpose — they are returned by the
// operations in manageops.go and invokeops.go, which check the scope they are
// handed rather than assume a middleware did (cf. backend.md » Gestion des
// fonctions par l'API » Une couche appelable sans requete). A sentinel kept away
// from its rule is a sentinel that stops matching it.
//
// Past the 300-line guideline, and it was already past it before the refusals
// arrived. The responsibility is single; splitting it would separate a refusal
// from the check that raises it, or the middleware from the format it validates.

const (
	apiKeyPrefix = "fbx_"
	apiKeyLen    = 44 // len(apiKeyPrefix) + 40 random chars

	faasboxAPIKeysCollection = "faasbox_api_keys"

	// apiKeyContextKey names the request-context slot where the middleware
	// leaves the authenticated key record for downstream handlers.
	apiKeyContextKey = "__faasboxApiKey"
)

// ensureAPIKeysCollection creates the faasbox_api_keys collection if it doesn't exist.
func ensureAPIKeysCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(faasboxAPIKeysCollection); err == nil {
		return nil // already exists
	}

	col := core.NewBaseCollection(faasboxAPIKeysCollection)

	col.Fields.Add(
		&core.TextField{Name: "name", Required: true},
		&core.TextField{Name: "keyHash", Required: true, Hidden: true},
		&core.TextField{Name: "keyPrefix", Required: true},
		&core.JSONField{Name: "allowedFunctions"},
		&core.BoolField{Name: "active"},
		// canManage is the second dimension of a key's authority: allowedFunctions
		// says *which* functions it reaches, this says *what it may do* with them.
		// Absent means invoke-only, which is what every key was before it existed.
		&core.BoolField{Name: "canManage"},
		&core.DateField{Name: "expiresAt"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)

	col.AddIndex("idx_faasbox_api_keys_keyHash", true, "keyHash", "")

	return app.Save(col)
}

// apiKeyName is the only way to read the label of a key record, which is
// encrypted at rest along with keyPrefix. Nothing in Go reads keyPrefix — it
// exists to identify a key on screen, and the enrichment opens it for the
// response — so it has no accessor of its own.
//
// **keyHash is encrypted by neither, and never will be.** A hash does not come
// back where a ciphertext would: sealing it would reopen a path by which the
// database and the master key together hand out a usable credential. The
// validation of a presented key keeps going through hashAPIKey and a lookup on
// that column, which is also what lets it stay indexed.
//
// The in-memory record an OAuth token stands for carries a plaintext name and
// was never sealed; it reads through here like any other, a value without the
// version marker coming back untouched.
func apiKeyName(app core.App, record *core.Record) string {
	return decryptedText(app, record, "name")
}

// hashAPIKey returns the hex-encoded SHA-256 of the raw key.
func hashAPIKey(rawKey string) string {
	h := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(h[:])
}

// generateAPIKey creates a new API key record and returns the raw key.
//
// A zero expiresAt leaves the field empty, which the middleware reads as "never
// expires". canManage opens the function management routes: a key carrying it
// can replace the code of a function, which is arbitrary execution on this
// server — hence the parameter rather than a default.
func generateAPIKey(app core.App, name string, allowedFunctions []string, expiresAt types.DateTime, canManage bool) (string, error) {
	rawKey := apiKeyPrefix + security.RandomString(apiKeyLen-len(apiKeyPrefix))
	keyHash := hashAPIKey(rawKey)

	col, err := app.FindCollectionByNameOrId(faasboxAPIKeysCollection)
	if err != nil {
		return "", fmt.Errorf("collection %s not found: %w", faasboxAPIKeysCollection, err)
	}

	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("keyHash", keyHash)
	record.Set("keyPrefix", rawKey[:16])
	record.Set("allowedFunctions", allowedFunctions)
	record.Set("active", true)
	record.Set("canManage", canManage)
	if !expiresAt.IsZero() {
		record.Set("expiresAt", expiresAt)
	}

	if err := app.Save(record); err != nil {
		return "", fmt.Errorf("failed to save API key: %w", err)
	}

	return rawKey, nil
}

// createKeyHandler handles POST /api/faasbox/keys (superuser only).
func createKeyHandler(e *core.RequestEvent) error {
	var body struct {
		Name             string   `json:"name"`
		AllowedFunctions []string `json:"allowedFunctions"`
		ExpiresAt        string   `json:"expiresAt"` // RFC3339, optional
		CanManage        bool     `json:"canManage"` // absent means invoke-only
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
	}
	if body.Name == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "\"name\" is required",
		})
	}

	// An absent expiry means the key never expires. A value that is present but
	// unparsable is rejected rather than dropped: types.ParseDateTime falls back
	// to the zero date instead of reporting the failure, and silently turning a
	// bounded key into a perpetual one would widen access, not narrow it.
	var expiresAt types.DateTime
	if body.ExpiresAt != "" {
		parsed, err := types.ParseDateTime(body.ExpiresAt)
		if err != nil || parsed.IsZero() {
			return e.JSON(http.StatusBadRequest, map[string]string{
				"error": "\"expiresAt\" must be an RFC3339 date",
			})
		}
		expiresAt = parsed
	}

	rawKey, err := generateAPIKey(e.App, body.Name, body.AllowedFunctions, expiresAt, body.CanManage)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to generate API key", "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to generate API key",
		})
	}

	// Answered from what the caller sent and from what was just minted, never
	// re-read off the record: the label and the prefix are sealed the moment they
	// are stored, and reading them back would hand the caller base64 instead of
	// its own key. The raw value carries its prefix in its first characters.
	return e.JSON(http.StatusOK, map[string]string{
		"key":  rawKey,
		"name": body.Name,
		"note": "Store this key securely. It will not be shown again.",
	})
}

// errScopeUnreadable marks a scope that is declared but cannot be decoded. It
// is a refusal and not an absence of restriction: reading it as "no scope" would
// widen access instead of narrowing it. Every transport answers 403.
var errScopeUnreadable = errors.New("API key scope cannot be read")

// errScopeRestricted marks an operation refused by the scope it was handed.
// Every transport answers 403; what differs is the wording, which the refusal
// carries — a scope that does not cover the function asked about, or a
// restricted scope that may create none.
var errScopeRestricted = errors.New("API key is not authorized for this function")

// refusedScope is one such refusal, spelled out for the caller. Same shape as
// refusedTrigger in managecrons.go: a sentinel says what class of refusal it is,
// the value says which one.
type refusedScope struct{ message string }

func (r *refusedScope) Error() string { return r.message }
func (r *refusedScope) Unwrap() error { return errScopeRestricted }

// scopeRefusalFor is the refusal every per-function operation returns. Its
// wording matches what requireAPIKey answers on a route that carries a segment,
// so the same refusal reads the same whichever barrier caught it.
func scopeRefusalFor(idOrName string) error {
	return &refusedScope{
		message: fmt.Sprintf("API key is not authorized for function %q", idOrName),
	}
}

// readKeyScope reads the function scope declared by an API key record.
//
// It returns the authorized function ids; an empty result means no restriction.
// Ids and not names: a scope written in names stopped designating anything the
// day a function was renamed — or, worse, started designating a *different*
// function created since under the old name. PocketBase hands a JSON field back
// under several shapes depending on the access path, hence the type switch.
//
// A field that is present but cannot be decoded yields an error wrapping
// errScopeUnreadable. Callers must deny on that error: an unreadable restriction
// is not an absence of restriction, and treating it as such would widen access
// instead of narrowing it.
func readKeyScope(record *core.Record) ([]string, error) {
	raw := record.Get("allowedFunctions")
	if raw == nil {
		return nil, nil
	}

	var encoded []byte
	switch v := raw.(type) {
	case []string:
		return v, nil
	case string:
		if v == "" {
			return nil, nil
		}
		encoded = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot re-encode allowedFunctions: %w", errScopeUnreadable, err)
		}
		encoded = b
	}

	var allowed []string
	if err := json.Unmarshal(encoded, &allowed); err != nil {
		return nil, fmt.Errorf("%w: allowedFunctions is not a list of function ids: %w", errScopeUnreadable, err)
	}
	return allowed, nil
}

// requestKeyScope returns the function scope carried by the request.
//
// A superuser session short-circuits the middleware and leaves no key record
// behind: it is unrestricted, exactly like a key that declares no scope. The
// error case is the caller's to deny on, same as readKeyScope.
func requestKeyScope(e *core.RequestEvent) ([]string, error) {
	record, ok := e.Get(apiKeyContextKey).(*core.Record)
	if !ok || record == nil {
		return nil, nil
	}
	return readKeyScope(record)
}

// scopeIsUnrestricted reports whether a scope grants every function without
// looking at any of them: an empty list carries no restriction, "*" is the
// wildcard. Split out of scopeAllows because the middleware needs the answer
// *before* it has a function to ask about — a scope that grants everything has
// nothing to compare, so it needs no lookup to compare it with.
func scopeIsUnrestricted(allowed []string) bool {
	return len(allowed) == 0 || slices.Contains(allowed, "*")
}

// scopeAllows reports whether a scope authorizes a function id.
//
// Invocation and enumeration ask this same question, and a rule spelled out
// twice is a rule that gets fixed once.
func scopeAllows(allowed []string, functionId string) bool {
	return scopeIsUnrestricted(allowed) || slices.Contains(allowed, functionId)
}

// requireManageKey returns a middleware that gates the function management
// routes on the canManage flag. It is bound *after* requireAPIKey, which is what
// leaves the key record in the request context.
//
// A superuser session short-circuits requireAPIKey and therefore leaves no
// record behind: it is recognised here for the same reason, and not read as an
// absent flag. Any other request without a record never went through the
// middleware chain and is refused rather than trusted.
//
// The flag is a second dimension, not a stronger scope: a key needs both a scope
// covering the function and canManage to change it. Losing an invoke-only key
// lets someone call code that already exists; losing a canManage key lets them
// write the code the server executes.
func requireManageKey() *hook.Handler[*core.RequestEvent] {
	return &hook.Handler[*core.RequestEvent]{
		Id: "faasboxRequireManageKey",
		Func: func(e *core.RequestEvent) error {
			if e.Auth != nil && e.Auth.IsSuperuser() {
				return e.Next()
			}

			record, ok := e.Get(apiKeyContextKey).(*core.Record)
			if !ok || record == nil || !record.GetBool("canManage") {
				return e.JSON(http.StatusForbidden, map[string]string{
					"error": "API key is not authorized to manage functions",
				})
			}

			return e.Next()
		},
	}
}

// apiKeysOnly is the footing of every route that takes an API key and nothing
// else: no bearer token is read, and no OAuth challenge is posted on its 401.
//
// Only /mcp is mounted on the other footing. Posting the challenge elsewhere
// would send a caller with a curl on a whole OAuth journey to obtain a token
// those routes do not accept.
var apiKeysOnly = oauthConfig{}

// keyRefusal is a credential that did not pass: the status and the wording, kept
// together so one place renders them and one place decides what rides along.
type keyRefusal struct {
	status  int
	message string
}

// authenticateCaller reads the credential a request presents and returns the API
// key record it stands for.
//
// **A key and a token presented together: the key wins**, and the token is never
// looked at. One path per request, decided by what is there rather than by which
// check happens to pass first.
//
// The bearer path is open only on a route mounted with an OAuth configuration —
// which is to say /mcp, on an instance where FAASBOX_PUBLIC_URL named an issuer.
func authenticateCaller(app core.App, e *core.RequestEvent, oauth oauthConfig) (*core.Record, *keyRefusal) {
	rawKey := e.Request.Header.Get("X-API-Key")
	if rawKey != "" {
		return authenticateAPIKey(app, rawKey)
	}

	if token := bearerToken(e.Request); token != "" && oauth.Resource != "" {
		record, err := bearerAPIKey(app, token, oauth)
		if err != nil {
			if !errors.Is(err, errBearerRefused) {
				app.Logger().Error("faasbox: failed to weigh an OAuth access token", "error", err)
			}
			return nil, &keyRefusal{http.StatusUnauthorized, "Invalid access token"}
		}
		return record, nil
	}

	return nil, &keyRefusal{http.StatusUnauthorized, "Missing X-API-Key header"}
}

// authenticateAPIKey is the key half of the chain: format, lookup, active, and
// expiry. The scope is not weighed here — it is the middleware's business, and
// it is the same question whichever credential produced the record.
func authenticateAPIKey(app core.App, rawKey string) (*core.Record, *keyRefusal) {
	if len(rawKey) != apiKeyLen || rawKey[:len(apiKeyPrefix)] != apiKeyPrefix {
		return nil, &keyRefusal{http.StatusUnauthorized, "Invalid API key format"}
	}

	record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", hashAPIKey(rawKey))
	if err != nil {
		return nil, &keyRefusal{http.StatusUnauthorized, "Invalid API key"}
	}

	if !record.GetBool("active") {
		return nil, &keyRefusal{http.StatusForbidden, "API key is disabled"}
	}

	expiresAt := record.GetDateTime("expiresAt")
	if !expiresAt.IsZero() && expiresAt.Time().Before(time.Now()) {
		return nil, &keyRefusal{http.StatusForbidden, "API key has expired"}
	}

	return record, nil
}

// requireAPIKey returns a middleware that validates the credential a request
// presents: the X-API-Key header, or — on a route mounted with an OAuth
// configuration — an Authorization: Bearer access token.
//
// **The challenge is a parameter of this middleware, empty everywhere else.**
// It is already the place that emits the 401 and, since tokens, the place that
// understands a bearer: a separate middleware posting the signpost would have to
// guess the answer another one wrote. The zero configuration is what turns both
// halves off — no token read, no challenge posted.
func requireAPIKey(app core.App, oauth oauthConfig) *hook.Handler[*core.RequestEvent] {
	return &hook.Handler[*core.RequestEvent]{
		Id: "faasboxRequireAPIKey",
		Func: func(e *core.RequestEvent) error {
			// 0. Allow superuser auth as alternative to API key
			if e.Auth != nil && e.Auth.IsSuperuser() {
				return e.Next()
			}

			// 1-5. Read and weigh the credential, key or token.
			record, refused := authenticateCaller(app, e, oauth)
			if refused != nil {
				// The signpost rides the 401 and only the 401: a 403 has already
				// identified its caller, and telling it where to authenticate
				// would be answering a question it did not ask.
				if refused.status == http.StatusUnauthorized {
					if challenge := oauth.bearerChallenge(); challenge != "" {
						e.Response.Header().Set("WWW-Authenticate", challenge)
					}
				}
				return e.JSON(refused.status, map[string]string{"error": refused.message})
			}

			// 6. Check function scope
			segment := e.Request.PathValue("name")
			if segment != "" {
				allowed, err := readKeyScope(record)
				if err != nil {
					app.Logger().Error("faasbox: unreadable allowedFunctions, denying access",
						"keyName", apiKeyName(app, record), "error", err)
					return e.JSON(http.StatusForbidden, map[string]string{
						"error": "API key scope cannot be read",
					})
				}
				// A scope that grants everything is answered without a lookup. The
				// check below costs one read of faasbox_functions, and the handler
				// pays a second one to run the function: that is the price of a
				// *restricted* key, and there is no reason for the common case to
				// pay it too.
				if !scopeIsUnrestricted(allowed) {
					// The scope lists ids, so the segment has to be resolved before
					// it can be compared. A segment that resolves to nothing is
					// weighed as it stands, so the key is refused before it learns
					// whether the name exists — which is what happened before ids,
					// and is the half of the inventory the scope filter withholds.
					target := segment
					if fn, err := resolveFunction(app, segment); err == nil {
						target = fn.Id
					}
					// The wording does not name invocation: the same check now
					// also governs reading, replacing and deleting a function.
					if !scopeAllows(allowed, target) {
						return e.JSON(http.StatusForbidden, map[string]string{
							"error": fmt.Sprintf("API key is not authorized for function %q", segment),
						})
					}
				}
			}

			// 7. Store record for logging and for handlers that enforce the
			// scope themselves, the routes without a {name} path value.
			e.Set(apiKeyContextKey, record)

			return e.Next()
		},
	}
}
