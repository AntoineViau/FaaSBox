package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/core"
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
