package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// codeExchange is the complete, valid token request. A case that tests a refusal
// overrides exactly the field it is about.
func codeExchange(overrides url.Values) url.Values {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {testAuthorizationCode},
		"code_verifier": {testCodeVerifier},
		"client_id":     {testClientID},
		"redirect_uri":  {testRedirectURI},
		"resource":      {testOAuthConfig.Resource},
	}
	for key, value := range overrides {
		if len(value) == 0 || value[0] == "" {
			values.Del(key)
			continue
		}
		values.Set(key, value[0])
	}
	return values
}

// tokenResponse reads the pair the endpoint handed back.
func tokenResponse(t testing.TB, res *http.Response) (access, refresh string) {
	t.Helper()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the token response is not JSON: %v", err)
	}
	return body.AccessToken, body.RefreshToken
}

func TestTokenExchangesTheCode(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantCode)

	body, headers := tokenForm(codeExchange(nil))
	s := oauthScenario(app, tests.ApiScenario{
		Name:           "exchange an authorization code",
		Method:         http.MethodPost,
		URL:            "/oauth/token",
		Body:           body,
		Headers:        headers,
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"token_type":"Bearer"`,
			`"access_token":"fbo_`,
			`"refresh_token":"fbo_`,
			`"expires_in":3600`,
			`"scope":"faasbox"`,
		},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertNoStore(t, res)

			access, refresh := tokenResponse(t, res)
			stored := reloadGrant(t, app, grant.Id)
			if stored.GetString("status") != grantActive {
				t.Fatalf("status = %q, want %q", stored.GetString("status"), grantActive)
			}
			if stored.GetString("accessTokenHash") != hashOAuthSecret(access) {
				t.Fatal("the stored access token hash is not the hash of the token handed out")
			}
			if stored.GetString("refreshTokenHash") != hashOAuthSecret(refresh) {
				t.Fatal("the stored refresh token hash is not the hash of the token handed out")
			}
			// Nothing was rotated yet, so there is no previous token to remember.
			if stored.GetString("previousRefreshHash") != "" {
				t.Fatal("a first issue left a previous refresh hash behind")
			}
			assertNothingInClear(t, app, faasboxOAuthGrantsCollection, stored.Id,
				testAuthorizationCode, access, refresh)
		},
	})
	s.Test(t)
}

// A code presented twice means two holders, and only one of them asked for it.
func TestTokenReplayedCodeRevokesTheGrant(t *testing.T) {
	app := oauthApp(t)
	// grantActive keeps the code hash, which is exactly what lets the replay be
	// recognised instead of looking like an unknown code.
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantActive)

	body, headers := tokenForm(codeExchange(nil))
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "replay an authorization code",
		Method:          http.MethodPost,
		URL:             "/oauth/token",
		Body:            body,
		Headers:         headers,
		ExpectedStatus:  400,
		ExpectedContent: []string{`"error":"invalid_grant"`, `already been used`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			if got := reloadGrant(t, app, grant.Id).GetString("status"); got != grantRevoked {
				t.Fatalf("status = %q, want the whole grant revoked", got)
			}
		},
	})
	s.Test(t)
}

func TestTokenRefusesABadExchange(t *testing.T) {
	cases := []struct {
		name      string
		overrides url.Values
		code      string
		expected  string
	}{
		{"wrong code_verifier", url.Values{"code_verifier": {"not-the-verifier"}}, "invalid_grant", "code_verifier"},
		{"no resource", url.Values{"resource": {""}}, "invalid_target", "RFC 8707"},
		{"foreign resource", url.Values{"resource": {"https://elsewhere.test/mcp"}}, "invalid_target", "not the one"},
		{"unknown code", url.Values{"code": {"fbo_never-issued"}}, "invalid_grant", "unknown"},
		{"wrong client_id", url.Values{"client_id": {"someone-else"}}, "invalid_grant", "client_id"},
		{"wrong redirect_uri", url.Values{"redirect_uri": {"http://127.0.0.1:1/other"}}, "invalid_grant", "redirect_uri"},
		{"unsupported grant type", url.Values{"grant_type": {"password"}}, "unsupported_grant_type", "grant_type"},
	}
	for _, tc := range cases {
		app := oauthApp(t)
		grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantCode)

		body, headers := tokenForm(codeExchange(tc.overrides))
		s := oauthScenario(app, tests.ApiScenario{
			Name:            tc.name,
			Method:          http.MethodPost,
			URL:             "/oauth/token",
			Body:            body,
			Headers:         headers,
			ExpectedStatus:  400,
			ExpectedContent: []string{`"error":"` + tc.code + `"`, tc.expected},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// A refusal that is not a replay leaves the grant usable: the
				// caller may have simply got the request wrong.
				stored := reloadGrant(t, app, grant.Id)
				if stored.GetString("status") != grantCode {
					t.Fatalf("status = %q, want the code still exchangeable", stored.GetString("status"))
				}
				if stored.GetString("accessTokenHash") != "" {
					t.Fatal("a refused exchange issued a token")
				}
			},
		})
		s.Test(t)
	}
}

func TestTokenRefusesAnExpiredCode(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantCode)
	grant.Set("codeExpiresAt", types.NowDateTime().Add(-time.Second))
	if err := app.Save(grant); err != nil {
		t.Fatal(err)
	}

	body, headers := tokenForm(codeExchange(nil))
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "exchange an expired code",
		Method:          http.MethodPost,
		URL:             "/oauth/token",
		Body:            body,
		Headers:         headers,
		ExpectedStatus:  400,
		ExpectedContent: []string{`"error":"invalid_grant"`, `expired`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			// Not evidence of theft, but dead all the same — and marked so the
			// hourly pass collects it.
			if got := reloadGrant(t, app, grant.Id).GetString("status"); got != grantRevoked {
				t.Fatalf("status = %q, want %q", got, grantRevoked)
			}
		},
	})
	s.Test(t)
}

func TestTokenRotatesTheRefreshToken(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantActive)

	body, headers := tokenForm(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {testRefreshToken},
		"client_id":     {testClientID},
		"resource":      {testOAuthConfig.Resource},
	})
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "refresh",
		Method:          http.MethodPost,
		URL:             "/oauth/token",
		Body:            body,
		Headers:         headers,
		ExpectedStatus:  200,
		ExpectedContent: []string{`"access_token":"fbo_`, `"refresh_token":"fbo_`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			access, refresh := tokenResponse(t, res)
			if refresh == testRefreshToken {
				t.Fatal("the same refresh token came back; rotation did not happen")
			}
			stored := reloadGrant(t, app, grant.Id)
			if stored.GetString("refreshTokenHash") != hashOAuthSecret(refresh) {
				t.Fatal("the new refresh token was not stored")
			}
			if stored.GetString("accessTokenHash") != hashOAuthSecret(access) {
				t.Fatal("the new access token was not stored")
			}
			// One rotation back, which is what turns the next presentation of
			// the old token into a signal.
			if stored.GetString("previousRefreshHash") != hashOAuthSecret(testRefreshToken) {
				t.Fatal("the retired refresh token was not remembered")
			}
			assertNothingInClear(t, app, faasboxOAuthGrantsCollection, stored.Id,
				testRefreshToken, access, refresh)
		},
	})
	s.Test(t)
}

// The refresh token lives in a file on the user's disk. Rotation is the only way
// a copy of that file ever announces itself.
func TestTokenRotatedRefreshRevokesTheGrant(t *testing.T) {
	app := oauthApp(t)
	grant := seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantActive)
	grant.Set("previousRefreshHash", hashOAuthSecret("fbo_the-retired-one"))
	if err := app.Save(grant); err != nil {
		t.Fatal(err)
	}

	body, headers := tokenForm(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"fbo_the-retired-one"},
		"client_id":     {testClientID},
		"resource":      {testOAuthConfig.Resource},
	})
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "present a rotated refresh token",
		Method:          http.MethodPost,
		URL:             "/oauth/token",
		Body:            body,
		Headers:         headers,
		ExpectedStatus:  400,
		ExpectedContent: []string{`"error":"invalid_grant"`, `already been rotated`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			if got := reloadGrant(t, app, grant.Id).GetString("status"); got != grantRevoked {
				t.Fatalf("status = %q, want the whole grant revoked", got)
			}
		},
	})
	s.Test(t)
}

func TestTokenRefreshNeedsTheGrantResource(t *testing.T) {
	app := oauthApp(t)
	seedOAuthGrant(t, app, seedOAuthClient(t, app, testRedirectURI), grantActive)

	body, headers := tokenForm(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {testRefreshToken},
		"client_id":     {testClientID},
	})
	s := oauthScenario(app, tests.ApiScenario{
		Name:            "refresh without a resource",
		Method:          http.MethodPost,
		URL:             "/oauth/token",
		Body:            body,
		Headers:         headers,
		ExpectedStatus:  400,
		ExpectedContent: []string{`"error":"invalid_target"`, `RFC 8707`},
	})
	s.Test(t)
}

// assertNothingInClear reads the whole stored row and checks that no raw secret
// is anywhere in it. Reading the declared columns one by one would miss the day a
// value is copied into a field nobody thought about.
func assertNothingInClear(t testing.TB, app core.App, collection, id string, secrets ...string) {
	t.Helper()

	row := dbx.NullStringMap{}
	err := app.DB().NewQuery("SELECT * FROM " + collection + " WHERE id = {:id}").
		Bind(dbx.Params{"id": id}).One(row)
	if err != nil {
		t.Fatalf("failed to read the stored row: %v", err)
	}

	for column, value := range row {
		if !value.Valid || value.String == "" {
			continue
		}
		for _, secret := range secrets {
			if secret != "" && strings.Contains(value.String, secret) {
				t.Fatalf("column %q of %s holds a secret in clear", column, collection)
			}
		}
	}
}
