package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

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
		&core.DateField{Name: "expiresAt"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)

	col.AddIndex("idx_faasbox_api_keys_keyHash", true, "keyHash", "")

	return app.Save(col)
}

// hashAPIKey returns the hex-encoded SHA-256 of the raw key.
func hashAPIKey(rawKey string) string {
	h := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(h[:])
}

// generateAPIKey creates a new API key record and returns the raw key.
//
// A zero expiresAt leaves the field empty, which the middleware reads as "never
// expires".
func generateAPIKey(app core.App, name string, allowedFunctions []string, expiresAt types.DateTime) (string, error) {
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

	rawKey, err := generateAPIKey(e.App, body.Name, body.AllowedFunctions, expiresAt)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to generate API key", "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to generate API key",
		})
	}

	return e.JSON(http.StatusOK, map[string]string{
		"key":  rawKey,
		"name": body.Name,
		"note": "Store this key securely. It will not be shown again.",
	})
}

// readKeyScope reads the function scope declared by an API key record.
//
// It returns the authorized function ids; an empty result means no restriction.
// Ids and not names: a scope written in names stopped designating anything the
// day a function was renamed — or, worse, started designating a *different*
// function created since under the old name. PocketBase hands a JSON field back
// under several shapes depending on the access path, hence the type switch.
//
// A field that is present but cannot be decoded yields an error. Callers must
// deny on that error: an unreadable restriction is not an absence of
// restriction, and treating it as such would widen access instead of
// narrowing it.
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
			return nil, fmt.Errorf("cannot re-encode allowedFunctions: %w", err)
		}
		encoded = b
	}

	var allowed []string
	if err := json.Unmarshal(encoded, &allowed); err != nil {
		return nil, fmt.Errorf("allowedFunctions is not a list of function ids: %w", err)
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

// requireAPIKey returns a middleware that validates the X-API-Key header.
func requireAPIKey(app core.App) *hook.Handler[*core.RequestEvent] {
	return &hook.Handler[*core.RequestEvent]{
		Id: "faasboxRequireAPIKey",
		Func: func(e *core.RequestEvent) error {
			// 0. Allow superuser auth as alternative to API key
			if e.Auth != nil && e.Auth.IsSuperuser() {
				return e.Next()
			}

			// 1. Read header
			rawKey := e.Request.Header.Get("X-API-Key")
			if rawKey == "" {
				return e.JSON(http.StatusUnauthorized, map[string]string{
					"error": "Missing X-API-Key header",
				})
			}

			// 2. Validate format
			if len(rawKey) != apiKeyLen || rawKey[:len(apiKeyPrefix)] != apiKeyPrefix {
				return e.JSON(http.StatusUnauthorized, map[string]string{
					"error": "Invalid API key format",
				})
			}

			// 3. Lookup by hash
			keyHash := hashAPIKey(rawKey)
			record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", keyHash)
			if err != nil {
				return e.JSON(http.StatusUnauthorized, map[string]string{
					"error": "Invalid API key",
				})
			}

			// 4. Check active
			if !record.GetBool("active") {
				return e.JSON(http.StatusForbidden, map[string]string{
					"error": "API key is disabled",
				})
			}

			// 5. Check expiration
			expiresAt := record.GetDateTime("expiresAt")
			if !expiresAt.IsZero() && expiresAt.Time().Before(time.Now()) {
				return e.JSON(http.StatusForbidden, map[string]string{
					"error": "API key has expired",
				})
			}

			// 6. Check function scope
			segment := e.Request.PathValue("name")
			if segment != "" {
				allowed, err := readKeyScope(record)
				if err != nil {
					app.Logger().Error("faasbox: unreadable allowedFunctions, denying access",
						"keyName", record.GetString("name"), "error", err)
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
					if !scopeAllows(allowed, target) {
						return e.JSON(http.StatusForbidden, map[string]string{
							"error": fmt.Sprintf("API key is not authorized to invoke function %q", segment),
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
