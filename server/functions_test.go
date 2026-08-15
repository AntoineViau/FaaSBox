package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/router"
)

// newDetachedFunction builds an unsaved record carrying the id we want, which is
// what names its directory now. A record straight out of core.NewRecord has no id
// until it is saved, and these tests are about the path, not the persistence.
func newDetachedFunction(id, name string) *core.Record {
	record := core.NewRecord(core.NewBaseCollection(faasboxFunctionsCollection))
	record.Id = id
	record.Set("name", name)
	return record
}

// TestSyncRecordToDisk_Validation guards the identifier that builds the path.
// That is the id now, not the name: a name is free to be anything the editor
// accepts, and it no longer reaches the filesystem.
func TestSyncRecordToDisk_Validation(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	tmpDir, err := os.MkdirTemp("", "faasbox-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name          string
		functionId    string
		shouldSucceed bool
	}{
		{"Generated id", "k9m2xq7p4wz1n3v", true},
		{"Id-shaped value", "func123", true},
		{"Empty id", "", false},
		{"Path traversal", "../traversal", false},
		{"Absolute path (if regex allowed it)", "/etc/passwd", false},
		{"Invalid characters", "func$name", false},
		{"Too long", "this-is-a-very-long-identifier-that-exceeds-sixty-four-characters-limit", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := newDetachedFunction(tt.functionId, "my-function")
			record.Set("script", "console.log('test')")

			err := syncRecordToDisk(app, record, tmpDir)
			if err != nil {
				t.Errorf("syncRecordToDisk() unexpected error: %v", err)
			}

			dir := filepath.Join(tmpDir, tt.functionId)
			_, err = os.Stat(dir)
			exists := !os.IsNotExist(err)

			if tt.shouldSucceed && !exists {
				t.Errorf("expected directory %s to exist, but it doesn't", dir)
			}
			if !tt.shouldSucceed && exists && tt.functionId != "" {
				t.Errorf("expected directory %s NOT to exist, but it does", dir)
			}

			// The name never names anything on disk any more.
			if _, err := os.Stat(filepath.Join(tmpDir, "my-function")); err == nil {
				t.Error("a directory was created under the function name, not its id")
			}
		})
	}
}

func TestDeleteRecordFromDisk_Validation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "faasbox-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a dummy file that shouldn't be deleted
	otherDir := filepath.Join(tmpDir, "keep-me")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		functionId string
		setupDir   bool
	}{
		{"Generated id", "k9m2xq7p4wz1n3v", true},
		{"Path traversal attempt", "..", false}, // In theory would delete tmpDir itself if not validated
		{"Path traversal attempt with child", "../other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupDir {
				if err := os.MkdirAll(filepath.Join(tmpDir, tt.functionId), 0755); err != nil {
					t.Fatal(err)
				}
			}

			record := newDetachedFunction(tt.functionId, "to-delete")

			err := deleteRecordFromDisk(record, tmpDir)
			if err != nil {
				t.Errorf("deleteRecordFromDisk() unexpected error: %v", err)
			}

			// Ensure otherDir still exists
			if _, err := os.Stat(otherDir); os.IsNotExist(err) {
				t.Errorf("CRITICAL: deleteRecordFromDisk deleted unrelated directory %s", otherDir)
			}

			if tt.setupDir {
				if _, err := os.Stat(filepath.Join(tmpDir, tt.functionId)); !os.IsNotExist(err) {
					t.Errorf("expected directory %s to be deleted, but it still exists", tt.functionId)
				}
			}
		})
	}
}

// TestSyncRecordToDisk_RenameLeavesTheDirectoryAlone is the whole point of naming
// the directory by the id: a rename moves nothing, so node_modules survives it and
// no reinstall is triggered.
func TestSyncRecordToDisk_RenameLeavesTheDirectoryAlone(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "before",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)

	// Stand in for what an install left behind, plus its fingerprint.
	modules := filepath.Join(functionsDir, record.Id, "node_modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := depsHash(filepath.Join(functionsDir, record.Id))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modules, depsHashFile), []byte(hash), 0o644); err != nil {
		t.Fatal(err)
	}

	record.Set("name", "after")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(modules); err != nil {
		t.Errorf("node_modules did not survive the rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(functionsDir, "after")); err == nil {
		t.Error("a second directory appeared under the new name")
	}
	// The install fingerprint still matches, so nothing would be reinstalled.
	if !depsUpToDate(filepath.Join(functionsDir, record.Id)) {
		t.Error("the rename invalidated the dependency fingerprint")
	}
}

// TestSyncFunctionRecord_ReadsDirAtEventTime pins down the wiring of the
// create and update hooks, not the sync itself.
//
// The hooks are bound while main() runs, before the command line is parsed, so
// functionsDir still holds the flag's default at that point. Binding a closure
// built around its value there — as a factory would — freezes the default: every
// save would write to ./functions while /invoke, the boot-time restore and the
// delete hook all read the directory --functionsDir actually names, and an
// invocation would answer "function not found" for a record that plainly exists.
//
// The test therefore binds the hook the way main.go does, then changes the
// variable, the way flag parsing does, and checks the later value is the one used.
func TestSyncFunctionRecord_ReadsDirAtEventTime(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatal(err)
	}

	// Stands in for the flag's default, the value present when the hook is bound.
	defaultDir := t.TempDir()
	functionsDir := defaultDir

	app.OnRecordAfterCreateSuccess(faasboxFunctionsCollection).BindFunc(func(e *core.RecordEvent) error {
		return syncFunctionRecord(context.Background(), e, functionsDir)
	})

	// Stands in for --functionsDir, written once the command line is parsed.
	chosenDir := t.TempDir()
	functionsDir = chosenDir

	collection, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("name", "probe")
	record.Set("script", "console.log(1)")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(chosenDir, record.Id, "index.ts")); err != nil {
		t.Errorf("function not written to the chosen directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultDir, record.Id)); err == nil {
		t.Error("function written to the default directory: the hook froze the flag's default")
	}
}

// TestEnsureFunctionsCollection_BunLockField covers the field on both paths of the
// idempotent collection setup, and the explicit Max it must declare: a TextField
// without one rejects the whole record past 5000 runes, and a lockfile goes past
// that on the first real dependency.
func TestEnsureFunctionsCollection_BunLockField(t *testing.T) {
	assertField := func(t *testing.T, app core.App) {
		t.Helper()
		col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}
		field := col.Fields.GetByName("bunLock")
		if field == nil {
			t.Fatal("the collection carries no bunLock field")
		}
		tf, ok := field.(*core.TextField)
		if !ok {
			t.Fatalf("bunLock is a %T, want a *core.TextField", field)
		}
		// The column stores the sealed lockfile, so what it declares is the
		// cap plus what the encryption adds around it.
		if want := cipherMax(maxLockfileSize); tf.Max != want {
			t.Errorf("bunLock Max = %d, want the declared cap %d", tf.Max, want)
		}
	}

	t.Run("at creation", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()

		if err := ensureFunctionsCollection(app); err != nil {
			t.Fatal(err)
		}
		assertField(t, app)
	})

	t.Run("on an existing collection", func(t *testing.T) {
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
		col.Fields.RemoveByName("bunLock")
		if err := app.Save(col); err != nil {
			t.Fatal(err)
		}

		if err := ensureFunctionsCollection(app); err != nil {
			t.Fatal(err)
		}
		assertField(t, app)
	})
}

// TestEnsureFunctionsCollection_SourceSize pins the declared size of the fields
// carrying what the user wrote — the script, the manifest, and the encrypted
// environment. Undeclared, they take PocketBase's 5000-rune default, which turns
// away an ordinary function and a single long private key.
func TestEnsureFunctionsCollection_SourceSize(t *testing.T) {
	// The two source columns are encrypted at rest, so what they declare is the
	// size of the sealed value; env has measured its own ciphertext all along.
	wanted := map[string]int{
		"script":      cipherMax(maxSourceSize),
		"packageJson": cipherMax(maxSourceSize),
		"env":         maxEnvSize,
	}
	assertFields := func(t *testing.T, app core.App) {
		t.Helper()
		col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}
		for name, max := range wanted {
			field, ok := col.Fields.GetByName(name).(*core.TextField)
			if !ok {
				t.Fatalf("%s is a %T, want a *core.TextField", name, col.Fields.GetByName(name))
			}
			if field.Max != max {
				t.Errorf("%s Max = %d, want the declared cap %d", name, field.Max, max)
			}
		}
	}

	t.Run("at creation", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()

		if err := ensureFunctionsCollection(app); err != nil {
			t.Fatal(err)
		}
		assertFields(t, app)
	})

	t.Run("realigned on a collection created by an earlier version", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()

		if err := ensureFunctionsCollection(app); err != nil {
			t.Fatal(err)
		}
		// Put the fields back the way a version without the cap left them: no
		// declared Max, hence the 5000-rune default.
		col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}
		for name := range wanted {
			col.Fields.GetByName(name).(*core.TextField).Max = 0
		}
		if err := app.Save(col); err != nil {
			t.Fatal(err)
		}

		if err := ensureFunctionsCollection(app); err != nil {
			t.Fatal(err)
		}
		assertFields(t, app)
	})

	t.Run("a second call saves nothing", func(t *testing.T) {
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
		before := col.Updated

		// Recomputing the wanted size differently on each path would make the
		// comparison miss and re-save the collection at every boot.
		if err := ensureFunctionsCollection(app); err != nil {
			t.Fatal(err)
		}
		col, err = app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}
		if col.Updated != before {
			t.Error("an idempotent call re-saved the collection")
		}
	})
}

// bindFunctionNameHook wires the name guard the way main.go does. A test app
// starts with no hook bound.
func bindFunctionNameHook(app core.App) {
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(validateFunctionNameHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(validateFunctionNameHook)
}

// bindFunctionSizeHook does the same for the source cap, which is a hook now
// that the column measures the sealed value rather than the plaintext.
func bindFunctionSizeHook(app core.App) {
	app.OnRecordCreate(faasboxFunctionsCollection).BindFunc(validateFunctionSizeHook)
	app.OnRecordUpdate(faasboxFunctionsCollection).BindFunc(validateFunctionSizeHook)
}

// newFunctionApp is a test app carrying the functions collection, with the name
// guard bound unless the caller asks to bind it later — which is how a record
// already stored under an invalid name is staged.
func newFunctionApp(t *testing.T, bindHook bool) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatal(err)
	}
	if bindHook {
		bindFunctionNameHook(app)
	}
	return app
}

// newFunctionRecord builds an unsaved record under the given name.
func newFunctionRecord(t *testing.T, app core.App, name string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("script", "console.log(1)")
	return record
}

// assertNameRefused checks the shape of the refusal as much as its existence: an
// ordinary error would be swallowed by firstApiError and replaced by a generic
// "Failed to create record", leaving the editor with nothing to show.
func assertNameRefused(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the name was accepted")
	}
	var apiErr *router.ApiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("refusal is a %T, want a *router.ApiError", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("refusal status = %d, want 400", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "Invalid function name") ||
		!strings.Contains(apiErr.Message, "letters, digits and hyphens only") {
		t.Errorf("refusal message = %q, it names neither the name nor the rule", apiErr.Message)
	}
}

// TestValidateFunctionNameHook covers the guard the naming rule was missing. It
// was declared as a product rule and enforced nowhere it applies: a name outside
// it saved and ran, but /invoke/{name} answered 400 before resolving anything,
// and a name carrying a NUL failed every single invocation.
func TestValidateFunctionNameHook(t *testing.T) {
	invalid := []struct {
		label string
		name  string
	}{
		{"underscore", "my_function"},
		{"space", "mon nom"},
		{"empty", ""},
		{"leading hyphen", "-my-function"},
		{"trailing hyphen", "my-function-"},
		{"path traversal", "../escape"},
		{"over 64 characters", strings.Repeat("a", 65)},
		{"NUL byte", "bad\x00name"},
	}

	t.Run("refuses an invalid name at creation", func(t *testing.T) {
		for _, tc := range invalid {
			t.Run(tc.label, func(t *testing.T) {
				app := newFunctionApp(t, true)
				assertNameRefused(t, app.Save(newFunctionRecord(t, app, tc.name)))

				records, err := app.FindAllRecords(faasboxFunctionsCollection)
				if err != nil {
					t.Fatal(err)
				}
				if len(records) != 0 {
					t.Errorf("%d record(s) written despite the refused name", len(records))
				}
			})
		}
	})

	t.Run("refuses a rename to an invalid name", func(t *testing.T) {
		for _, tc := range invalid {
			t.Run(tc.label, func(t *testing.T) {
				app := newFunctionApp(t, true)
				record := newFunctionRecord(t, app, "before")
				if err := app.Save(record); err != nil {
					t.Fatal(err)
				}

				record.Set("name", tc.name)
				assertNameRefused(t, app.Save(record))

				stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
				if err != nil {
					t.Fatal(err)
				}
				if got := stored.GetString("name"); got != "before" {
					t.Errorf("stored name = %q, want the old one kept", got)
				}
			})
		}
	})

	t.Run("a valid name goes through, at creation and at update", func(t *testing.T) {
		for _, name := range []string{"my-function", "hello123", "a", strings.Repeat("a", 64)} {
			t.Run(name[:min(len(name), 12)], func(t *testing.T) {
				app := newFunctionApp(t, true)
				record := newFunctionRecord(t, app, name)
				if err := app.Save(record); err != nil {
					t.Fatalf("valid name refused at creation: %v", err)
				}
				record.Set("script", "console.log(2)")
				if err := app.Save(record); err != nil {
					t.Fatalf("valid name refused at update: %v", err)
				}
			})
		}
	})

	// The guard applies to updates too, so a record stored under an invalid name
	// stays frozen until its name is fixed. That correction is the only way out,
	// and the name field is editable on screen — this pins it down.
	t.Run("an update that fixes an invalid name goes through", func(t *testing.T) {
		app := newFunctionApp(t, false)
		record := newFunctionRecord(t, app, "my_function")
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		bindFunctionNameHook(app)

		// Saving it as it stands is refused: that is the frozen state.
		record.Set("script", "console.log(2)")
		assertNameRefused(t, app.Save(record))

		record.Set("name", "my-function")
		if err := app.Save(record); err != nil {
			t.Fatalf("the correction was refused: %v", err)
		}
		stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
		if err != nil {
			t.Fatal(err)
		}
		if got := stored.GetString("name"); got != "my-function" {
			t.Errorf("stored name = %q, want the corrected one", got)
		}
	})
}

// TestValidateFunctionNameHook_OverHTTP is the other half: what reaches the wire.
// The editor writes through the PocketBase collections API, whose endpoints pass
// a hook failure through firstApiError — an ordinary error would be replaced by a
// generic message, and the editor would have nothing but "Failed to save".
func TestValidateFunctionNameHook_OverHTTP(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:   "invalid name refused with a message the client can display",
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxFunctionsCollection + "/records",
			Body:   strings.NewReader(`{"name":"a_b","script":"console.log(1)"}`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				bindFunctionNameHook(app)
			},
			ExpectedStatus: 400,
			ExpectedContent: []string{
				`Invalid function name \"a_b\"`,
				`letters, digits and hyphens only`,
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "nameHash", blindIndex("a_b")); err == nil {
					t.Error("record was created despite the refused name")
				}
			},
		},
		{
			Name:   "valid name goes through",
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxFunctionsCollection + "/records",
			Body:   strings.NewReader(`{"name":"a-b","script":"console.log(1)"}`),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				bindFunctionNameHook(app)
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"a-b"`},
		},
	}

	for _, s := range scenarios {
		s.Test(t)
	}
}

// TestSyncRecordToDisk_Lockfile makes the lockfile an artefact of the record: it is
// written back like index.ts and package.json, and cleared when the record no
// longer carries one.
func TestSyncRecordToDisk_Lockfile(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := newDetachedFunction("k9m2xq7p4wz1n3v", "pinned")
	record.Set("script", "console.log('hi')")
	record.Set("packageJson", `{"dependencies":{"dayjs":"^1.11.0"}}`)
	record.Set("bunLock", "resolved")

	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(functionsDir, record.Id, "bun.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("bun.lock not restored: %v", err)
	}
	if string(data) != "resolved" {
		t.Errorf("bun.lock = %q, want %q", data, "resolved")
	}

	// The dependencies were dropped: a leftover lockfile would pin what no longer
	// exists, and would still count towards the install hash.
	record.Set("bunLock", "")
	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("bun.lock still on disk with an empty bunLock (stat error: %v)", err)
	}
}

// TestSyncRecordToDisk_ClearedScript puts the script under the same rule as the two
// other artefacts: an empty field removes its file. What it must not remove is the
// directory, which still carries a package.json, a lockfile and a node_modules that
// clearing a script has nothing to do with.
func TestSyncRecordToDisk_ClearedScript(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := newDetachedFunction("k9m2xq7p4wz1n3v", "cleared")
	record.Set("script", "console.log('hi')")
	record.Set("packageJson", `{"dependencies":{"dayjs":"^1.11.0"}}`)
	record.Set("bunLock", "resolved")

	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(functionsDir, record.Id)
	scriptPath := filepath.Join(dir, "index.ts")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("index.ts not written: %v", err)
	}

	// Stand in for what an install left behind.
	modules := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}

	record.Set("script", "")
	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("index.ts still on disk with an empty script (stat error: %v)", err)
	}
	// The other two are untouched: only the field that was cleared loses its file.
	for _, name := range []string{"package.json", "bun.lock", "node_modules"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s did not survive the cleared script: %v", name, err)
		}
	}

	// Writing a script again puts the function back where it was.
	record.Set("script", "console.log('back')")
	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("index.ts not restored: %v", err)
	}
	if string(data) != "console.log('back')" {
		t.Errorf("index.ts = %q, want %q", data, "console.log('back')")
	}
}

// TestClearedScript_IsNoLongerServed is what the removal is for: the listing and the
// invocation both read the disk, so a stale index.ts made the record and what runs
// disagree — and the disagreement only surfaced at the next restart on a fresh
// filesystem, where nothing gets written for an empty script.
func TestClearedScript_IsNoLongerServed(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	saveTestFunction(t, app, functionsDir, "echo", defaultTestScript, "")

	listed, err := listFunctions(app, functionsDir, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listFunctions returned %d entries before clearing, want 1", len(listed))
	}

	// Same record, no script left on it.
	saveTestFunction(t, app, functionsDir, "echo", "", "")

	listed, err = listFunctions(app, functionsDir, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Errorf("listFunctions returned %d entries for a cleared script, want 0", len(listed))
	}
	invokeOverHTTP(t, app, functionsDir, "echo", 404, []string{"not found"})

	// The container is thrown away: the boot-time restore must reach the same
	// state, not resurrect a script the record no longer carries.
	freshDir := t.TempDir()
	syncDiskFromDB(app, freshDir)

	listed, err = listFunctions(app, freshDir, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Errorf("listFunctions returned %d entries after a restart, want 0", len(listed))
	}
	invokeOverHTTP(t, app, freshDir, "echo", 404, []string{"not found"})

	// And a script written again is served again, on the rebuilt filesystem.
	saveTestFunction(t, app, freshDir, "echo", defaultTestScript, "")
	listed, err = listFunctions(app, freshDir, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Errorf("listFunctions returned %d entries once the script was written back, want 1", len(listed))
	}
}

// TestSyncDiskFromDB_RestoresLockfile is the point of persisting it: a restart on a
// fresh filesystem must find the pinning again, not re-resolve every version range.
func TestSyncDiskFromDB_RestoresLockfile(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "survivor",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)
	const lock = `{"lockfileVersion":1,"packages":{"dayjs":["dayjs@1.11.13"]}}`
	setBunLock(app, record.Id, lock)

	// The container is thrown away and comes back with nothing on disk.
	freshDir := t.TempDir()
	syncDiskFromDB(app, freshDir)

	data, err := os.ReadFile(filepath.Join(freshDir, record.Id, "bun.lock"))
	if err != nil {
		t.Fatalf("bun.lock not restored on a fresh filesystem: %v", err)
	}
	if string(data) != lock {
		t.Errorf("bun.lock = %q, want %q", data, lock)
	}
}
