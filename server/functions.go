package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pocketbase/pocketbase/core"
)

const faasboxFunctionsCollection = "faasbox_functions"

// ensureFunctionsCollection creates the faasbox_functions collection if it doesn't exist,
// or migrates it by adding missing fields (script, packageJson).
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

// lookupFunctionEnv retrieves and decrypts the environment variables for a function.
// Returns a slice of "KEY=value" strings ready for cmd.Env, or nil if no env is configured.
func lookupFunctionEnv(app core.App, name string) []string {
	if encryptionKey == nil {
		return nil
	}

	record, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", name)
	if err != nil {
		return nil
	}

	encryptedEnv := record.GetString("env")
	if encryptedEnv == "" {
		return nil
	}

	plaintext, err := decrypt(encryptedEnv, encryptionKey)
	if err != nil {
		app.Logger().Error("faasbox: failed to decrypt env for function",
			"function", name, "error", err)
		return nil
	}

	var envMap map[string]string
	if err := json.Unmarshal(plaintext, &envMap); err != nil {
		app.Logger().Error("faasbox: failed to parse decrypted env as JSON",
			"function", name, "error", err)
		return nil
	}

	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}
	return envSlice
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

	// Skip if plainEnv is empty, null, or empty object
	s := string(jsonBytes)
	if s == "null" || s == `""` || s == "{}" || s == "" {
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
// Creates the function directory and writes index.ts (and package.json if non-empty).
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
		if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(script), 0o644); err != nil {
			return fmt.Errorf("failed to write index.ts for %s: %w", name, err)
		}
	}

	pkgPath := filepath.Join(dir, "package.json")
	pkg := record.GetString("packageJson")
	if pkg != "" {
		if err := os.WriteFile(pkgPath, []byte(pkg), 0o644); err != nil {
			return fmt.Errorf("failed to write package.json for %s: %w", name, err)
		}
	} else {
		os.Remove(pkgPath) // clean up stale file if packageJson was cleared
	}

	return nil
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

