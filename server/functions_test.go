package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestSyncRecordToDisk_Validation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "faasbox-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name          string
		functionName  string
		shouldSucceed bool
	}{
		{"Valid name", "my-function", true},
		{"Valid name with numbers", "func123", true},
		{"Empty name", "", false},
		{"Path traversal", "../traversal", false},
		{"Absolute path (if regex allowed it)", "/etc/passwd", false},
		{"Invalid characters", "func$name", false},
		{"Too long", "this-is-a-very-long-function-name-that-exceeds-sixty-four-characters-limit", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := core.NewRecord(core.NewBaseCollection(faasboxFunctionsCollection))
			record.Set("name", tt.functionName)
			record.Set("script", "console.log('test')")

			err := syncRecordToDisk(record, tmpDir)
			if err != nil {
				t.Errorf("syncRecordToDisk() unexpected error: %v", err)
			}

			dir := filepath.Join(tmpDir, tt.functionName)
			_, err = os.Stat(dir)
			exists := !os.IsNotExist(err)

			if tt.shouldSucceed && !exists {
				t.Errorf("expected directory %s to exist, but it doesn't", dir)
			}
			if !tt.shouldSucceed && exists && tt.functionName != "" {
				t.Errorf("expected directory %s NOT to exist, but it does", dir)
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
		name         string
		functionName string
		setupDir     bool
	}{
		{"Valid name", "to-delete", true},
		{"Path traversal attempt", "..", false}, // In theory would delete tmpDir itself if not validated
		{"Path traversal attempt with child", "../other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupDir {
				if err := os.MkdirAll(filepath.Join(tmpDir, tt.functionName), 0755); err != nil {
					t.Fatal(err)
				}
			}

			record := core.NewRecord(core.NewBaseCollection(faasboxFunctionsCollection))
			record.Set("name", tt.functionName)

			err := deleteRecordFromDisk(record, tmpDir)
			if err != nil {
				t.Errorf("deleteRecordFromDisk() unexpected error: %v", err)
			}

			// Ensure otherDir still exists
			if _, err := os.Stat(otherDir); os.IsNotExist(err) {
				t.Errorf("CRITICAL: deleteRecordFromDisk deleted unrelated directory %s", otherDir)
			}

			if tt.setupDir {
				if _, err := os.Stat(filepath.Join(tmpDir, tt.functionName)); !os.IsNotExist(err) {
					t.Errorf("expected directory %s to be deleted, but it still exists", tt.functionName)
				}
			}
		})
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

	if _, err := os.Stat(filepath.Join(chosenDir, "probe", "index.ts")); err != nil {
		t.Errorf("function not written to the chosen directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultDir, "probe")); err == nil {
		t.Error("function written to the default directory: the hook froze the flag's default")
	}
}
