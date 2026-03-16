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
)

const (
	apiKeyPrefix = "fbx_"
	apiKeyLen    = 44 // len(apiKeyPrefix) + 40 random chars

	faasboxAPIKeysCollection = "faasbox_api_keys"
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
func generateAPIKey(app core.App, name string, allowedFunctions []string) (string, error) {
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

	rawKey, err := generateAPIKey(e.App, body.Name, body.AllowedFunctions)
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
			name := e.Request.PathValue("name")
			if name != "" {
				raw := record.Get("allowedFunctions")
				if raw != nil {
					var allowed []string
					switch v := raw.(type) {
					case []string:
						allowed = v
					case string:
						if v != "" {
							if err := json.Unmarshal([]byte(v), &allowed); err != nil {
								app.Logger().Warn("faasbox: failed to parse allowedFunctions",
									"keyName", record.GetString("name"), "error", err)
							}
						}
					default:
						// Try JSON marshal/unmarshal for other types (e.g. []any)
						if b, err := json.Marshal(v); err == nil {
							if err := json.Unmarshal(b, &allowed); err != nil {
								app.Logger().Warn("faasbox: failed to parse allowedFunctions",
									"keyName", record.GetString("name"), "error", err)
							}
						}
					}
					if len(allowed) > 0 && !slices.Contains(allowed, "*") && !slices.Contains(allowed, name) {
						return e.JSON(http.StatusForbidden, map[string]string{
							"error": fmt.Sprintf("API key is not authorized to invoke function %q", name),
						})
					}
				}
			}

			// 7. Store record for logging
			e.Set("__faasboxApiKey", record)

			return e.Next()
		},
	}
}
