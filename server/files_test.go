package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// bigFileSize is comfortably above the shrunken view cap the tests install, and
// small enough to stay cheap to write.
const bigFileSize = 8192

// withMaxFileView shrinks the inline view cap for the duration of a test.
// maxFileView is package state, so it has to be restored.
func withMaxFileView(t testing.TB, n int) {
	t.Helper()
	previous := maxFileView
	maxFileView = n
	t.Cleanup(func() { maxFileView = previous })
}

// filesFixtureId is the id of the function the browsing fixture lays out. The
// directory is named by it, which is what the routes resolve to before touching
// the disk; the record itself is registered under the name "app".
const filesFixtureId = "appfunction0001"

// setupFilesFixture builds a functions root holding one function directory with
// every shape the browsing routes have to deal with, and returns that root.
//
//	<root>/outside.txt           the file the escapes aim at
//	<root>/other/secret.txt      a sibling function, one "../" away
//	<root>/<id>/index.ts         plain text
//	<root>/<id>/package.json     plain text
//	<root>/<id>/big.txt          8 KB of text
//	<root>/<id>/binary.bin       carries a NUL byte
//	<root>/<id>/evade            symlink to <root>/outside.txt
//	<root>/<id>/inward           symlink to <root>/<id>/index.ts
//	<root>/<id>/node_modules/    three entries, one of them nested
func setupFilesFixture(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	fn := filepath.Join(root, filesFixtureId)

	mkdir := func(path string) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", path, err)
		}
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}
	link := func(target, path string) {
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("failed to link %s -> %s: %v", path, target, err)
		}
	}

	mkdir(filepath.Join(root, "other"))
	write(filepath.Join(root, "outside.txt"), "not yours")
	write(filepath.Join(root, "other", "secret.txt"), "another function")

	mkdir(fn)
	write(filepath.Join(fn, "index.ts"), "console.log('{}')")
	write(filepath.Join(fn, "package.json"), `{"dependencies":{}}`)
	write(filepath.Join(fn, "big.txt"), strings.Repeat("a", bigFileSize))
	write(filepath.Join(fn, "binary.bin"), "ELF\x00\x01\x02payload")
	link(filepath.Join(root, "outside.txt"), filepath.Join(fn, "evade"))
	link(filepath.Join(fn, "index.ts"), filepath.Join(fn, "inward"))

	mods := filepath.Join(fn, "node_modules")
	mkdir(filepath.Join(mods, "pkg-a"))
	mkdir(filepath.Join(mods, "pkg-b"))
	write(filepath.Join(mods, ".faasbox-deps"), "hash")
	write(filepath.Join(mods, "pkg-a", "package.json"), `{"name":"pkg-a"}`)

	return root
}

// registerFilesFixtureFunctions declares the records the browsing routes resolve
// before they touch the disk: the one the fixture laid out, and one that carries
// no directory at all. Neither is synced to disk — setupFilesFixture owns the
// tree, and a sync would overwrite it with a bare script.
func registerFilesFixtureFunctions(t testing.TB, app core.App) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []struct{ id, name string }{
		{filesFixtureId, "app"},
		{"", "fresh"},
	} {
		record := core.NewRecord(col)
		if fn.id != "" {
			record.Id = fn.id
		}
		record.Set("name", fn.name)
		if err := app.Save(record); err != nil {
			t.Fatalf("failed to register function %q: %v", fn.name, err)
		}
	}
}

// TestResolveFunctionPath is where the ticket's real risk is covered: the
// confinement of a directory the executed code can write into.
func TestResolveFunctionPath(t *testing.T) {
	root := setupFilesFixture(t)
	fn := filepath.Join(root, filesFixtureId)

	t.Run("the root itself resolves", func(t *testing.T) {
		got, err := resolveFunctionPath(root, filesFixtureId, "")
		if err != nil {
			t.Fatalf("resolveFunctionPath() error: %v", err)
		}
		if got != fn {
			t.Errorf("got %q, want %q", got, fn)
		}
	})

	t.Run("a nested file resolves", func(t *testing.T) {
		got, err := resolveFunctionPath(root, filesFixtureId, "node_modules/pkg-a/package.json")
		if err != nil {
			t.Fatalf("resolveFunctionPath() error: %v", err)
		}
		if want := filepath.Join(fn, "node_modules", "pkg-a", "package.json"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a link staying inside resolves", func(t *testing.T) {
		got, err := resolveFunctionPath(root, filesFixtureId, "inward")
		if err != nil {
			t.Fatalf("resolveFunctionPath() error: %v", err)
		}
		if want := filepath.Join(fn, "index.ts"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// A "../" is refused, not collapsed against the root. Collapsing would turn
	// the attempt into a plain not-found and lose the fact that it was made.
	t.Run("a climbing path is refused", func(t *testing.T) {
		for _, rel := range []string{"..", "../", "../outside.txt", "../other/secret.txt", "a/../../outside.txt"} {
			if _, err := resolveFunctionPath(root, filesFixtureId, rel); !errors.Is(err, errOutsideFunction) {
				t.Errorf("resolveFunctionPath(%q) error = %v, want errOutsideFunction", rel, err)
			}
		}
	})

	// The test that matters. The subprocess runs with this directory in reach
	// and executes arbitrary code, so a link planted at run time is the escape
	// a lexical check alone would never see.
	t.Run("a link leaving the directory is refused", func(t *testing.T) {
		if _, err := resolveFunctionPath(root, filesFixtureId, "evade"); !errors.Is(err, errOutsideFunction) {
			t.Fatalf("resolveFunctionPath(\"evade\") error = %v, want errOutsideFunction", err)
		}
	})

	t.Run("a missing entry is not a refusal", func(t *testing.T) {
		_, err := resolveFunctionPath(root, filesFixtureId, "nope.txt")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error = %v, want fs.ErrNotExist", err)
		}
		if errors.Is(err, errOutsideFunction) {
			t.Error("a missing entry must not read as an escape")
		}
	})

	t.Run("an invalid function id is refused", func(t *testing.T) {
		if _, err := resolveFunctionPath(root, "bad$id", ""); err == nil {
			t.Fatal("expected an error on an invalid function id")
		}
	})

	t.Run("a sibling whose name is a prefix is refused", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(root, filesFixtureId+"-extra"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveFunctionPath(root, filesFixtureId, "../"+filesFixtureId+"-extra"); !errors.Is(err, errOutsideFunction) {
			t.Errorf("error = %v, want errOutsideFunction", err)
		}
	})
}

// TestReadLevel covers the shape of a listing: one level, directories first,
// each group by name, sizes on files and entry counts on directories.
func TestReadLevel(t *testing.T) {
	root := setupFilesFixture(t)

	entries, err := readLevel(filepath.Join(root, filesFixtureId))
	if err != nil {
		t.Fatalf("readLevel() error: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := []string{"node_modules", "big.txt", "binary.bin", "evade", "index.ts", "inward", "package.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v", names, want)
	}

	byName := map[string]fileEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	mods := byName["node_modules"]
	if !mods.Dir {
		t.Error("node_modules is not reported as a directory")
	}
	if mods.Entries == nil || *mods.Entries != 3 {
		t.Errorf("node_modules entries = %v, want 3", mods.Entries)
	}
	if mods.Size != nil {
		t.Error("a directory must not carry a size")
	}
	if mods.Modified == "" {
		t.Error("a directory must carry its modification date")
	}

	index := byName["index.ts"]
	if index.Dir {
		t.Error("index.ts is reported as a directory")
	}
	if index.Size == nil || *index.Size != int64(len("console.log('{}')")) {
		t.Errorf("index.ts size = %v, want %d", index.Size, len("console.log('{}')"))
	}
	if index.Entries != nil {
		t.Error("a file must not carry an entry count")
	}
	if index.Modified == "" {
		t.Error("a file must carry its modification date")
	}

	// Never recursive: what lives under node_modules stays there.
	for _, e := range entries {
		if e.Name == "pkg-a" || e.Name == ".faasbox-deps" {
			t.Fatalf("the listing walked into node_modules: found %q at the root level", e.Name)
		}
	}
}

func TestFunctionFilesHandler(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:   "lists one level, directories first",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"path":""`,
				`"name":"node_modules","dir":true,"entries":3`,
				`"name":"index.ts","dir":false,"size":17`,
			},
			NotExpectedContent: []string{`"pkg-a"`, `".faasbox-deps"`},
		},
		{
			Name:   "lists a nested level",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files?path=node_modules",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"path":"node_modules"`,
				`"name":"pkg-a","dir":true,"entries":1`,
				`"name":".faasbox-deps"`,
			},
		},
		{
			// A function never invoked and without dependencies may have no
			// directory yet. That is "nothing there", not a failure.
			Name:   "an absent directory lists empty",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/fresh/files",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"entries":[]`},
		},
		{
			Name:   "a climbing path is refused",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files?path=../other",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`escapes the function directory`},
		},
		{
			Name:   "a file is not a directory",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files?path=index.ts",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`not a directory`},
		},
		{
			Name:   "an invalid function name is refused",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/bad$name/files",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid function name`},
		},
		{
			Name:   "unauthorized without auth",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files",
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:     401,
			ExpectedContent:    []string{`"data"`},
			NotExpectedContent: []string{`"entries"`},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}

func TestFunctionFileContentHandler(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:   "renders a text file inline",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=index.ts",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"path":"index.ts"`,
				`"size":17`,
				`console.log`,
			},
		},
		{
			// The verdict is the NUL byte, never the extension: node_modules is
			// full of files without one.
			Name:   "refuses a binary file inline",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=binary.bin",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:     415,
			ExpectedContent:    []string{`"binary"`},
			NotExpectedContent: []string{`"content"`},
		},
		{
			Name:   "refuses an oversized file inline, with its size",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=big.txt",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withMaxFileView(t, 4096)
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:     413,
			ExpectedContent:    []string{`"too large"`, `"size":8192`},
			NotExpectedContent: []string{`"content"`},
		},
		{
			// The cap is on what is displayed, not on what is fetched.
			Name:   "downloads past the view cap",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=big.txt&download=1",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				withMaxFileView(t, 4096)
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{strings.Repeat("a", 512)},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				got := res.Header.Get("Content-Disposition")
				if !strings.HasPrefix(got, "attachment;") || !strings.Contains(got, `"big.txt"`) {
					t.Errorf("Content-Disposition = %q, want an attachment named big.txt", got)
				}
			},
		},
		{
			Name:   "a climbing path is refused",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=../outside.txt",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:     400,
			ExpectedContent:    []string{`escapes the function directory`},
			NotExpectedContent: []string{`not yours`},
		},
		{
			Name:   "a symlink out of the directory is refused",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=evade",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:     400,
			ExpectedContent:    []string{`escapes the function directory`},
			NotExpectedContent: []string{`not yours`},
		},
		{
			Name:   "a directory is not a file",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=node_modules",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`"directory"`},
		},
		{
			Name:   "a missing file is a not found",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=nope.txt",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  404,
			ExpectedContent: []string{`"not found"`},
		},
		{
			Name:   "an invalid function name is refused",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/bad$name/files/content?path=index.ts",
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid function name`},
		},
		{
			Name:   "unauthorized without auth",
			Method: http.MethodGet,
			URL:    "/api/faasbox/functions/app/files/content?path=index.ts",
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFilesFixtureFunctions(t, app)
				registerFaaSRoutes(app, e, setupFilesFixture(t))
			},
			ExpectedStatus:     401,
			ExpectedContent:    []string{`"data"`},
			NotExpectedContent: []string{`console.log`},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
