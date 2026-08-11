package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The fixtures the authorization, consent and token scenarios share.
const (
	testClientID          = "test-client-id"
	testRedirectURI       = "http://127.0.0.1:41234/callback"
	testCodeVerifier      = "M25iVXpKU3puUjFaYWg3T1NDTDQtcW1ROUY5YXlwalNoc0hhakxpfmZH"
	testAuthorizationCode = "fbo_test-authorization-code"
	testRefreshToken      = "fbo_test-refresh-token"
	testState             = "st-4711"
)

func testCodeChallenge() string {
	sum := sha256.Sum256([]byte(testCodeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// seedOAuthClient stores a registration without going through /oauth/register:
// the scenarios below are about what happens after one.
func seedOAuthClient(t testing.TB, app core.App, redirectUris ...string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxOAuthClientsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	// The identifier carries a unique index, so only the first client of a test
	// takes the readable one the scenarios name in their requests. The others
	// exist to be counted, not to be addressed.
	clientId := testClientID
	if existing, err := findOAuthClient(app, clientId); err == nil && existing != nil {
		clientId = security.RandomString(clientIDLen)
	}
	record.Set("clientId", clientId)
	record.Set("name", "test agent")
	record.Set("redirectUris", redirectUris)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to seed an OAuth client: %v", err)
	}
	return record
}

// seedOAuthGrant stores a grant in the state a scenario needs, with the
// credentials that state carries.
func seedOAuthGrant(t testing.TB, app core.App, client *core.Record, status string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxOAuthGrantsCollection)
	if err != nil {
		t.Fatal(err)
	}
	now := types.NowDateTime()

	record := core.NewRecord(col)
	record.Set("client", client.Id)
	record.Set("status", status)
	record.Set("redirectUri", testRedirectURI)
	record.Set("codeChallenge", testCodeChallenge())
	record.Set("resource", testOAuthConfig.Resource)
	record.Set("state", testState)
	record.Set("requestExpiresAt", now.Add(oauthRequestTTL))

	switch status {
	case grantCode:
		record.Set("codeHash", hashOAuthSecret(testAuthorizationCode))
		record.Set("codeExpiresAt", now.Add(oauthCodeTTL))
	case grantActive:
		record.Set("codeHash", hashOAuthSecret(testAuthorizationCode))
		record.Set("accessTokenHash", hashOAuthSecret("fbo_test-access-token"))
		record.Set("refreshTokenHash", hashOAuthSecret(testRefreshToken))
		record.Set("refreshExpiresAt", now.Add(oauthRefreshTTL))
	}

	if err := app.Save(record); err != nil {
		t.Fatalf("failed to seed an OAuth grant: %v", err)
	}
	return record
}

// authorizeURL builds a /oauth/authorize request, complete unless a field is
// overridden with an empty string.
func authorizeURL(clientId string, overrides url.Values) string {
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientId},
		"redirect_uri":          {testRedirectURI},
		"code_challenge":        {testCodeChallenge()},
		"code_challenge_method": {"S256"},
		"resource":              {testOAuthConfig.Resource},
		"state":                 {testState},
	}
	for key, values := range overrides {
		if len(values) == 0 || values[0] == "" {
			query.Del(key)
			continue
		}
		query.Set(key, values[0])
	}
	return "/oauth/authorize?" + query.Encode()
}

// tokenForm turns a token request into a body the endpoint accepts.
func tokenForm(values url.Values) (io.Reader, map[string]string) {
	return strings.NewReader(values.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
}

// reloadGrant reads a grant back from the database.
func reloadGrant(t testing.TB, app core.App, id string) *core.Record {
	t.Helper()
	record, err := app.FindRecordById(faasboxOAuthGrantsCollection, id)
	if err != nil {
		t.Fatalf("grant %q not found: %v", id, err)
	}
	return record
}

// A client nobody registered, and a redirect URI nobody registered, are refused
// **on the spot**. Redirecting either one is the open redirect itself.
func TestAuthorizeRefusesOnTheSpot(t *testing.T) {
	cases := []struct {
		name, url, expected string
	}{
		{"unknown client_id", authorizeURL("nobody", nil), `"error":"invalid_client"`},
		{"unregistered redirect_uri",
			authorizeURL(testClientID, url.Values{"redirect_uri": {"https://attacker.test/cb"}}),
			`"error":"invalid_request"`},
		{"missing redirect_uri",
			authorizeURL(testClientID, url.Values{"redirect_uri": {""}}),
			`"error":"invalid_request"`},
	}
	for _, tc := range cases {
		app := oauthApp(t)
		seedOAuthClient(t, app, testRedirectURI)
		s := oauthScenario(app, tests.ApiScenario{
			Name:            tc.name,
			Method:          http.MethodGet,
			URL:             tc.url,
			ExpectedStatus:  400,
			ExpectedContent: []string{tc.expected},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if location := res.Header.Get("Location"); location != "" {
					t.Fatalf("the refusal redirected to %q; an unvalidated URI must never be followed", location)
				}
			},
		})
		s.Test(t)
	}
}

// Once the client and its redirect URI are known, a bad request comes back as an
// OAuth error on the redirection, carrying the state and the iss.
func TestAuthorizeRedirectsItsErrors(t *testing.T) {
	cases := []struct {
		name string
		url  string
		code string
	}{
		{"no resource", authorizeURL(testClientID, url.Values{"resource": {""}}), "invalid_request"},
		{"no code_challenge", authorizeURL(testClientID, url.Values{"code_challenge": {""}}), "invalid_request"},
		{"plain PKCE", authorizeURL(testClientID, url.Values{"code_challenge_method": {"plain"}}), "invalid_request"},
		{"wrong response_type", authorizeURL(testClientID, url.Values{"response_type": {"token"}}), "unsupported_response_type"},
		{"foreign resource", authorizeURL(testClientID, url.Values{"resource": {"https://elsewhere.test/mcp"}}), "invalid_target"},
	}
	for _, tc := range cases {
		app := oauthApp(t)
		seedOAuthClient(t, app, testRedirectURI)
		s := oauthScenario(app, tests.ApiScenario{
			Name:           tc.name,
			Method:         http.MethodGet,
			URL:            tc.url,
			ExpectedStatus: 302,
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				location, err := url.Parse(res.Header.Get("Location"))
				if err != nil {
					t.Fatalf("the Location header is not a URL: %v", err)
				}
				if !strings.HasPrefix(location.String(), testRedirectURI) {
					t.Fatalf("redirected to %q, want the registered redirect URI", location)
				}
				query := location.Query()
				if query.Get("error") != tc.code {
					t.Fatalf("error = %q, want %q", query.Get("error"), tc.code)
				}
				if query.Get("state") != testState {
					t.Fatalf("state = %q, want it echoed back", query.Get("state"))
				}
				// RFC 9207, on error responses too: a client that cannot tell
				// which server answered cannot spot a mix-up.
				if query.Get("iss") != testOAuthConfig.Issuer {
					t.Fatalf("iss = %q, want %q", query.Get("iss"), testOAuthConfig.Issuer)
				}
				if query.Has("code") {
					t.Fatal("an error response carried a code")
				}
				count, err := app.CountRecords(faasboxOAuthGrantsCollection)
				if err != nil || count != 0 {
					t.Fatalf("got %d grants (err %v), want none recorded for a refused request", count, err)
				}
			},
		})
		s.Test(t)
	}
}

func TestAuthorizeCreatesAPendingRequest(t *testing.T) {
	app := oauthApp(t)
	seedOAuthClient(t, app, testRedirectURI)
	s := oauthScenario(app, tests.ApiScenario{
		Name:   "a valid authorization request",
		Method: http.MethodGet,
		// A loopback port the client never registered: RFC 8252 lets it float.
		URL:            authorizeURL(testClientID, url.Values{"redirect_uri": {"http://127.0.0.1:9999/callback"}}),
		ExpectedStatus: 302,
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			grants, err := app.FindAllRecords(faasboxOAuthGrantsCollection)
			if err != nil || len(grants) != 1 {
				t.Fatalf("got %d grants (err %v), want 1", len(grants), err)
			}
			grant := grants[0]
			if got := grant.GetString("status"); got != grantPending {
				t.Fatalf("status = %q, want %q", got, grantPending)
			}
			if got := grant.GetString("redirectUri"); got != "http://127.0.0.1:9999/callback" {
				t.Fatalf("stored redirectUri = %q", got)
			}
			want := "/consent?request=" + grant.Id
			if got := res.Header.Get("Location"); got != want {
				t.Fatalf("Location = %q, want %q", got, want)
			}
		},
	})
	s.Test(t)
}

func TestConsentRequestNeedsASuperuser(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantPending)
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "read a request without a session",
		Method:          http.MethodGet,
		URL:             "/api/faasbox/oauth/requests/" + grant.Id,
		ExpectedStatus:  401,
		ExpectedContent: []string{`"status":401`},
	})
	s.Test(t)
}

func TestConsentRequestSaysWhatItGrants(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantPending)
	s := oauthScenario(app, tests.ApiScenario{
		Name:           "read a request as a superuser",
		Method:         http.MethodGet,
		URL:            "/api/faasbox/oauth/requests/" + grant.Id,
		Headers:        map[string]string{"Authorization": superuserToken},
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"client":"test agent"`,
			`"redirectUri":"` + testRedirectURI + `"`,
			`"scope":"faasbox"`,
			// The screen has nothing to tick, so it has to say what it gives.
			`Create, rewrite and delete any function`,
		},
	})
	s.Test(t)
}

func TestConsentRefusalEmitsNoCode(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantPending)
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "refuse the request",
		Method:          http.MethodPost,
		URL:             "/api/faasbox/oauth/requests/" + grant.Id + "/decision",
		Body:            strings.NewReader(`{"approve":false}`),
		Headers:         map[string]string{"Authorization": superuserToken},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"redirectUrl":"`, `error=access_denied`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			redirect := decisionRedirect(t, res)
			query := redirect.Query()
			if query.Get("state") != testState || query.Get("iss") != testOAuthConfig.Issuer {
				t.Fatalf("the refusal lost state or iss: %q", redirect)
			}
			if query.Has("code") {
				t.Fatal("a refusal emitted an authorization code")
			}
			stored := reloadGrant(t, app, grant.Id)
			if stored.GetString("status") != grantRevoked {
				t.Fatalf("status = %q, want %q", stored.GetString("status"), grantRevoked)
			}
			if stored.GetString("codeHash") != "" {
				t.Fatal("a refused request stored a code hash")
			}
		},
	})
	s.Test(t)
}

func TestConsentApprovalIssuesAHashedCode(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantPending)
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "approve the request",
		Method:          http.MethodPost,
		URL:             "/api/faasbox/oauth/requests/" + grant.Id + "/decision",
		Body:            strings.NewReader(`{"approve":true}`),
		Headers:         map[string]string{"Authorization": superuserToken},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"redirectUrl":"`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			query := decisionRedirect(t, res).Query()
			code := query.Get("code")
			if code == "" {
				t.Fatal("the approval emitted no code")
			}
			if query.Get("state") != testState || query.Get("iss") != testOAuthConfig.Issuer {
				t.Fatal("the approval lost state or iss")
			}

			stored := reloadGrant(t, app, grant.Id)
			if stored.GetString("status") != grantCode {
				t.Fatalf("status = %q, want %q", stored.GetString("status"), grantCode)
			}
			if stored.GetString("codeHash") == code {
				t.Fatal("the code is stored in clear")
			}
			if stored.GetString("codeHash") != hashOAuthSecret(code) {
				t.Fatal("the stored hash is not the hash of the code that was handed out")
			}
			// One minute, and not a second of the ten-minute request window.
			expiry := stored.GetDateTime("codeExpiresAt")
			if expiry.IsZero() || expiry.Time().After(time.Now().Add(oauthCodeTTL+5*time.Second)) {
				t.Fatalf("codeExpiresAt = %v, want about a minute out", expiry)
			}
		},
	})
	s.Test(t)
}

// decisionRedirect reads the URL the consent endpoint hands back.
func decisionRedirect(t testing.TB, res *http.Response) *url.URL {
	t.Helper()
	var body struct {
		RedirectUrl string `json:"redirectUrl"`
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the decision response is not JSON: %v", err)
	}
	parsed, err := url.Parse(body.RedirectUrl)
	if err != nil {
		t.Fatalf("redirectUrl is not a URL: %v", err)
	}
	return parsed
}
