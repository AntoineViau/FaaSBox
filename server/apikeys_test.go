package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// --- Pure function tests (no PocketBase) ---

func TestHashAPIKey(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"simple", "hello"},
		{"api key format", "fbx_abcdefghijklmnopqrstuvwxyz1234567890abcdef"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hashAPIKey(tc.input)

			// Verify against direct SHA-256 computation
			h := sha256.Sum256([]byte(tc.input))
			want := hex.EncodeToString(h[:])

			if got != want {
				t.Errorf("hashAPIKey(%q) = %q, want %q", tc.input, got, want)
			}

			// Verify it's a valid hex string of correct length (64 chars = 32 bytes)
			if len(got) != 64 {
				t.Errorf("hashAPIKey(%q) length = %d, want 64", tc.input, len(got))
			}
		})
	}
}

func TestHashAPIKey_Deterministic(t *testing.T) {
	input := "fbx_test1234567890abcdefghijklmnopqrstuvwxyz"
	h1 := hashAPIKey(input)
	h2 := hashAPIKey(input)
	if h1 != h2 {
		t.Errorf("hashAPIKey is not deterministic: %q != %q", h1, h2)
	}
}

// --- TestApp-based tests ---

func TestEnsureAPIKeysCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// First call: should create the collection
	if err := ensureAPIKeysCollection(app); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	col, err := app.FindCollectionByNameOrId(faasboxAPIKeysCollection)
	if err != nil {
		t.Fatalf("collection not found after creation: %v", err)
	}

	// Verify expected fields exist
	expectedFields := []string{"name", "keyHash", "keyPrefix", "allowedFunctions", "active", "expiresAt"}
	for _, fieldName := range expectedFields {
		if col.Fields.GetByName(fieldName) == nil {
			t.Errorf("field %q not found in collection", fieldName)
		}
	}

	// Verify keyHash is hidden
	keyHashField := col.Fields.GetByName("keyHash")
	if keyHashField != nil {
		if tf, ok := keyHashField.(*core.TextField); ok {
			if !tf.Hidden {
				t.Error("keyHash field should be hidden")
			}
		}
	}

	// Second call: should be idempotent (no error)
	if err := ensureAPIKeysCollection(app); err != nil {
		t.Fatalf("second call (idempotent) failed: %v", err)
	}
}

func TestGenerateAPIKey(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	rawKey, err := generateAPIKey(app, "test-key", []string{"echo"}, types.DateTime{})
	if err != nil {
		t.Fatalf("generateAPIKey failed: %v", err)
	}

	// Check format
	if len(rawKey) != apiKeyLen {
		t.Errorf("key length = %d, want %d", len(rawKey), apiKeyLen)
	}
	if !strings.HasPrefix(rawKey, apiKeyPrefix) {
		t.Errorf("key should start with %q, got %q", apiKeyPrefix, rawKey[:8])
	}

	// Check that the hash is stored in DB
	hash := hashAPIKey(rawKey)
	record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", hash)
	if err != nil {
		t.Fatalf("key record not found in DB: %v", err)
	}

	// Check record fields
	if record.GetString("name") != "test-key" {
		t.Errorf("record name = %q, want %q", record.GetString("name"), "test-key")
	}
	if record.GetString("keyPrefix") != rawKey[:16] {
		t.Errorf("record keyPrefix = %q, want %q", record.GetString("keyPrefix"), rawKey[:16])
	}
	if !record.GetBool("active") {
		t.Error("record should be active by default")
	}

	// Two generated keys should be different
	rawKey2, err := generateAPIKey(app, "test-key-2", nil, types.DateTime{})
	if err != nil {
		t.Fatalf("second generateAPIKey failed: %v", err)
	}
	if rawKey == rawKey2 {
		t.Error("two generated keys should be different")
	}
}

// --- ApiScenario-based tests ---

func TestCreateKeyHandler(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:   "success with superuser auth",
			Method: http.MethodPost,
			URL:    "/api/faasbox/keys",
			Body:   strings.NewReader(`{"name":"my-key","allowedFunctions":["echo"]}`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"key":"fbx_`,
				`"name":"my-key"`,
			},
		},
		{
			Name:   "unauthorized without auth",
			Method: http.MethodPost,
			URL:    "/api/faasbox/keys",
			Body:   strings.NewReader(`{"name":"my-key"}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{`"message"`},
		},
		{
			Name:   "bad request without name",
			Method: http.MethodPost,
			URL:    "/api/faasbox/keys",
			Body:   strings.NewReader(`{}`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`is required`},
		},
		{
			Name:   "bad request with invalid JSON",
			Method: http.MethodPost,
			URL:    "/api/faasbox/keys",
			Body:   strings.NewReader(`not json`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`"error"`},
		},
		{
			Name:   "success with a valid expiresAt",
			Method: http.MethodPost,
			URL:    "/api/faasbox/keys",
			Body:   strings.NewReader(`{"name":"expiring-key","expiresAt":"2030-01-02T03:04:05Z"}`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"key":"fbx_`,
				`"name":"expiring-key"`,
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "name", "expiring-key")
				if err != nil {
					t.Fatalf("key record not found: %v", err)
				}
				got := record.GetDateTime("expiresAt")
				if got.IsZero() {
					t.Fatal("expiresAt was not persisted")
				}
				want := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
				if !got.Time().Equal(want) {
					t.Errorf("expiresAt = %v, want %v", got.Time(), want)
				}
			},
		},
		{
			Name:   "bad request with an unparsable expiresAt",
			Method: http.MethodPost,
			URL:    "/api/faasbox/keys",
			Body:   strings.NewReader(`{"name":"bad-expiry","expiresAt":"not-a-date"}`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`expiresAt`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// A rejected expiry must not leave a perpetual key behind.
				if _, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "name", "bad-expiry"); err == nil {
					t.Error("key record was created despite the rejected expiresAt")
				}
			},
		},
	}

	for _, s := range scenarios {
		s.Test(t)
	}
}

func TestRequireAPIKey(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:   "missing X-API-Key header",
			Method: http.MethodGet,
			URL:    "/functions",
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{`"Missing X-API-Key header"`},
		},
		{
			Name:   "invalid format - too short",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": "short",
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{`"Invalid API key format"`},
		},
		{
			Name:   "invalid format - wrong prefix",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": "xxx_abcdefghijklmnopqrstuvwxyz1234567890ab",
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{`"Invalid API key format"`},
		},
		{
			Name:   "unknown key - correct format but not in DB",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": "fbx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{`"Invalid API key"`},
		},
	}

	for _, s := range scenarios {
		s.Test(t)
	}

	// Test disabled, expired, and scoped keys separately since ApiScenario
	// doesn't allow setting headers dynamically from BeforeTestFunc.
	t.Run("disabled key", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKeyWithOptions(t, app, "disabled", nil, false, time.Time{})
		scenario := tests.ApiScenario{
			Name:   "disabled key returns 403",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{`"API key is disabled"`},
		}
		scenario.Test(t)
	})

	t.Run("expired key", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		pastTime := time.Now().Add(-24 * time.Hour)
		key := createTestAPIKeyWithOptions(t, app, "expired", nil, true, pastTime)
		scenario := tests.ApiScenario{
			Name:   "expired key returns 403",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{`"API key has expired"`},
		}
		scenario.Test(t)
	})

	t.Run("scoped key - wrong function", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		functionsDir, functions := setupTestFunctions(t, app, map[string]string{
			"echo":       "",
			"other-func": "",
		})
		key := createTestAPIKey(t, app, "scoped-echo-only", []string{functions["echo"].Id})
		scenario := tests.ApiScenario{
			Name:   "scoped key rejects unauthorized function",
			Method: http.MethodPost,
			URL:    "/invoke/other-func",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, functionsDir)
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{`API key is not authorized`},
		}
		scenario.Test(t)
	})

	t.Run("valid key - access granted", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "valid-key", []string{"*"})
		scenario := tests.ApiScenario{
			Name:   "valid key passes through to handler",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"functions"`},
		}
		scenario.Test(t)
	})

	t.Run("wildcard allowedFunctions", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "wildcard", []string{"*"})
		functionsDir, _ := setupTestFunctions(t, app, map[string]string{"any-func": ""})
		scenario := tests.ApiScenario{
			Name:   "wildcard key allows any function",
			Method: http.MethodPost,
			URL:    "/invoke/any-func",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, functionsDir)
			},
			// The key point is that the middleware passes and we don't get 401 or 403.
			ExpectedStatus:     200,
			NotExpectedContent: []string{`Missing X-API-Key`, `Invalid API key`, `not authorized`},
		}
		scenario.Test(t)
	})
}

func TestRequireAPIKey_EmptyAllowedFunctions(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	// Key with empty allowedFunctions should have access to all functions
	key := createTestAPIKey(t, app, "no-scope", nil)
	scenario := tests.ApiScenario{
		Name:   "nil allowedFunctions grants access to all",
		Method: http.MethodGet,
		URL:    "/functions",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, t.TempDir())
		},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"functions"`},
	}
	scenario.Test(t)
}

// --- Function scope reading ---

// setKeyScope overwrites allowedFunctions on an existing key record, the way a
// manual edit in the PocketBase admin would.
func setKeyScope(t testing.TB, app core.App, rawKey string, scope any) {
	t.Helper()
	record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", hashAPIKey(rawKey))
	if err != nil {
		t.Fatalf("failed to find API key record: %v", err)
	}
	record.Set("allowedFunctions", scope)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to set allowedFunctions to %v: %v", scope, err)
	}
}

func TestReadKeyScope(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	cases := []struct {
		name        string
		scope       any
		wantAllowed []string
		wantErr     bool
	}{
		{"Absent", nil, nil, false},
		{"JSON null", "null", nil, false},
		{"Empty list", []string{}, nil, false},
		{"Explicit list", []string{"a", "b"}, []string{"a", "b"}, false},
		{"Wildcard", []string{"*"}, []string{"*"}, false},
		// Valid JSON that is not a list of names: what a manual admin edit produces.
		{"JSON object", `{"echo":true}`, nil, true},
		{"JSON string", `"echo"`, nil, true},
		{"JSON number", "42", nil, true},
		{"List of numbers", `[1,2]`, nil, true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rawKey := createTestAPIKey(t, app, "scope-"+tt.name, nil)
			if tt.scope != nil {
				setKeyScope(t, app, rawKey, tt.scope)
			}
			record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", hashAPIKey(rawKey))
			if err != nil {
				t.Fatal(err)
			}

			allowed, err := readKeyScope(record)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("readKeyScope() = %v, want an error", allowed)
				}
				if allowed != nil {
					t.Errorf("readKeyScope() returned %v alongside its error, want nil", allowed)
				}
				return
			}
			if err != nil {
				t.Fatalf("readKeyScope() unexpected error: %v", err)
			}
			if len(allowed) != len(tt.wantAllowed) {
				t.Fatalf("readKeyScope() = %v, want %v", allowed, tt.wantAllowed)
			}
			for i := range allowed {
				if allowed[i] != tt.wantAllowed[i] {
					t.Errorf("readKeyScope()[%d] = %q, want %q", i, allowed[i], tt.wantAllowed[i])
				}
			}
		})
	}
}

// TestRequireAPIKey_UnreadableScope locks the fail-closed behaviour: a scope
// that cannot be read must deny, not grant access to everything.
func TestRequireAPIKey_UnreadableScope(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "broken-scope", []string{"echo"})
	setKeyScope(t, app, key, `{"echo":true}`)

	functionsDir, _ := setupTestFunctions(t, app, map[string]string{"echo": ""})
	scenario := tests.ApiScenario{
		Name:   "unreadable scope denies invocation",
		Method: http.MethodPost,
		URL:    "/invoke/echo",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  403,
		ExpectedContent: []string{`API key scope cannot be read`},
	}
	scenario.Test(t)
}

// TestRequireAPIKey_ScopedKeyAllowsItsOwnFunction is the positive half of the
// scope check; the rejection half is covered above.
func TestRequireAPIKey_ScopedKeyAllowsItsOwnFunction(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{"echo": ""})
	key := createTestAPIKey(t, app, "scoped-echo", []string{functions["echo"].Id})
	scenario := tests.ApiScenario{
		Name:   "scoped key invokes its authorized function",
		Method: http.MethodPost,
		URL:    "/invoke/echo",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:     200,
		NotExpectedContent: []string{`not authorized`, `scope cannot be read`},
	}
	scenario.Test(t)
}

// TestRequireAPIKey_EmptyListAllowsAny covers the explicit empty list, distinct
// from the absent field already covered by TestRequireAPIKey_EmptyAllowedFunctions.
func TestRequireAPIKey_EmptyListAllowsAny(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "empty-list", []string{})
	functionsDir, _ := setupTestFunctions(t, app, map[string]string{"echo": ""})
	scenario := tests.ApiScenario{
		Name:   "empty list grants access to any function",
		Method: http.MethodPost,
		URL:    "/invoke/echo",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:     200,
		NotExpectedContent: []string{`not authorized`, `scope cannot be read`},
	}
	scenario.Test(t)
}
