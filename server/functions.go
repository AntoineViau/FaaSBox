package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pocketbase/pocketbase/core"
)

const faasboxFunctionsCollection = "faasbox_functions"

// ensureFunctionsCollection creates the faasbox_functions collection if it doesn't exist,
// or migrates it by adding missing fields (script, packageJson, depsStatus, depsError,
// bunLock).
func ensureFunctionsCollection(app core.App) error {
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		// Collection doesn't exist — create it with all fields
		col = core.NewBaseCollection(faasboxFunctionsCollection)
		col.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.TextField{Name: "env", Hidden: true},
			&core.JSONField{Name: "plainEnv"},
			&core.TextField{Name: "script"},
			&core.TextField{Name: "packageJson"},
			newDepsStatusField(),
			newDepsErrorField(),
			newBunLockField(),
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		col.AddIndex("idx_faasbox_functions_name", true, "name", "")
		return app.Save(col)
	}

	// Collection exists — add missing fields if needed
	needsSave := false
	if col.Fields.GetByName("script") == nil {
		col.Fields.Add(&core.TextField{Name: "script"})
		needsSave = true
	}
	if col.Fields.GetByName("packageJson") == nil {
		col.Fields.Add(&core.TextField{Name: "packageJson"})
		needsSave = true
	}
	if col.Fields.GetByName("depsStatus") == nil {
		col.Fields.Add(newDepsStatusField())
		needsSave = true
	}
	if col.Fields.GetByName("depsError") == nil {
		col.Fields.Add(newDepsErrorField())
		needsSave = true
	}
	if col.Fields.GetByName("bunLock") == nil {
		col.Fields.Add(newBunLockField())
		needsSave = true
	}
	if field := col.Fields.GetByName("env"); field != nil {
		if tf, ok := field.(*core.TextField); ok && !tf.Hidden {
			tf.Hidden = true
			needsSave = true
		}
	}
	if needsSave {
		return app.Save(col)
	}
	return nil
}

// decryptFunctionEnv decrypts the environment variables of a function record.
// A function without secrets yields an empty map, which is not an error: only a
// key that is missing, a payload that will not decrypt, or plaintext that is not
// a JSON object are. Callers must not confuse the two — reading an empty map out
// of a failure would let an editor overwrite secrets it could not display.
func decryptFunctionEnv(record *core.Record) (map[string]string, error) {
	encryptedEnv := record.GetString("env")
	if encryptedEnv == "" {
		return map[string]string{}, nil
	}

	if encryptionKey == nil {
		return nil, fmt.Errorf("FAASBOX_ENCRYPTION_KEY is not configured")
	}

	plaintext, err := decrypt(encryptedEnv, encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt env: %w", err)
	}

	var envMap map[string]string
	if err := json.Unmarshal(plaintext, &envMap); err != nil {
		return nil, fmt.Errorf("parse decrypted env: %w", err)
	}

	return envMap, nil
}

// lookupFunctionEnv retrieves and decrypts the environment variables for a function.
// Returns a slice of "KEY=value" strings ready for cmd.Env, or nil if no env is configured.
// A failure is logged and degrades to nil: an invocation runs without secrets
// rather than not running at all.
func lookupFunctionEnv(app core.App, name string) []string {
	record, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", name)
	if err != nil {
		return nil
	}

	envMap, err := decryptFunctionEnv(record)
	if err != nil {
		app.Logger().Error("faasbox: failed to read env for function",
			"function", name, "error", err)
		return nil
	}

	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}
	return envSlice
}

// functionEnvHandler returns the decrypted environment of a function as a JSON
// object. Superuser only: it is the one path that turns a stored secret back
// into plaintext, and the editor needs it to show what is set before replacing
// it — saving rewrites the whole object, so editing blind silently drops the
// variables the user could not see.
func functionEnvHandler(e *core.RequestEvent) error {
	name := e.Request.PathValue("name")
	if !validName.MatchString(name) || len(name) > 64 {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid function name"})
	}

	record, err := e.App.FindFirstRecordByData(faasboxFunctionsCollection, "name", name)
	if err != nil {
		return e.JSON(http.StatusNotFound, map[string]string{"error": "Function not found"})
	}

	envMap, err := decryptFunctionEnv(record)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to read env for function",
			"function", name, "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to read the environment of this function",
		})
	}

	return e.JSON(http.StatusOK, envMap)
}

// encryptPlainEnvHook is a PocketBase hook that encrypts the plainEnv field
// into env and clears plainEnv before saving a faasbox_functions record.
func encryptPlainEnvHook(e *core.RecordEvent) error {
	raw := e.Record.GetRaw("plainEnv")
	if raw == nil {
		return e.Next()
	}

	// Marshal raw value to JSON bytes to check if it's non-empty
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return e.Next()
	}

	// A record whose plainEnv was never submitted still carries the null left by
	// the previous save, so null means "untouched" and must preserve env. An
	// explicit {} is the opposite: the editor sends it to remove every variable.
	s := string(jsonBytes)
	if s == "null" || s == `""` || s == "" {
		return e.Next()
	}
	if s == "{}" {
		e.Record.Set("env", "")
		e.Record.Set("plainEnv", nil)
		return e.Next()
	}

	if encryptionKey == nil {
		return fmt.Errorf("cannot save encrypted env: FAASBOX_ENCRYPTION_KEY is not configured")
	}

	encrypted, err := encrypt(jsonBytes, encryptionKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt env: %w", err)
	}

	e.Record.Set("env", encrypted)
	e.Record.Set("plainEnv", nil)

	return e.Next()
}

// syncRecordToDisk writes a single faasbox_functions record to disk.
// Creates the function directory and writes index.ts (and package.json and
// bun.lock if non-empty).
func syncRecordToDisk(record *core.Record, functionsDir string) error {
	name := record.GetString("name")
	if name == "" || !validName.MatchString(name) || len(name) > 64 {
		return nil
	}

	dir := filepath.Join(functionsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	script := record.GetString("script")
	if script != "" {
		if err := writeIfChanged(filepath.Join(dir, "index.ts"), []byte(script), 0o644); err != nil {
			return fmt.Errorf("failed to write index.ts for %s: %w", name, err)
		}
	}

	pkgPath := filepath.Join(dir, "package.json")
	pkg := record.GetString("packageJson")
	if pkg != "" {
		if err := writeIfChanged(pkgPath, []byte(pkg), 0o644); err != nil {
			return fmt.Errorf("failed to write package.json for %s: %w", name, err)
		}
	} else {
		os.Remove(pkgPath) // clean up stale file if packageJson was cleared
	}

	// The lockfile is restored like the rest: it is an artefact of the record, not
	// of the disk, and that is what makes the pinning survive a rebuilt filesystem.
	lockPath := filepath.Join(dir, "bun.lock")
	lock := record.GetString("bunLock")
	if lock != "" {
		if err := writeIfChanged(lockPath, []byte(lock), 0o644); err != nil {
			return fmt.Errorf("failed to write bun.lock for %s: %w", name, err)
		}
	} else {
		os.Remove(lockPath) // no lockfile on the record: do not leave a stale one
	}

	return nil
}

// syncFunctionRecord is the record handler shared by create and update: mirror
// the saved record to disk, then install its dependencies in the background. A
// disk sync failure skips the install — the spec on disk is then unknown, and
// installing against it would be guesswork.
//
// It takes functionsDir as a plain argument rather than returning a closure
// built around it. A factory called while the hooks are registered captures the
// flag's default, since the command line is only parsed later, and every save
// would then write to ./functions while /invoke read the directory the flag
// actually names.
func syncFunctionRecord(ctx context.Context, e *core.RecordEvent, functionsDir string) error {
	if err := syncRecordToDisk(e.Record, functionsDir); err != nil {
		e.App.Logger().Error("faasbox: failed to sync function to disk",
			"function", e.Record.GetString("name"), "error", err)
		return e.Next()
	}
	scheduleDepsInstall(ctx, e.App, e.Record, functionsDir)
	return e.Next()
}

// writeIfChanged writes data only when the content differs. Rewriting an identical
// file would refresh its mtime and churn the disk (and Litestream replication) for
// nothing.
func writeIfChanged(path string, data []byte, perm os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	return os.WriteFile(path, data, perm)
}

// deleteRecordFromDisk removes the function directory from disk.
func deleteRecordFromDisk(record *core.Record, functionsDir string) error {
	name := record.GetString("name")
	if name == "" || !validName.MatchString(name) || len(name) > 64 {
		return nil
	}
	dir := filepath.Join(functionsDir, name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to remove directory %s: %w", dir, err)
	}
	return nil
}

// syncDiskFromDB restores all functions from the database to disk.
// Called at startup to recreate files after a container restart.
func syncDiskFromDB(app core.App, functionsDir string) {
	records, err := app.FindAllRecords(faasboxFunctionsCollection)
	if err != nil {
		app.Logger().Error("faasbox: failed to load functions from DB", "error", err)
		return
	}

	for _, r := range records {
		if err := syncRecordToDisk(r, functionsDir); err != nil {
			app.Logger().Error("faasbox: failed to sync function to disk",
				"function", r.GetString("name"), "error", err)
		}
	}

	app.Logger().Info("faasbox: synced functions from DB to disk", "count", len(records))
}
