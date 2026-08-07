package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
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

			err := syncRecordToDisk(record, tmpDir)
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
	if err := syncRecordToDisk(record, functionsDir); err != nil {
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
		if tf.Max != maxLockfileSize {
			t.Errorf("bunLock Max = %d, want the declared cap %d", tf.Max, maxLockfileSize)
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

// TestSyncRecordToDisk_Lockfile makes the lockfile an artefact of the record: it is
// written back like index.ts and package.json, and cleared when the record no
// longer carries one.
func TestSyncRecordToDisk_Lockfile(t *testing.T) {
	functionsDir := t.TempDir()
	record := newDetachedFunction("k9m2xq7p4wz1n3v", "pinned")
	record.Set("script", "console.log('hi')")
	record.Set("packageJson", `{"dependencies":{"dayjs":"^1.11.0"}}`)
	record.Set("bunLock", "resolved")

	if err := syncRecordToDisk(record, functionsDir); err != nil {
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
	if err := syncRecordToDisk(record, functionsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("bun.lock still on disk with an empty bunLock (stat error: %v)", err)
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
