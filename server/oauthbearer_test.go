package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// What a token is worth on the way in. The endpoint-level half — the challenge,
// and a token reaching the transport — is in mcp_test.go.

// bearerApp is manageApp carrying the two OAuth collections as well: the token
// path reads a grant, and the record it builds belongs to faasbox_api_keys.
func bearerApp(t testing.TB) (*tests.TestApp, string, *core.Record) {
	t.Helper()
	app, functionsDir, fn := manageApp(t)
	if err := ensureOAuthClientsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth clients collection: %v", err)
	}
	if err := ensureOAuthGrantsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth grants collection: %v", err)
	}
	return app, functionsDir, fn
}

// seedActiveGrant stores a client and the authorization it holds, in the state a
// token presented on /mcp resolves to.
func seedActiveGrant(t testing.TB, app core.App) *core.Record {
	t.Helper()
	return seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantActive)
}

// bearerScenario wires a scenario onto an app whose /mcp group is mounted on the
// OAuth footing, the way main.go mounts it when FAASBOX_PUBLIC_URL is set.
func bearerScenario(app *tests.TestApp, functionsDir string, s tests.ApiScenario) tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		registerFaaSRoutesFor(app, e, functionsDir, testOAuthConfig)
	}
	return s
}

// bearerHeader is what an authorized agent presents.
func bearerHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		name, header, want string
	}{
		{"no header", "", ""},
		{"bearer", "Bearer fbo_abc", "fbo_abc"},
		{"lowercase scheme", "bearer fbo_abc", "fbo_abc"},
		{"another scheme", "Basic dXNlcjpwYXNz", ""},
		{"the superuser session token", "eyJhbGciOiJIUzI1NiJ9.x.y", ""},
		{"scheme only", "Bearer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := bearerToken(r); got != tc.want {
				t.Fatalf("bearerToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every refusal of the token path, and they all read the same from outside: a
// token that does not pass never says which check caught it.
func TestBearerAPIKeyRefusals(t *testing.T) {
	cases := []struct {
		name  string
		token func(t testing.TB, app core.App) string
	}{
		{
			"a wrong prefix",
			func(t testing.TB, app core.App) string {
				seedActiveGrant(t, app)
				return "fbx_" + strings.Repeat("a", oauthSecretRandom)
			},
		},
		{
			"a wrong length",
			func(t testing.TB, app core.App) string {
				seedActiveGrant(t, app)
				return oauthSecretPrefix + strings.Repeat("a", oauthSecretRandom-1)
			},
		},
		{
			"a token this server never minted",
			func(t testing.TB, app core.App) string {
				seedActiveGrant(t, app)
				return oauthSecretPrefix + strings.Repeat("b", oauthSecretRandom)
			},
		},
		{
			"a revoked grant",
			func(t testing.TB, app core.App) string {
				grant := seedActiveGrant(t, app)
				revokeGrant(app, grant.Id, "test")
				return testAccessToken
			},
		},
		{
			"an expired access token",
			func(t testing.TB, app core.App) string {
				grant := seedActiveGrant(t, app)
				grant.Set("accessExpiresAt", types.NowDateTime().Add(-oauthAccessTTL))
				if err := app.Save(grant); err != nil {
					t.Fatalf("failed to expire the access token: %v", err)
				}
				return testAccessToken
			},
		},
		{
			"an access token with no expiry at all",
			func(t testing.TB, app core.App) string {
				grant := seedActiveGrant(t, app)
				grant.Set("accessExpiresAt", "")
				if err := app.Save(grant); err != nil {
					t.Fatalf("failed to clear the access expiry: %v", err)
				}
				return testAccessToken
			},
		},
		{
			// The second barrier: /authorize already refuses a foreign
			// resource, and this is what stops a token minted for another MCP
			// server — or carried in from another database — being replayed.
			"a grant opened for another resource",
			func(t testing.TB, app core.App) string {
				grant := seedActiveGrant(t, app)
				grant.Set("resource", "https://elsewhere.test/mcp")
				if err := app.Save(grant); err != nil {
					t.Fatalf("failed to move the grant's resource: %v", err)
				}
				return testAccessToken
			},
		},
		{
			// Codes, access tokens and refresh tokens are the same kind of
			// string: only the column their hash is sought in tells them apart.
			"a refresh token presented as an access token",
			func(t testing.TB, app core.App) string {
				seedActiveGrant(t, app)
				return testRefreshToken
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, _, _ := bearerApp(t)
			token := tc.token(t, app)
			if _, err := bearerAPIKey(app, token, testOAuthConfig); err == nil {
				t.Fatal("the token was accepted")
			}
		})
	}
}

// A valid token stands for an API key record that was never written down: no
// scope, canManage, and a name a log line can be read with.
func TestBearerAPIKeyIsAnUnscopedManagementKey(t *testing.T) {
	app, _, _ := bearerApp(t)
	grant := seedActiveGrant(t, app)

	record, err := bearerAPIKey(app, testAccessToken, testOAuthConfig)
	if err != nil {
		t.Fatalf("a valid access token was refused: %v", err)
	}

	if !record.GetBool("canManage") {
		t.Fatal("the record does not carry canManage")
	}
	allowed, err := readKeyScope(record)
	if err != nil {
		t.Fatalf("the scope of the record cannot be read: %v", err)
	}
	if !scopeIsUnrestricted(allowed) {
		t.Fatalf("the scope reads %v, want no restriction", allowed)
	}
	if name := record.GetString("name"); !strings.Contains(name, "test agent") ||
		!strings.Contains(name, grant.Id) {
		t.Fatalf("name = %q, want the client and the grant in it", name)
	}

	// A phantom API key carrying canManage is exactly what a stray save would
	// create, so the count is the assertion.
	keys, err := app.FindAllRecords(faasboxAPIKeysCollection)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("got %d API key records, want the token record to stay in memory", len(keys))
	}
}

// The scope has to survive the WrapStdHandler the same way a key's does — it is
// the same record, so it does, and this is what pins that nothing forked.
func TestBearerScopeReachesTheHandler(t *testing.T) {
	app, functionsDir, _ := bearerApp(t)
	seedActiveGrant(t, app)

	probe := func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		registerFaaSRoutesFor(app, e, functionsDir, testOAuthConfig)

		group := e.Router.Group("/mcp-bearer-probe")
		group.Bind(requireAPIKey(e.App, testOAuthConfig))
		group.Bind(requireManageKey())
		group.Bind(exposeKeyScope())
		group.GET("", apis.WrapStdHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, err := keyScopeFromContext(r.Context())
			if err != nil {
				http.Error(w, "unreadable", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": allowed})
		})))
	}

	scenario := tests.ApiScenario{
		Name:            "a token arrives with no restriction",
		Method:          http.MethodGet,
		URL:             "/mcp-bearer-probe",
		Headers:         bearerHeader(testAccessToken),
		ExpectedStatus:  200,
		ExpectedContent: []string{`"allowed":[]`},
	}
	scenario.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	scenario.DisableTestAppCleanup = true
	scenario.BeforeTestFunc = probe
	scenario.Test(t)
}

// A key and a token presented together: the key is what the request is weighed
// on, and the token is never looked at.
func TestKeyWinsOverToken(t *testing.T) {
	app, functionsDir, _ := bearerApp(t)
	seedActiveGrant(t, app)

	scenarios := []tests.ApiScenario{
		{
			Name:   "a disabled key is refused although the token is valid",
			Method: http.MethodPost,
			URL:    "/mcp",
			Headers: map[string]string{
				"Accept":        "application/json, text/event-stream",
				"Authorization": "Bearer " + testAccessToken,
				"X-API-Key": createTestAPIKeyWithOptions(t, app, "disabled-manager", nil,
					false, types.NowDateTime().Time().AddDate(1, 0, 0)),
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{"API key is disabled"},
		},
		{
			Name:   "an invoke-only key is refused although the token is valid",
			Method: http.MethodPost,
			URL:    "/mcp",
			Headers: map[string]string{
				"Accept":        "application/json, text/event-stream",
				"Authorization": "Bearer " + testAccessToken,
				"X-API-Key":     createTestAPIKey(t, app, "invoke-only-beside-a-token", nil),
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{"not authorized to manage functions"},
		},
	}

	for _, scenario := range scenarios {
		s := bearerScenario(app, functionsDir, scenario)
		s.Test(t)
	}
}

// What the AI agents page reads and writes, through the PocketBase collections
// API. It is pinned here because the page depends on both halves of it: the
// field selection that keeps the token hashes off the wire, and the status flag
// that ends an authorization.
func TestGrantsCollectionServesThePage(t *testing.T) {
	app := oauthApp(t)
	grant := seedActiveGrant(t, app)

	listURL := "/api/collections/" + faasboxOAuthGrantsCollection + "/records" +
		"?filter=" + url.QueryEscape(`status="active"`) +
		"&expand=client" +
		"&fields=" + url.QueryEscape("id,created,refreshExpiresAt,expand.client.name,expand.client.clientId")

	scenarios := []tests.ApiScenario{
		{
			// The hashes are Hidden, and Hidden is not enough on its own: it
			// strips a field for non-superusers, and the editor *is* a
			// superuser. The selection is what keeps them out.
			Name:           "the list carries no token material",
			Method:         http.MethodGet,
			URL:            listURL,
			Headers:        map[string]string{"Authorization": superuserToken},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"id":"` + grant.Id + `"`,
				`"name":"test agent"`,
				`"refreshExpiresAt":"`,
			},
			NotExpectedContent: []string{"Hash", "codeHash", "accessTokenHash", "refreshTokenHash"},
		},
		{
			Name:            "the list is superuser-only",
			Method:          http.MethodGet,
			URL:             listURL,
			ExpectedStatus:  403,
			ExpectedContent: []string{"Only superusers can perform this action"},
		},
		{
			Name:            "revoking is a flag",
			Method:          http.MethodPatch,
			URL:             "/api/collections/" + faasboxOAuthGrantsCollection + "/records/" + grant.Id,
			Body:            strings.NewReader(`{"status":"revoked"}`),
			Headers:         map[string]string{"Authorization": superuserToken},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"status":"revoked"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := reloadGrant(t, app, grant.Id).GetString("status"); got != grantRevoked {
					t.Fatalf("status = %q, want %q", got, grantRevoked)
				}
				// The hashes stay: a later replay then resolves to this grant
				// and is refused by its state, rather than looking like an
				// unknown credential.
				if reloadGrant(t, app, grant.Id).GetString("accessTokenHash") == "" {
					t.Fatal("revoking cleared the access token hash")
				}
			},
		},
	}

	for _, scenario := range scenarios {
		scenario.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
		scenario.DisableTestAppCleanup = true
		scenario.Test(t)
	}
}

// A revocation takes effect on the next request: the grant is read on every
// call, and there is no session to invalidate.
func TestRevokedGrantIsRefusedAtOnce(t *testing.T) {
	app, _, _ := bearerApp(t)
	grant := seedActiveGrant(t, app)

	if _, err := bearerAPIKey(app, testAccessToken, testOAuthConfig); err != nil {
		t.Fatalf("the token was refused before the revocation: %v", err)
	}

	revokeGrant(app, grant.Id, "revoked from the AI agents page")

	if _, err := bearerAPIKey(app, testAccessToken, testOAuthConfig); err == nil {
		t.Fatal("the token still passes after the grant was revoked")
	}
}
