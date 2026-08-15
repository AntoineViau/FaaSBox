package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// The blind index: what lets SQL still find a column whose value is sealed.
//
// Every scenario past the primitive runs on an app whose hooks seal, because
// that is the only footing on which any of it proves anything. On a readable
// fixture a lookup by name succeeds for the wrong reason, and the failure this
// file exists to prevent — a name that resolves to nothing, or resolves twice —
// cannot happen at all.

// namedApp is a sealed app carrying the name validation hook as well, bound in
// the order main.go binds them: the validator, then the fingerprint, then the
// sealing.
func namedApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	bindFunctionNameHook(app)
	setupFieldEncryption(t, app)
	return app
}

// TestDerivedSubkeys pins the two derivations against their values, and the
// second half is the one that matters: adding the index subkey must not have
// moved the one that encrypts, or every value written by an earlier build would
// have become unreadable without anything saying so.
func TestDerivedSubkeys(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x2a
	}

	cases := []struct{ info, want string }{
		{hkdfInfoCipher, "01676f440805338cdc4fd1985a340aed77b42cd915e0af7ce2020a78807c0b70"},
		{hkdfInfoIndex, "1c68711775a1ce1ce46773d2e2bc42589353407a258a595792336b982755dcd2"},
	}
	for _, tc := range cases {
		got, err := deriveKey(master, tc.info)
		if err != nil {
			t.Fatalf("deriveKey(%q) failed: %v", tc.info, err)
		}
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("subkey for %q = %s, want %s", tc.info, hex.EncodeToString(got), tc.want)
		}
	}
}

func TestBlindIndex(t *testing.T) {
	t.Run("is stable and hexadecimal", func(t *testing.T) {
		got := blindIndex("alpha")
		if got != blindIndex("alpha") {
			t.Error("two calls on the same value disagree: nothing could be looked up")
		}
		if len(got) != 64 {
			t.Errorf("fingerprint = %q, want 64 hex characters", got)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Errorf("fingerprint = %q, want hexadecimal: %v", got, err)
		}
	})

	// validName accepts capitals, so two spellings are two names — and the
	// fingerprint is taken on the exact value precisely so resolution stays as
	// faithful as it was.
	t.Run("tells the case apart", func(t *testing.T) {
		if blindIndex("Alpha") == blindIndex("alpha") {
			t.Error("Alpha and alpha share a fingerprint: two names would collide on the unique index")
		}
	})

	t.Run("an empty value has no fingerprint", func(t *testing.T) {
		if got := blindIndex(""); got != "" {
			t.Errorf("blindIndex(\"\") = %q, want it left empty", got)
		}
	})

	// The property the whole scheme rests on. A bare hash of a guessable name
	// falls to a dictionary in seconds from a dump; the keyed one cannot, and the
	// difference is only visible by computing the unkeyed digest here.
	t.Run("is keyed, not a bare digest", func(t *testing.T) {
		bare := sha256.Sum256([]byte("alpha"))
		if blindIndex("alpha") == hex.EncodeToString(bare[:]) {
			t.Error("the fingerprint is the plain SHA-256 of the value: a dictionary breaks it")
		}
	})
}

// TestSealedName_ResolvesEitherSpelling is the pair of readings every route
// depends on: the URL segment a user wired an integration on, and the id.
func TestSealedName_ResolvesEitherSpelling(t *testing.T) {
	app := namedApp(t)
	fn := saveSealedFunction(t, app, "alpha", "console.log('hi')", "")

	for _, segment := range []string{"alpha", fn.Id} {
		got, err := resolveFunction(app, segment)
		if err != nil {
			t.Fatalf("resolveFunction(%q) failed: %v", segment, err)
		}
		if got.Id != fn.Id {
			t.Errorf("resolveFunction(%q) = %q, want %q", segment, got.Id, fn.Id)
		}
	}
}

// TestSealedName_LeavesNothingLegibleInTheDatabase is what the whole exercise
// buys. Every column of the row is read straight out of SQLite: the name must
// appear in none of them, fingerprint included.
func TestSealedName_LeavesNothingLegibleInTheDatabase(t *testing.T) {
	app := namedApp(t)
	const name = "billing-webhook"
	fn := saveSealedFunction(t, app, name, "console.log('hi')", "")

	row := dbx.NullStringMap{}
	err := app.DB().NewQuery("SELECT * FROM " + faasboxFunctionsCollection + " WHERE id = {:id}").
		Bind(dbx.Params{"id": fn.Id}).One(&row)
	if err != nil {
		t.Fatalf("failed to read the stored row: %v", err)
	}
	if len(row) == 0 {
		t.Fatal("the stored row came back empty: the assertion below would prove nothing")
	}
	for column, value := range row {
		if strings.Contains(value.String, name) {
			t.Errorf("%s = %q in the database, want the name legible nowhere", column, value.String)
		}
	}

	if got := row["name"].String; !strings.HasPrefix(got, cipherPrefix) {
		t.Errorf("name = %q, want a sealed value", got)
	}
	if got := row["nameHash"].String; got != blindIndex(name) {
		t.Errorf("nameHash = %q, want the fingerprint of the name", got)
	}
}

// TestSealedName_ARenameFollows covers the write side of the fingerprint. A
// stamp that missed an update would leave the old name resolving and the new one
// unreachable, with the editor showing the new one all along.
func TestSealedName_ARenameFollows(t *testing.T) {
	app := namedApp(t)
	fn := saveSealedFunction(t, app, "alpha", "console.log('hi')", "")

	fn.Set("name", "delta")
	if err := app.Save(fn); err != nil {
		t.Fatalf("the rename failed: %v", err)
	}

	got, err := resolveFunction(app, "delta")
	if err != nil {
		t.Fatalf("resolveFunction(\"delta\") failed after the rename: %v", err)
	}
	if got.Id != fn.Id {
		t.Errorf("resolveFunction(\"delta\") = %q, want %q", got.Id, fn.Id)
	}

	var notFound *errNotFound
	if _, err := resolveFunction(app, "alpha"); !errors.As(err, &notFound) {
		t.Errorf("resolveFunction(\"alpha\") error = %v, want the old name to designate nothing", err)
	}
}

// TestSealedName_PartialUpdateKeepsTheFingerprint is the trap of stamping from
// the column instead of the accessor. A save that touches only the script
// arrives carrying the sealed name, and a fingerprint taken on *that* would be
// the hash of a ciphertext — a value nothing looks up, on a function the editor
// still shows under its proper name.
func TestSealedName_PartialUpdateKeepsTheFingerprint(t *testing.T) {
	app := namedApp(t)
	fn := saveSealedFunction(t, app, "alpha", "console.log('hi')", "")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id)
	if err != nil {
		t.Fatal(err)
	}
	stored.Set("script", "console.log('touched')")
	if err := app.Save(stored); err != nil {
		t.Fatalf("the partial update failed: %v", err)
	}

	if got, err := resolveFunction(app, "alpha"); err != nil || got.Id != fn.Id {
		t.Fatalf("resolveFunction(\"alpha\") = %v, %v — want the function still reachable by name", got, err)
	}
	if got := storedColumn(t, app, faasboxFunctionsCollection, "nameHash", fn.Id); got != blindIndex("alpha") {
		t.Errorf("nameHash = %q, want the fingerprint of the plaintext name", got)
	}
}

// TestSealedName_StaysUnique holds what resolution cannot survive losing. The
// uniqueness moved off the sealed column onto the fingerprint, and two functions
// sharing a name would make one of them permanently unreachable by name with
// nothing reporting anything.
func TestSealedName_StaysUnique(t *testing.T) {
	t.Run("a second save is refused", func(t *testing.T) {
		app := namedApp(t)
		saveSealedFunction(t, app, "alpha", "console.log('one')", "")

		col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}
		twin := core.NewRecord(col)
		twin.Set("name", "alpha")
		twin.Set("script", "console.log('two')")
		if err := app.Save(twin); err == nil {
			t.Fatal("a second function named alpha was accepted")
		}
	})

	// Concurrently, which is the case a check-then-write cannot cover: the index
	// is what settles it, and the count is the property — never two rows.
	t.Run("two simultaneous saves cannot both land", func(t *testing.T) {
		app := namedApp(t)
		col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}

		var accepted atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				record := core.NewRecord(col)
				record.Set("name", "twin")
				record.Set("script", "console.log('hi')")
				if err := app.Save(record); err == nil {
					accepted.Add(1)
				}
			}()
		}
		wg.Wait()

		records, err := app.FindAllRecords(faasboxFunctionsCollection)
		if err != nil {
			t.Fatal(err)
		}
		stored := 0
		for _, record := range records {
			if functionName(app, record) == "twin" {
				stored++
			}
		}
		if stored != 1 {
			t.Fatalf("%d functions are named twin, want exactly one", stored)
		}
		if got := int(accepted.Load()); got != stored {
			t.Errorf("%d saves reported success for %d stored rows", got, stored)
		}
	})
}

// TestSealedName_InvalidIsStillRefusedWithItsOwnMessage keeps the naming rule
// where the user reads it. The validator now reads through the accessor, and a
// refusal blaming the encryption instead of the name would leave whoever typed
// it with nothing to correct.
func TestSealedName_InvalidIsStillRefusedWithItsOwnMessage(t *testing.T) {
	app := namedApp(t)

	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("name", "-invalid-")
	record.Set("script", "console.log('hi')")

	err = app.Save(record)
	if err == nil {
		t.Fatal("a function named -invalid- was accepted")
	}
	if !strings.Contains(err.Error(), "Invalid function name") {
		t.Errorf("refusal = %q, want the naming rule's own message", err)
	}
}

// TestSealedName_ManagementContractIsUnchanged covers the two places the sealed
// name would have broken the published contract: the conflict on a name already
// taken, and a replacement whose body repeats the identity it was handed.
func TestSealedName_ManagementContractIsUnchanged(t *testing.T) {
	// A name already taken is a 409, never the constraint error the unique index
	// would raise on the way down.
	t.Run("a taken name conflicts", func(t *testing.T) {
		app := namedApp(t)
		saveSealedFunction(t, app, "beta", "console.log('hi')", "")

		_, err := createFunction(app, unrestricted, manageRequest{Name: "beta", Script: "console.log('x')"})
		if !errors.Is(err, errNameTaken) {
			t.Fatalf("createFunction() error = %v, want errNameTaken", err)
		}
		if refusal := classifyManageFailure(err); refusal == nil || refusal.status != http.StatusConflict {
			t.Errorf("refusal = %+v, want a 409", refusal)
		}
	})

	// The contract says a body may repeat the identity of its target, "so a
	// contract read back can be edited and sent straight in". Compared against
	// the stored value, every such replacement would be refused.
	t.Run("a replacement may repeat the name it was handed", func(t *testing.T) {
		app := namedApp(t)
		fn := saveSealedFunction(t, app, "beta", "console.log('hi')", "")

		contract, err := getFunction(app, unrestricted, "beta")
		if err != nil {
			t.Fatalf("getFunction() failed: %v", err)
		}
		if contract.Name != "beta" {
			t.Fatalf("contract.Name = %q, want the plaintext name", contract.Name)
		}

		replaced, err := replaceFunction(app, unrestricted, fn.Id, manageRequest{
			Name:   contract.Name,
			Script: "console.log('again')",
		})
		if err != nil {
			t.Fatalf("replaceFunction() refused a body repeating the name: %v", err)
		}
		if replaced.Name != "beta" {
			t.Errorf("contract.Name = %q, want %q", replaced.Name, "beta")
		}
	})
}

// functionNameReporter is a function that answers with what it was told it is
// called. It is the only way to observe FUNCTION_NAME as the user's code sees
// it: the contract is verified by what the function does with it, not by
// anything the server holds.
const functionNameReporter = `console.log(JSON.stringify({ name: process.env.FUNCTION_NAME }));`

// TestSealedName_FunctionNameIsPlaintextOverHTTP is the one defect of this
// change that would reach the user's code. A name read off the column ships to
// every subprocess as `fbx1:…`, and no test of the server would notice.
func TestSealedName_FunctionNameIsPlaintextOverHTTP(t *testing.T) {
	app := namedApp(t)
	functionsDir := t.TempDir()
	fn := saveSealedFunction(t, app, "reporter", functionNameReporter, "")
	if err := syncRecordToDisk(app, fn, functionsDir); err != nil {
		t.Fatal(err)
	}
	key := createTestAPIKey(t, app, "invoke", []string{"*"})

	scenario := tests.ApiScenario{
		Name:                  "the subprocess is told the plaintext name",
		Method:                http.MethodPost,
		URL:                   "/invoke/reporter",
		Headers:               map[string]string{"X-API-Key": key},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:     200,
		ExpectedContent:    []string{`"name":"reporter"`, `"function":"reporter"`},
		NotExpectedContent: []string{cipherPrefix},
	}
	scenario.Test(t)
}

// TestSealedName_FunctionNameIsPlaintextOnCron is the same contract on the other
// trigger. It holds on both or it holds on neither, and a scheduled run answers
// no one — its log entry is the only trace it leaves.
func TestSealedName_FunctionNameIsPlaintextOnCron(t *testing.T) {
	app := namedApp(t)
	functionsDir := t.TempDir()
	fn := saveSealedFunction(t, app, "scheduled-reporter", functionNameReporter, "")
	if err := syncRecordToDisk(app, fn, functionsDir); err != nil {
		t.Fatal(err)
	}

	runFunction(context.Background(), app, functionsDir, fn.Id, "{}", 0, "")

	entries := executionLogsOf(t, app, "scheduled-reporter")
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want the one the run left", len(entries))
	}
	stdout := decryptedText(app, entries[0], "stdout")
	if !strings.Contains(stdout, `"name":"scheduled-reporter"`) {
		t.Errorf("the run printed %q, want the plaintext name", stdout)
	}
}

// TestSealedClientId_IsFoundByItsFingerprint covers the OAuth half: the
// identifier is sealed like the rest, and the registration is still the one a
// client_id designates.
func TestSealedClientId_IsFoundByItsFingerprint(t *testing.T) {
	app := sealedApp(t)
	if err := ensureOAuthClientsCollection(app); err != nil {
		t.Fatal(err)
	}

	col, err := app.FindCollectionByNameOrId(faasboxOAuthClientsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("clientId", "client-abcdef")
	record.Set("name", "")
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to register the client: %v", err)
	}

	if got := storedColumn(t, app, faasboxOAuthClientsCollection, "clientId", record.Id); !strings.HasPrefix(got, cipherPrefix) {
		t.Errorf("clientId = %q, want a sealed value", got)
	}
	if got := storedColumn(t, app, faasboxOAuthClientsCollection, "clientIdHash", record.Id); got != blindIndex("client-abcdef") {
		t.Errorf("clientIdHash = %q, want the fingerprint of the identifier", got)
	}

	found, err := findOAuthClient(app, "client-abcdef")
	if err != nil {
		t.Fatalf("findOAuthClient() failed: %v", err)
	}
	if found.Id != record.Id {
		t.Errorf("findOAuthClient() = %q, want %q", found.Id, record.Id)
	}
	// A registration with no name falls back on the identifier, which is what the
	// consent screen puts in front of the user.
	if got := clientDisplayName(app, found); got != "client-abcdef" {
		t.Errorf("clientDisplayName() = %q, want the plaintext identifier", got)
	}
}
