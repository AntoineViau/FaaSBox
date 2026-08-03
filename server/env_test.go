package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// withEncryptionKey installs a throwaway AES-256 key for the duration of a test.
// encryptionKey is package state, so it has to be restored.
func withEncryptionKey(t testing.TB) {
	t.Helper()
	previous := encryptionKey
	encryptionKey = make([]byte, 32)
	for i := range encryptionKey {
		encryptionKey[i] = byte(i)
	}
	t.Cleanup(func() { encryptionKey = previous })
}

// bindEnvHook wires encryptPlainEnvHook the way main.go does, since a test app
// starts with no hooks bound.
func bindEnvHook(app core.App) {
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(encryptPlainEnvHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(encryptPlainEnvHook)
}

// saveEnvFixture creates a function carrying two encrypted variables.
func saveEnvFixture(t testing.TB, app core.App, name string) *core.Record {
	t.Helper()
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatalf("failed to create functions collection: %v", err)
	}
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("script", "console.log('{}')")
	record.Set("plainEnv", map[string]string{"MY_SECRET": "s3cr3t", "API_TOKEN": "t0ken"})
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to save function %q: %v", name, err)
	}
	return record
}

func TestEncryptPlainEnvHook(t *testing.T) {
	t.Run("encrypts and clears plainEnv", func(t *testing.T) {
		withEncryptionKey(t)
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		bindEnvHook(app)

		record := saveEnvFixture(t, app, "envfn")

		if record.GetString("env") == "" {
			t.Fatal("env was not encrypted")
		}
		if raw := record.GetRaw("plainEnv"); raw != nil {
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != "null" {
				t.Fatalf("plainEnv was not cleared, got %s", encoded)
			}
		}

		envMap, err := decryptFunctionEnv(record)
		if err != nil {
			t.Fatalf("decryptFunctionEnv() error: %v", err)
		}
		if envMap["MY_SECRET"] != "s3cr3t" || envMap["API_TOKEN"] != "t0ken" {
			t.Fatalf("round trip lost values: %v", envMap)
		}
	})

	t.Run("an update that omits plainEnv preserves env", func(t *testing.T) {
		withEncryptionKey(t)
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		bindEnvHook(app)

		record := saveEnvFixture(t, app, "envfn")
		encrypted := record.GetString("env")

		// What a PATCH on the script alone does: plainEnv keeps the null left by
		// the previous save.
		record.Set("script", "console.log('changed')")
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}

		if record.GetString("env") != encrypted {
			t.Fatal("env was overwritten by an update that never mentioned plainEnv")
		}
	})

	t.Run("an explicit empty object clears env", func(t *testing.T) {
		withEncryptionKey(t)
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		bindEnvHook(app)

		record := saveEnvFixture(t, app, "envfn")

		record.Set("plainEnv", map[string]string{})
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}

		if record.GetString("env") != "" {
			t.Fatal("env survived an explicit empty object")
		}
		envMap, err := decryptFunctionEnv(record)
		if err != nil {
			t.Fatalf("decryptFunctionEnv() error: %v", err)
		}
		if len(envMap) != 0 {
			t.Fatalf("expected no variable left, got %v", envMap)
		}
	})
}

func TestDecryptFunctionEnv_NoEnv(t *testing.T) {
	withEncryptionKey(t)
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatal(err)
	}
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("name", "plainfn")

	envMap, err := decryptFunctionEnv(record)
	if err != nil {
		t.Fatalf("a function without secrets must not be an error, got %v", err)
	}
	if len(envMap) != 0 {
		t.Fatalf("expected an empty map, got %v", envMap)
	}
}

func TestDecryptFunctionEnv_Corrupted(t *testing.T) {
	withEncryptionKey(t)
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatal(err)
	}
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("name", "brokenfn")
	record.Set("env", "not-a-valid-ciphertext")

	// Must surface as an error: reading it as "no variable" would let the editor
	// overwrite secrets it never displayed.
	if _, err := decryptFunctionEnv(record); err == nil {
		t.Fatal("expected an error on an undecryptable env")
	}
}

func TestFunctionEnvHandler(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:   "success with superuser auth",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/envfn/env",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withEncryptionKey(t)
				setupFaaSCollections(t, app)
				bindEnvHook(app)
				saveEnvFixture(t, app, "envfn")
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"MY_SECRET":"s3cr3t"`,
				`"API_TOKEN":"t0ken"`,
			},
		},
		{
			Name:   "unauthorized without auth",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/envfn/env",
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withEncryptionKey(t)
				setupFaaSCollections(t, app)
				bindEnvHook(app)
				saveEnvFixture(t, app, "envfn")
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{`"data"`},
		},
		{
			Name:   "empty object for a function without secrets",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/plainfn/env",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withEncryptionKey(t)
				setupFaaSCollections(t, app)
				saveTestFunction(t, app, t.TempDir(), "plainfn", "console.log('{}')", "")
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`{}`},
		},
		{
			Name:   "unknown function",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/missing/env",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withEncryptionKey(t)
				setupFaaSCollections(t, app)
				if err := ensureFunctionsCollection(app); err != nil {
					t.Fatal(err)
				}
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  404,
			ExpectedContent: []string{`"Function not found"`},
		},
		{
			Name:   "invalid function name",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/bad$name/env",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withEncryptionKey(t)
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`"Invalid function name"`},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
