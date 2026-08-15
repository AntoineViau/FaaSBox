package main

import (
	"encoding/json"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/types"
)

// How a record column is encrypted and read back. crypto.go answers "what is a
// sealed value"; this file answers "where does one live on a record" — the two
// column shapes, the hooks that seal them on the way in, the hook that opens
// them on the way out, and the fingerprints that let SQL still find the two
// columns it has to.
//
// **Two mechanisms, never interchangeable.** A response serialised by PocketBase
// goes through OnRecordEnrich, which is what keeps the editor — it reads the
// collections API directly — showing text rather than base64. Everything Go
// reads for itself serialises nothing and goes through the accessors below.
// Neither covers the other.
//
// **No direct access to an encrypted column, whichever accessor.** Not
// record.GetString, not record.GetStringSlice, not record.Get, not
// record.GetRaw. Reading one by hand is how a value ships to a user as base64
// without anything visibly breaking — and the failure is silent on the two
// paths that read a JSON column, where a ciphertext has exactly the shape a
// truncated payload legitimately takes. The four methods appear here, in the
// accessors, and nowhere else.
//
// Past the 300-line guideline, deliberately. The blind indexes could live in a
// file of their own, and they would then sit away from the very declaration a
// reader has to hold beside them: encryptedTextFields says the name of a
// function is sealed, blindIndexedColumns says nameHash is how it is still
// found. Neither is legible without the other.

// encryptedTextFields lists, per collection, the text columns sealed at rest.
//
// What is absent is as deliberate as what is here. A **hash** is never
// encrypted — keyHash, the four *Hash of a grant, and the two blind indexes
// below: a hash does not come back where a ciphertext would, and sealing one
// would reopen a path by which the database and the master key together hand
// out usable credentials. And env keeps its own hook and its own dedicated
// route: it is already encrypted, and it must not be opened by the enrichment
// below.
//
// The name of a function and the clientId of an OAuth client are here, and they
// are the two columns SQL still has to find. What buys that back is the blind
// index declared underneath — never a weaker cipher.
var encryptedTextFields = map[string][]string{
	faasboxFunctionsCollection:    {"name", "script", "packageJson", "bunLock", "depsError"},
	faasboxCronJobsCollection:     {"name", "schedule"},
	faasboxLogsCollection:         {"functionName", "stdout", "stderr"},
	faasboxAPIKeysCollection:      {"name", "keyPrefix"},
	faasboxOAuthClientsCollection: {"name", "clientId"},
	faasboxOAuthGrantsCollection:  {"redirectUri", "codeChallenge", "resource", "state"},
}

// blindIndexedColumn pairs an encrypted column with the one carrying its
// fingerprint.
type blindIndexedColumn struct{ source, index string }

// blindIndexedColumns lists, per collection, the sealed columns SQL still has to
// find, and the column that lets it.
//
// A column queried in SQL cannot simply be encrypted: AES-GCM draws a nonce at
// every write, so two writings of the same plaintext differ, and both properties
// those queries rest on — equality and uniqueness — are gone. The pair is what
// buys them back. The value is sealed, its fingerprint is not, and it is the
// fingerprint that carries the unique index and answers the lookup.
//
// What the index costs is stated where it is computed (blindIndex): equal values
// are visible as equal. That is the whole leak, and it is the price of being
// able to find a row at all.
var blindIndexedColumns = map[string][]blindIndexedColumn{
	faasboxFunctionsCollection:    {{source: "name", index: "nameHash"}},
	faasboxOAuthClientsCollection: {{source: "clientId", index: "clientIdHash"}},
}

// encryptedJSONFields lists, per collection, the JSON columns sealed at rest.
//
// They stay declared as JSONField. A JSON field accepts any JSON value, a string
// included, so a ciphertext lives there as a quoted string without the declared
// type moving — which is what leaves the editor's contract intact: it sends a
// parsed object and reads one back.
var encryptedJSONFields = map[string][]string{
	faasboxCronJobsCollection:     {"payload"},
	faasboxLogsCollection:         {"requestPayload"},
	faasboxOAuthClientsCollection: {"redirectUris"},
}

// enrichedCollections are the five whose records reach a client through the
// PocketBase collections API, and which the editor therefore reads in the clear.
//
// faasbox_oauth_grants is not among them, and that is not an oversight: the only
// page that reads it asks for the identifier, the dates and the client name, and
// nothing on the grant itself. Opening its columns for a response would buy
// nothing and widen what a serialisation carries.
var enrichedCollections = []string{
	faasboxFunctionsCollection,
	faasboxCronJobsCollection,
	faasboxLogsCollection,
	faasboxAPIKeysCollection,
	faasboxOAuthClientsCollection,
}

// registerFieldEncryption binds the hooks that seal these columns on the way in
// and open them on the way out.
//
// **It must be bound after the validation hooks.** validateCronScheduleHook
// weighs a cron expression, and a hook that sealed it first would hand it
// base64 to parse. The accessors it reads through make it robust either way —
// a partial update arrives carrying the sealed value loaded from the database —
// but the order is what the guarantee rests on, not the accessor.
//
// The OAuth collections are bound unconditionally although they only exist when
// FAASBOX_PUBLIC_URL mounted the authorization server: a tagged hook on a
// collection that is absent never fires.
func registerFieldEncryption(app core.App) {
	for collection := range encryptedTextFields {
		seal := sealRecordHook(collection)
		app.OnRecordCreate(collection).BindFunc(seal)
		app.OnRecordUpdate(collection).BindFunc(seal)
	}

	for _, collection := range enrichedCollections {
		app.OnRecordEnrich(collection).BindFunc(openRecordHook(collection))
	}
}

// registerBlindIndexes binds the hook that stamps the fingerprint of a sealed
// column on every write.
//
// **It is bound before registerFieldEncryption, and separately from it**, and
// both halves of that sentence matter. Before, because the fingerprint is taken
// on the plaintext and there is no reason to pay an AES open to get it back.
// Separately, because a caller may want the fingerprints without the sealing:
// the unique index rests on them, so a write that skips them is a write that
// stops enforcing that two functions cannot share a name.
//
// The handlers carry an id, so binding twice on the same app replaces rather
// than stacks.
func registerBlindIndexes(app core.App) {
	for collection := range blindIndexedColumns {
		stamp := stampBlindIndexHook(collection)
		app.OnRecordCreate(collection).Bind(&hook.Handler[*core.RecordEvent]{
			Id:   "faasboxBlindIndexCreate_" + collection,
			Func: stamp,
		})
		app.OnRecordUpdate(collection).Bind(&hook.Handler[*core.RecordEvent]{
			Id:   "faasboxBlindIndexUpdate_" + collection,
			Func: stamp,
		})
	}
}

// stampBlindIndexHook returns the create/update hook that writes the
// fingerprints of one collection.
//
// The source is read **through the accessor**, never off the column, and that is
// what makes a partial update work: it arrives carrying the value loaded from
// the database, which is sealed, and hashing that would stamp the fingerprint of
// a ciphertext — a value nothing will ever look up, on a record whose name looks
// perfectly normal in the editor.
func stampBlindIndexHook(collection string) func(*core.RecordEvent) error {
	columns := blindIndexedColumns[collection]

	return func(e *core.RecordEvent) error {
		for _, column := range columns {
			e.Record.Set(column.index, blindIndex(decryptedText(e.App, e.Record, column.source)))
		}
		return e.Next()
	}
}

// sealRecordHook returns the create/update hook that encrypts the columns of one
// collection.
//
// A column that already carries the version prefix is left alone, which is what
// makes a second save of an unchanged record a no-op rather than a second layer.
func sealRecordHook(collection string) func(*core.RecordEvent) error {
	text := encryptedTextFields[collection]
	jsonFields := encryptedJSONFields[collection]

	return func(e *core.RecordEvent) error {
		for _, field := range text {
			if err := sealTextField(e.Record, field); err != nil {
				return err
			}
		}
		for _, field := range jsonFields {
			if err := sealJSONField(e.Record, field); err != nil {
				return err
			}
		}
		return e.Next()
	}
}

// openRecordHook returns the OnRecordEnrich hook that decrypts the columns of
// one collection for the response being serialised.
//
// It rewrites nothing in the database: enrichment happens on the record that is
// about to be marshalled, and no save starts from here.
//
// **These collections declare no API rule, so PocketBase reserves them to a
// superuser session, and that is the only reason handing back plaintext here is
// safe.** Opening a rule on any of them would turn this hook into a leak. It
// touches the listed columns and no others: env keeps its dedicated route, and
// no hash is ever made legible.
func openRecordHook(collection string) func(*core.RecordEnrichEvent) error {
	text := encryptedTextFields[collection]
	jsonFields := encryptedJSONFields[collection]

	// Each column is rewritten only when opening it changed something. A
	// pointless Set would put an empty JSON column through PocketBase's string
	// normalisation and turn an unset payload into an empty string.
	return func(e *core.RecordEnrichEvent) error {
		for _, field := range text {
			if plaintext := decryptedText(e.App, e.Record, field); plaintext != e.Record.GetString(field) {
				e.Record.Set(field, plaintext)
			}
		}
		for _, field := range jsonFields {
			if plaintext := decryptedJSON(e.App, e.Record, field); plaintext != storedJSON(e.Record, field) {
				e.Record.Set(field, plaintext)
			}
		}
		return e.Next()
	}
}

// sealTextField encrypts a text column in place.
func sealTextField(record *core.Record, field string) error {
	stored := record.GetString(field)
	sealed, err := encryptField(stored)
	if err != nil {
		return err
	}
	if sealed != stored {
		record.Set(field, sealed)
	}
	return nil
}

// sealJSONField encrypts a JSON column in place, as a JSON string.
//
// PocketBase quotes a plain string handed to a JSON field, so what lands in the
// column is `"fbx1:…"` — a valid JSON value, and one this file can tell apart
// from a document.
//
// The already-sealed test goes through alreadySealed rather than through the
// prefix: a payload whose value is the string `"fbx1:…"` is a payload, and
// mistaking it for a ciphertext would store it in the clear. It is the same
// hazard encryptField guards on the text columns, and it has to be guarded here
// too — the raw JSON text never starts with the prefix itself, so encryptField's
// own check can never see it.
func sealJSONField(record *core.Record, field string) error {
	stored := storedJSON(record, field)
	if stored == "" || stored == "null" {
		return nil
	}
	var value string
	if json.Unmarshal([]byte(stored), &value) == nil && alreadySealed(value) {
		return nil
	}

	sealed, err := encryptField(stored)
	if err != nil {
		return err
	}
	record.Set(field, sealed)
	return nil
}

// decryptedText returns the plaintext of a text column.
//
// A value that will not decrypt yields the empty string, and the failure is
// logged rather than swallowed. Handing back the ciphertext instead would pass
// base64 off as content — written to disk as a script, compared as a redirect
// URI — where an empty value fails closed everywhere it is weighed.
func decryptedText(app core.App, record *core.Record, field string) string {
	plaintext, err := decryptField(record.GetString(field))
	if err != nil {
		reportDecryptFailure(app, record, field, err)
		return ""
	}
	return plaintext
}

// decryptedJSON returns the plaintext of a JSON column, as JSON text.
//
// What comes back is always a valid JSON value, and reproduces the two shapes
// the column carried before it was encrypted. A payload cut at the storage cap
// is no longer a document, and PocketBase kept it as an escaped string: the
// plaintext is quoted back into one here, so the published contract still tells
// a document from a truncated string — which a ciphertext, being exactly the
// second shape, would otherwise silently impersonate.
func decryptedJSON(app core.App, record *core.Record, field string) string {
	stored := storedJSON(record, field)
	ciphertext, sealed := jsonCiphertext(stored)
	if !sealed {
		return stored
	}

	plaintext, err := decryptField(ciphertext)
	if err != nil {
		reportDecryptFailure(app, record, field, err)
		return ""
	}
	if json.Valid([]byte(plaintext)) {
		return plaintext
	}
	quoted, err := json.Marshal(plaintext)
	if err != nil {
		return ""
	}
	return string(quoted)
}

// storedJSON returns the raw JSON text a JSON column holds.
func storedJSON(record *core.Record, field string) string {
	raw, ok := record.GetRaw(field).(types.JSONRaw)
	if !ok {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// jsonCiphertext reads a JSON value as a sealed one, and reports whether it is.
// A quoted string that does not carry the version prefix is an ordinary payload
// and is left alone.
//
// It is the **read** side, so the test is the prefix alone, like decryptField
// and unlike sealJSONField: a value that carries it and will not open is an
// error to report, not a document to relay.
func jsonCiphertext(stored string) (string, bool) {
	var value string
	if err := json.Unmarshal([]byte(stored), &value); err != nil {
		return "", false
	}
	if !strings.HasPrefix(value, cipherPrefix) {
		return "", false
	}
	return value, true
}

// reportDecryptFailure names what could not be read. The value itself never
// appears: it is either a secret or a ciphertext, and neither belongs in a log.
func reportDecryptFailure(app core.App, record *core.Record, field string, err error) {
	app.Logger().Error("faasbox: failed to decrypt a stored field",
		"collection", record.Collection().Name, "recordId", record.Id,
		"field", field, "error", err)
}
