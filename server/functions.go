package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

const faasboxFunctionsCollection = "faasbox_functions"

// maxSourceSize bounds the two fields that carry what the user actually wrote:
// the script and the package.json manifest.
//
// The unit is the rune, not the byte: a million runes is at least a megabyte of
// source, and more when it is not ASCII.
//
// **It is no longer the column that enforces it.** These two fields are
// encrypted at rest, so what a TextField measures is the sealed value — larger
// than the plaintext, and by a factor that depends on the encoding. The declared
// size is therefore cipherMax of this cap, wide enough never to be the binding
// constraint, and the product limit is checked on the plaintext by
// validateFunctionSizeHook. Without that check the announced cap would simply
// cease to exist.
const maxSourceSize = 1 << 20 // 1,048,576 characters

// maxSampleSize bounds the two columns carrying the call the Runner replays:
// the sample body and the serialised sample headers.
//
// It is deliberately far below maxSourceSize, and the list endpoint is the
// reason. The editor enumerates the fields it wants precisely so that a
// reconnection of the realtime channel does not re-download what nobody looks
// at, and a generous cap here would let back in through the door what bunLock
// was shown out of the window. A sample is an example of a call, not an
// archive of one.
//
// Same rule as the source columns: what a TextField measures is the sealed
// value, hence cipherMax. There is no product limit announced on the plaintext
// here, so no hook holds one — the column is the only bound, and it is meant to
// be.
const maxSampleSize = 16 << 10 // 16,384 characters

// maxEnvSize bounds the encrypted environment of a function.
//
// It measures the *stored* value — base64 of nonce, ciphertext and tag — not the
// plaintext, which is what the caller sends. Base64 costs a third on top, so
// this leaves roughly 75 KB of secrets in clear, where the undeclared default
// left about 3.7 KB: enough to turn away a single long private key.
const maxEnvSize = 100 << 10 // 102,400 characters

// ensureFunctionsCollection creates the faasbox_functions collection if it doesn't exist,
// or migrates it by adding missing fields (script, packageJson, sampleBody,
// sampleHeaders, depsStatus, depsError, bunLock).
func ensureFunctionsCollection(app core.App) error {
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		// Collection doesn't exist — create it with all fields
		col = core.NewBaseCollection(faasboxFunctionsCollection)
		col.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			newNameHashField(),
			&core.TextField{Name: "env", Hidden: true, Max: maxEnvSize},
			&core.JSONField{Name: "plainEnv"},
			&core.TextField{Name: "script", Max: cipherMax(maxSourceSize)},
			&core.TextField{Name: "packageJson", Max: cipherMax(maxSourceSize)},
			newSampleBodyField(),
			newSampleHeadersField(),
			newDepsStatusField(),
			newDepsErrorField(),
			newBunLockField(),
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		col.AddIndex("idx_faasbox_functions_nameHash", true, "nameHash", "")
		return app.Save(col)
	}

	// Collection exists — add missing fields if needed
	needsSave := false
	if col.Fields.GetByName("nameHash") == nil {
		col.Fields.Add(newNameHashField())
		needsSave = true
	}
	// The uniqueness of a name is carried by the fingerprint, and by nothing
	// else. Left on the sealed column the index would still be there and would
	// constrain nothing — two nonces never collide — so two functions could
	// quietly come to share a name, which is the one thing resolution cannot
	// survive.
	if !slices.ContainsFunc(col.Indexes, func(idx string) bool {
		return strings.Contains(idx, "idx_faasbox_functions_nameHash")
	}) {
		col.RemoveIndex("idx_faasbox_functions_name")
		col.AddIndex("idx_faasbox_functions_nameHash", true, "nameHash", "")
		needsSave = true
	}
	if col.Fields.GetByName("script") == nil {
		col.Fields.Add(&core.TextField{Name: "script", Max: cipherMax(maxSourceSize)})
		needsSave = true
	}
	if col.Fields.GetByName("packageJson") == nil {
		col.Fields.Add(&core.TextField{Name: "packageJson", Max: cipherMax(maxSourceSize)})
		needsSave = true
	}
	if col.Fields.GetByName("sampleBody") == nil {
		col.Fields.Add(newSampleBodyField())
		needsSave = true
	}
	if col.Fields.GetByName("sampleHeaders") == nil {
		col.Fields.Add(newSampleHeadersField())
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

	// Realign the declared size of every capped field of this collection.
	//
	// A TextField measures the value it *stores*, and six of these now store a
	// ciphertext — about a third larger than the plaintext, and more when it is
	// not ASCII. A collection created by an earlier version carries the plaintext
	// cap and would refuse exactly what the encryption is meant to write through.
	// env is here for the older reason: it long carried PocketBase's 5000-rune
	// default, short enough to turn away a single long private key.
	//
	// Widening never invalidates what is stored: PocketBase validates a size when
	// a record is written, not when a schema changes.
	//
	// A field that is absent — or is not a TextField — yields nil, false and is
	// skipped, same reasoning as ensureLogsCollection.
	for _, want := range []struct {
		name string
		max  int
	}{
		{"script", cipherMax(maxSourceSize)},
		{"packageJson", cipherMax(maxSourceSize)},
		{"sampleBody", cipherMax(maxSampleSize)},
		{"sampleHeaders", cipherMax(maxSampleSize)},
		{"bunLock", cipherMax(maxLockfileSize)},
		{"depsError", cipherMax(maxDepsError + logMarkerSlack)},
		{"env", maxEnvSize},
	} {
		field, ok := col.Fields.GetByName(want.name).(*core.TextField)
		if !ok || field.Max == want.max {
			continue
		}
		field.Max = want.max
		needsSave = true
	}

	if needsSave {
		return app.Save(col)
	}
	return nil
}

// newNameHashField declares the column carrying the fingerprint of the name.
//
// It is what the unique index sits on and what resolveFunction queries, the name
// itself being sealed and therefore unfindable. Not Required: it is stamped by a
// hook rather than sent by a caller, and marking it required would refuse the
// record before the hook that fills it ever ran.
func newNameHashField() *core.TextField {
	return &core.TextField{Name: "nameHash"}
}

// newSampleBodyField and newSampleHeadersField declare the call the editor's
// Runner replays for this function: the body typed on the right of the panel,
// and the key/value rows typed on its left.
//
// They belong to the record for the reason the script does: switching function
// in the editor is a load, and what is not on the record has nothing to load.
// The editor writes the starting sample when it creates a function, exactly as
// it writes the example script, so an empty column is an empty sample and
// nothing else — which is what lets a body be deliberately emptied and stay
// that way. A function created through the management API carries none, for the
// reason it carries no example script: its author brought their own.
//
// **The headers are JSON serialised into a text column, never a JSONField.**
// The encryption at rest covers text columns; a JSON column would keep its
// own shape and this one would travel in the clear.
//
// Sealed at rest and legible all the same: OnRecordEnrich opens them on every
// read of the collections API, so anyone who can read this instance reads the
// sample. That is the point — it is how the panel shows the header a function
// actually expects — and it is why the documentation says not to put a real
// secret there.
func newSampleBodyField() *core.TextField {
	return &core.TextField{Name: "sampleBody", Max: cipherMax(maxSampleSize)}
}

func newSampleHeadersField() *core.TextField {
	return &core.TextField{Name: "sampleHeaders", Max: cipherMax(maxSampleSize)}
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

	if cipherKey == nil {
		return nil, fmt.Errorf("%s is not configured", encryptionKeyEnv)
	}

	plaintext, err := decrypt(encryptedEnv, cipherKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt env: %w", err)
	}

	var envMap map[string]string
	if err := json.Unmarshal(plaintext, &envMap); err != nil {
		return nil, fmt.Errorf("parse decrypted env: %w", err)
	}

	return envMap, nil
}

// functionEnv decrypts the environment variables of an already loaded function
// record. Returns a slice of "KEY=value" strings ready for cmd.Env, or nil if no
// env is configured. A failure is logged and degrades to nil: an invocation runs
// without secrets rather than not running at all.
//
// It takes the record rather than a name because both invocation paths now hold
// one: they resolved it to learn which directory to run. Looking it up again
// would be a second read for a row already in hand.
func functionEnv(app core.App, record *core.Record) []string {
	envMap, err := decryptFunctionEnv(record)
	if err != nil {
		app.Logger().Error("faasbox: failed to read env for function",
			"function", functionName(app, record), "error", err)
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
	record, err := functionFromPath(e)
	if err != nil {
		return answerFunctionLookup(e, err)
	}

	envMap, err := decryptFunctionEnv(record)
	if err != nil {
		e.App.Logger().Error("faasbox: failed to read env for function",
			"function", functionName(e.App, record), "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to read the environment of this function",
		})
	}

	return e.JSON(http.StatusOK, envMap)
}

// validateFunctionNameHook refuses a function name outside the product's rule:
// letters, digits and hyphens, never a leading or trailing hyphen, 64 characters
// at most.
//
// The rule was declared and enforced nowhere it applies. A name outside it saved,
// synced and ran, but POST /invoke/{name} answered 400 before resolving anything
// — the function was only reachable by an id the editor shows nowhere, with
// nothing on screen saying why. A name carrying a NUL was worse: Go refuses an
// environment entry containing one, so cmd.Run failed, every invocation answered
// 500 and wrote a log line — once a minute for a trigger on the minute.
//
// The engine cannot catch either case. executeFunction stopped validating the
// name on purpose: an identifier that builds no path must not be able to stop an
// execution. The guard belongs upstream, at the write.
//
// The refusal is an ApiError and not an ordinary error, same reason as
// validateTriggerHook: the record endpoints pass a hook failure through
// firstApiError, which keeps only an argument that already is an ApiError. An
// ordinary error is replaced by a generic "Failed to update record", and the
// client is left with a 400 whose body says nothing.
//
// The name is read **through the accessor**, exactly as validateTriggerHook
// reads its expression, and for the same reason: a partial update — a script
// saved on its own, a replacement that never renames — arrives carrying the name
// loaded from the database, which is sealed. Weighed as it stands, `fbx1:…`
// fails validName on its colon, and every such save is refused with a message
// naming a value the caller never sent. Binding this hook before the encryption
// one is what keeps the *submitted* value plaintext; the accessor is what covers
// the value nobody submitted.
func validateFunctionNameHook(e *core.RecordEvent) error {
	name := functionName(e.App, e.Record)
	if !validName.MatchString(name) || len(name) > 64 {
		// %q escapes what the regex refused — a NUL prints as \x00 rather than
		// travelling raw into the response.
		return apis.NewBadRequestError(fmt.Sprintf(
			"Invalid function name %q. Use letters, digits and hyphens only, "+
				"starting and ending with a letter or digit, 64 characters at most.",
			name,
		), nil)
	}
	return e.Next()
}

// functionName, functionScript, functionPackageJson, functionBunLock and
// functionDepsError are the only way to read the five encrypted columns of a
// function record. Nothing else touches them, whichever accessor: a value read
// by hand ships to the user as base64, and to disk as a script that will not
// run.
//
// The name is the one that reaches furthest. It is injected in the subprocess as
// FUNCTION_NAME, which is a documented contract: read by hand it would hand
// every function on this instance `fbx1:…` as its own name, on both triggers, and
// no test of ours would notice — only the user's code would.
func functionName(app core.App, record *core.Record) string {
	return decryptedText(app, record, "name")
}

func functionScript(app core.App, record *core.Record) string {
	return decryptedText(app, record, "script")
}

func functionPackageJson(app core.App, record *core.Record) string {
	return decryptedText(app, record, "packageJson")
}

func functionBunLock(app core.App, record *core.Record) string {
	return decryptedText(app, record, "bunLock")
}

func functionDepsError(app core.App, record *core.Record) string {
	return decryptedText(app, record, "depsError")
}

// validateFunctionSizeHook holds the product's source limit where the column no
// longer can.
//
// script and packageJson are encrypted at rest, so their declared size measures
// the ciphertext and is deliberately wider than what the product announces.
// Without this check the cap of maxSourceSize characters would simply cease to
// exist — the field would take whatever it was handed.
//
// It counts runes, which is the unit the limit was always expressed in and the
// one PocketBase itself used. It runs before the encryption hook and reads the
// submitted value directly, which at that point is still the plaintext.
//
// The refusal is an ApiError for the reason validateFunctionNameHook gives: the
// record endpoints keep only an argument that already is one, and an ordinary
// error reaches the client as a 400 whose body says nothing.
func validateFunctionSizeHook(e *core.RecordEvent) error {
	for _, field := range []string{"script", "packageJson"} {
		value := functionSourceInput(e.Record, field)
		if utf8.RuneCountInString(value) <= maxSourceSize {
			continue
		}
		return apis.NewBadRequestError(fmt.Sprintf(
			"The %s of a function is limited to %d characters.", field, maxSourceSize), nil)
	}
	return e.Next()
}

// functionSourceInput reads one of the two source columns as the request left
// it. A partial update carries the value loaded from the database, which is
// already sealed and is not what this check is about — the caller did not submit
// it, and it passed the check on the save that wrote it.
func functionSourceInput(record *core.Record, field string) string {
	value := record.GetString(field)
	if strings.HasPrefix(value, cipherPrefix) {
		return ""
	}
	return value
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

	if cipherKey == nil {
		return fmt.Errorf("cannot save encrypted env: %s is not configured", encryptionKeyEnv)
	}

	encrypted, err := encrypt(jsonBytes, cipherKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt env: %w", err)
	}

	e.Record.Set("env", encrypted)
	e.Record.Set("plainEnv", nil)

	return e.Next()
}

// syncRecordToDisk writes a single faasbox_functions record to disk. It creates
// the function directory and mirrors the three artefacts the record carries:
// index.ts, package.json and bun.lock. A field that is empty removes its file
// rather than leaving the previous one in place.
//
// **The sample call the record also carries is not one of them.** sampleBody and
// sampleHeaders are how the editor invokes the function, not source bun
// compiles: nothing on disk reads them, and writing them there would put a
// request payload in the directory the subprocess runs in. The directory itself stays: it
// may still hold a valid package.json and its node_modules, which clearing a
// script has nothing to do with — removing it is what deleting the function does.
//
// The directory is named by the record id, never by the name: a rename then
// moves nothing, loses no node_modules and triggers no reinstall. The name is
// kept for the error messages, which are read by a human.
//
// The three artefacts are read through their accessors: what the record carries
// is the sealed value, and what belongs on disk is the plaintext bun compiles.
func syncRecordToDisk(app core.App, record *core.Record, functionsDir string) error {
	if !validName.MatchString(record.Id) || len(record.Id) > 64 {
		return nil
	}
	name := functionName(app, record)

	dir := filepath.Join(functionsDir, record.Id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// The three artefacts follow one rule: an empty field removes its file. A
	// script left behind by a record that no longer carries one would keep the
	// function listed and keep running code the database has forgotten — until a
	// restart on a fresh filesystem stopped writing it, and the function turned
	// into a 404 with nothing having been touched in between.
	scriptPath := filepath.Join(dir, "index.ts")
	script := functionScript(app, record)
	if script != "" {
		if err := writeIfChanged(scriptPath, []byte(script), 0o644); err != nil {
			return fmt.Errorf("failed to write index.ts for %s: %w", name, err)
		}
	} else {
		os.Remove(scriptPath) // no script on the record: do not leave a stale one
	}

	pkgPath := filepath.Join(dir, "package.json")
	pkg := functionPackageJson(app, record)
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
	lock := functionBunLock(app, record)
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
	if err := syncRecordToDisk(e.App, e.Record, functionsDir); err != nil {
		e.App.Logger().Error("faasbox: failed to sync function to disk",
			"function", functionName(e.App, e.Record), "error", err)
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
	if !validName.MatchString(record.Id) || len(record.Id) > 64 {
		return nil
	}
	dir := filepath.Join(functionsDir, record.Id)
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
		if err := syncRecordToDisk(app, r, functionsDir); err != nil {
			app.Logger().Error("faasbox: failed to sync function to disk",
				"function", functionName(app, r), "error", err)
		}
	}

	app.Logger().Info("faasbox: synced functions from DB to disk", "count", len(records))
}
