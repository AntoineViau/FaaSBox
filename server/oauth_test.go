package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// testOAuthConfig is the identity every scenario in these files publishes.
var testOAuthConfig = oauthConfig{
	Issuer:   "https://faas.test",
	Resource: "https://faas.test/mcp",
}

// oauthApp prepares an app carrying the two OAuth collections.
func oauthApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	if err := ensureOAuthClientsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth clients collection: %v", err)
	}
	if err := ensureOAuthGrantsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth grants collection: %v", err)
	}
	return app
}

// oauthScenario wires a scenario onto an already prepared app, mounting the
// OAuth routes exactly as main.go mounts them.
//
// mountOAuth calls both ensure functions again, so every scenario re-exercises
// their idempotence on the way in.
func oauthScenario(app *tests.TestApp, s tests.ApiScenario) tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		if err := mountOAuth(e, testOAuthConfig); err != nil {
			t.Fatalf("failed to mount the OAuth routes: %v", err)
		}
	}
	return s
}

func TestParsePublicURL(t *testing.T) {
	valid := []struct {
		name, raw, issuer string
	}{
		{"https origin", "https://faas.example.com", "https://faas.example.com"},
		{"trailing slash is dropped", "https://faas.example.com/", "https://faas.example.com"},
		{"port is kept", "https://faas.example.com:8443", "https://faas.example.com:8443"},
		{"http on 127.0.0.1", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http on localhost", "http://localhost:8080/", "http://localhost:8080"},
		{"http on ::1", "http://[::1]:8080", "http://[::1]:8080"},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parsePublicURL(tc.raw)
			if err != nil {
				t.Fatalf("parsePublicURL(%q) failed: %v", tc.raw, err)
			}
			if cfg.Issuer != tc.issuer {
				t.Fatalf("issuer = %q, want %q", cfg.Issuer, tc.issuer)
			}
			if want := tc.issuer + "/mcp"; cfg.Resource != want {
				t.Fatalf("resource = %q, want %q", cfg.Resource, want)
			}
		})
	}

	refused := []struct {
		name, raw, reason string
	}{
		{"absent", "", "is not set"},
		{"blank", "   ", "is not set"},
		{"not absolute", "faas.example.com", "absolute"},
		{"path only", "/mcp", "absolute"},
		{"carries a path", "https://faas.example.com/faasbox", "path"},
		{"carries a query", "https://faas.example.com?a=1", "query"},
		{"carries a fragment", "https://faas.example.com#top", "fragment"},
		{"carries credentials", "https://user:pw@faas.example.com", "credentials"},
		{"unknown scheme", "ftp://faas.example.com", "absolute"},
		{"http on a public host", "http://faas.example.com", "loopback"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePublicURL(tc.raw)
			if err == nil {
				t.Fatalf("parsePublicURL(%q) was accepted, want a refusal", tc.raw)
			}
			// The message has to name the motive: it is the only thing printed
			// when the endpoints silently do not go up.
			if !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("refusal %q does not name %q", err, tc.reason)
			}
		})
	}
}

// The variable being absent is the nominal "no OAuth" case, and it has to read as
// a refusal rather than as an empty configuration.
func TestOAuthConfigFromEnvWithoutPublicURL(t *testing.T) {
	t.Setenv(publicURLEnv, "")
	if _, err := oauthConfigFromEnv(); err == nil {
		t.Fatal("an absent FAASBOX_PUBLIC_URL was accepted, want a refusal that keeps the routes down")
	}
}

func TestOAuthConfigFromEnvWithPublicURL(t *testing.T) {
	t.Setenv(publicURLEnv, "https://faas.example.com/")
	cfg, err := oauthConfigFromEnv()
	if err != nil {
		t.Fatalf("a valid FAASBOX_PUBLIC_URL was refused: %v", err)
	}
	if cfg.Issuer != "https://faas.example.com" {
		t.Fatalf("issuer = %q, want the normalised origin", cfg.Issuer)
	}
}

func TestProtectedResourceMetadataIsPublic(t *testing.T) {
	for _, url := range []string{
		"/.well-known/oauth-protected-resource",
		// RFC 9728 §3: same document, path inserted.
		"/.well-known/oauth-protected-resource/mcp",
	} {
		app := oauthApp(t)
		s := oauthScenario(app, tests.ApiScenario{
			Name:           "protected resource metadata at " + url,
			Method:         http.MethodGet,
			URL:            url,
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"resource":"https://faas.test/mcp"`,
				`"authorization_servers":["https://faas.test"]`,
				`"scopes_supported":["faasbox"]`,
			},
		})
		s.Test(t)
	}
}

func TestAuthServerMetadataIsPublic(t *testing.T) {
	app := oauthApp(t)
	s := oauthScenario(app, tests.ApiScenario{
		Name:           "authorization server metadata",
		Method:         http.MethodGet,
		URL:            "/.well-known/oauth-authorization-server",
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"issuer":"https://faas.test"`,
			`"authorization_endpoint":"https://faas.test/oauth/authorize"`,
			`"token_endpoint":"https://faas.test/oauth/token"`,
			`"registration_endpoint":"https://faas.test/oauth/register"`,
			`"response_types_supported":["code"]`,
			`"grant_types_supported":["authorization_code","refresh_token"]`,
			`"code_challenge_methods_supported":["S256"]`,
			`"token_endpoint_auth_methods_supported":["none"]`,
			`"authorization_response_iss_parameter_supported":true`,
		},
		// No key set to publish: an empty jwks_uri would hand a client an empty
		// URL to fetch.
		NotExpectedContent: []string{`"jwks_uri"`, `"plain"`},
	})
	s.Test(t)
}

func TestRegisterClientNeedsNoAuthentication(t *testing.T) {
	app := oauthApp(t)
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "register a client",
		Method:          http.MethodPost,
		URL:             "/oauth/register",
		Body:            strings.NewReader(`{"client_name":"test agent","redirect_uris":["http://127.0.0.1:41234/callback"]}`),
		ExpectedStatus:  201,
		ExpectedContent: []string{`"client_id":"`, `"token_endpoint_auth_method":"none"`},
		// A public client gets no secret, and the field is absent rather than
		// empty.
		NotExpectedContent: []string{`"client_secret"`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			records, err := app.FindAllRecords(faasboxOAuthClientsCollection)
			if err != nil || len(records) != 1 {
				t.Fatalf("got %d client records (err %v), want 1", len(records), err)
			}
			if got := records[0].GetString("name"); got != "test agent" {
				t.Fatalf("stored name = %q, want %q", got, "test agent")
			}
		},
	})
	s.Test(t)
}

func TestRegisterClientWithoutRedirectURIs(t *testing.T) {
	app := oauthApp(t)
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "register without redirect_uris",
		Method:          http.MethodPost,
		URL:             "/oauth/register",
		Body:            strings.NewReader(`{"client_name":"test agent"}`),
		ExpectedStatus:  400,
		ExpectedContent: []string{`"error":"invalid_redirect_uri"`, `redirect_uris`},
	})
	s.Test(t)
}

func TestValidateRedirectURIs(t *testing.T) {
	cases := []struct {
		name    string
		uris    []string
		refused bool
	}{
		{"loopback", []string{"http://127.0.0.1:41234/callback"}, false},
		{"https", []string{"https://app.example.com/cb"}, false},
		{"private scheme", []string{"com.example.app:/cb"}, false},
		{"none", nil, true},
		{"empty list", []string{}, true},
		{"relative", []string{"/cb"}, true},
		{"fragment", []string{"https://app.example.com/cb#x"}, true},
		{"javascript", []string{"javascript:alert(1)"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRedirectURIs(tc.uris)
			if tc.refused != (err != nil) {
				t.Fatalf("validateRedirectURIs(%v) = %v, refused expected: %v", tc.uris, err, tc.refused)
			}
		})
	}
}

func TestRedirectURIAllowed(t *testing.T) {
	registered := []string{"http://127.0.0.1:41234/callback", "https://app.example.com/cb"}

	cases := []struct {
		name      string
		candidate string
		allowed   bool
	}{
		{"exact loopback", "http://127.0.0.1:41234/callback", true},
		// RFC 8252: the port is the one component a native client cannot know
		// when it registers.
		{"loopback on another port", "http://127.0.0.1:9999/callback", true},
		{"exact https", "https://app.example.com/cb", true},
		{"loopback with another path", "http://127.0.0.1:41234/other", false},
		{"another loopback spelling", "http://localhost:41234/callback", false},
		{"https on another port", "https://app.example.com:8443/cb", false},
		// A prefix comparison would accept this one, and that is the open
		// redirect the exact match exists to prevent.
		{"prefix of a registered URI", "https://app.example.com/cb.attacker.test", false},
		{"unrelated host", "https://attacker.test/cb", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectURIAllowed(registered, tc.candidate); got != tc.allowed {
				t.Fatalf("redirectURIAllowed(%q) = %v, want %v", tc.candidate, got, tc.allowed)
			}
		})
	}
}

func TestVerifyPKCE(t *testing.T) {
	if !verifyPKCE(testCodeChallenge(), testCodeVerifier) {
		t.Fatal("the matching verifier was refused")
	}
	if verifyPKCE(testCodeChallenge(), testCodeVerifier+"x") {
		t.Fatal("a wrong verifier was accepted")
	}
	if verifyPKCE("", testCodeVerifier) || verifyPKCE(testCodeChallenge(), "") {
		t.Fatal("an empty side was accepted")
	}
}

func TestEncodeAuthServerMetaDropsEmptyJWKS(t *testing.T) {
	encoded, err := encodeAuthServerMeta(authServerMetadata(testOAuthConfig))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if _, present := document["jwks_uri"]; present {
		t.Fatal("jwks_uri is published; this server signs nothing and has no key set")
	}
}

// The hourly pass has to collect exactly the four kinds of dead row, and leave a
// live authorization, a request still in flight, and the clients behind them
// standing.
func TestPruneOAuthRecords(t *testing.T) {
	app := oauthApp(t)

	live := seedOAuthClient(t, app, testRedirectURI)
	liveGrant := seedOAuthGrant(t, app, live, grantActive)
	liveGrant.Set("refreshExpiresAt", types.NowDateTime().Add(oauthRefreshTTL))
	if err := app.Save(liveGrant); err != nil {
		t.Fatal(err)
	}

	stale := seedOAuthClient(t, app, testRedirectURI)
	staleGrant := seedOAuthGrant(t, app, stale, grantPending)
	staleGrant.Set("requestExpiresAt", types.NowDateTime().Add(-time.Minute))
	if err := app.Save(staleGrant); err != nil {
		t.Fatal(err)
	}

	refused := seedOAuthClient(t, app, testRedirectURI)
	refusedGrant := seedOAuthGrant(t, app, refused, grantRevoked)

	expired := seedOAuthClient(t, app, testRedirectURI)
	expiredGrant := seedOAuthGrant(t, app, expired, grantActive)
	expiredGrant.Set("refreshExpiresAt", types.NowDateTime().Add(-time.Hour))
	if err := app.Save(expiredGrant); err != nil {
		t.Fatal(err)
	}

	// Approved, then abandoned by its client. Not pending, not revoked, and it
	// carries no refresh token, so it is the row the first three clauses walk
	// straight past — and nothing else ever comes back to collect it.
	abandoned := seedOAuthClient(t, app, testRedirectURI)
	abandonedGrant := seedOAuthGrant(t, app, abandoned, grantCode)
	abandonedGrant.Set("codeExpiresAt", types.NowDateTime().Add(-time.Minute))
	if err := app.Save(abandonedGrant); err != nil {
		t.Fatal(err)
	}

	// The same shape, one minute earlier in its life: still exchangeable, so the
	// same clause must not touch it.
	pending := seedOAuthClient(t, app, testRedirectURI)
	pendingCode := seedOAuthGrant(t, app, pending, grantCode)

	// Registered and never followed by anything: only the age decides.
	fresh := seedOAuthClient(t, app, testRedirectURI)
	forgotten := seedOAuthClient(t, app, testRedirectURI)
	setRecordDate(t, app, faasboxOAuthClientsCollection, forgotten.Id, "created",
		time.Now().Add(-oauthClientGrace-time.Hour))

	pruneOAuthRecords(app)

	for _, gone := range []*core.Record{staleGrant, refusedGrant, expiredGrant, abandonedGrant} {
		if _, err := app.FindRecordById(faasboxOAuthGrantsCollection, gone.Id); err == nil {
			t.Fatalf("grant %q survived the purge", gone.Id)
		}
	}
	for _, kept := range []*core.Record{liveGrant, pendingCode} {
		if _, err := app.FindRecordById(faasboxOAuthGrantsCollection, kept.Id); err != nil {
			t.Fatalf("grant %q was purged while still usable: %v", kept.Id, err)
		}
	}

	if _, err := app.FindRecordById(faasboxOAuthClientsCollection, forgotten.Id); err == nil {
		t.Fatal("a registration older than the grace period and followed by nothing survived")
	}
	for _, kept := range []*core.Record{live, fresh} {
		if _, err := app.FindRecordById(faasboxOAuthClientsCollection, kept.Id); err != nil {
			t.Fatalf("client %q was purged: %v", kept.Id, err)
		}
	}
}

// The guard is what makes a one-use credential one-use. Two claims on the same
// grant is the shape of two presentations of the same code, or of the same
// refresh token, racing each other.
func TestClaimGrantIsWonOnce(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantCode)
	guard := grantGuard{Column: "status", Value: grantCode}

	won, err := claimGrant(app, grant.Id, guard, issuedTokens{
		AccessHash: hashOAuthSecret("fbo_first-access"), RefreshHash: hashOAuthSecret("fbo_first-refresh"),
	})
	if err != nil || !won {
		t.Fatalf("the first claim did not win: won=%v err=%v", won, err)
	}

	// Same guard, same grant: it has moved to active, so nothing matches and the
	// second caller must not walk away believing it issued anything.
	won, err = claimGrant(app, grant.Id, guard, issuedTokens{
		AccessHash: hashOAuthSecret("fbo_second-access"), RefreshHash: hashOAuthSecret("fbo_second-refresh"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("the second claim won too; the credential is not single-use")
	}

	stored := reloadGrant(t, app, grant.Id)
	if stored.GetString("status") != grantActive {
		t.Fatalf("status = %q, want %q", stored.GetString("status"), grantActive)
	}
	// The loser must not have overwritten the winner's credentials.
	if stored.GetString("accessTokenHash") != hashOAuthSecret("fbo_first-access") {
		t.Fatal("the losing claim overwrote the tokens the winner issued")
	}
}

// A revocation writes the status and nothing else, so a caller holding a record
// read before the winner's write cannot undo it.
func TestRevokeGrantTouchesOnlyTheStatus(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantActive)
	before := reloadGrant(t, app, grant.Id).GetString("refreshTokenHash")

	revokeGrant(app, grant.Id, "test")

	stored := reloadGrant(t, app, grant.Id)
	if stored.GetString("status") != grantRevoked {
		t.Fatalf("status = %q, want %q", stored.GetString("status"), grantRevoked)
	}
	if stored.GetString("refreshTokenHash") != before {
		t.Fatal("the revocation rewrote a credential column")
	}
}
