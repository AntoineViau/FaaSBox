package main

import (
	"net/http"
	"net/url"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The authorization endpoint: the one public route that turns a client's demand
// into a pending grant, and the point where an open redirect would live if the
// order of its checks were wrong.
//
// It is transport, on the line the rest of the server already draws between a
// handler and the thing it drives (manage.go / manageops.go, invoke.go /
// invokeops.go). What a grant *is*, and every write that moves one, lives in
// oauthgrants.go; what happens here is reading a query string, refusing it, and
// recording what survived.
//
// Why it redirects to /consent rather than rendering anything itself is in
// oauthgrants.go, at the head of the file: the server cannot see the session on a
// top-level navigation, and the SPA catch-all would lose to this very route.

// authorizeHandler answers GET /oauth/authorize.
//
// The order of the checks is the security of the endpoint. An unknown client_id
// or a redirect_uri that does not validate is answered **on the spot** and never
// redirected: redirecting to an unvalidated URI is the open redirect itself. Only
// once both are known does a failure become a redirection carrying an OAuth
// error, the state and the iss.
func authorizeHandler(e *core.RequestEvent, cfg oauthConfig) error {
	query := e.Request.URL.Query()

	client, err := findOAuthClient(e.App, query.Get("client_id"))
	if err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_client",
			"unknown client_id; register the client first")
	}

	redirectURI := query.Get("redirect_uri")
	if !redirectURIAllowed(client.GetStringSlice("redirectUris"), redirectURI) {
		return oauthFailure(e, http.StatusBadRequest, "invalid_request",
			`"redirect_uri" is missing or does not match a registered redirect URI`)
	}

	state := query.Get("state")
	deny := func(code, description string) error {
		return e.Redirect(http.StatusFound,
			redirectWithError(redirectURI, code, description, state, cfg.Issuer))
	}

	if responseType := query.Get("response_type"); responseType != "code" {
		return deny("unsupported_response_type", `"response_type" must be "code"`)
	}
	challenge := query.Get("code_challenge")
	if challenge == "" {
		return deny("invalid_request", `"code_challenge" is required: this server requires PKCE`)
	}
	if method := query.Get("code_challenge_method"); method != "S256" {
		return deny("invalid_request", `"code_challenge_method" must be "S256"`)
	}
	resource := query.Get("resource")
	if resource == "" {
		return deny("invalid_request", `"resource" is required (RFC 8707)`)
	}
	if !sameResource(resource, cfg.Resource) {
		return deny("invalid_target", "this server does not issue tokens for that resource")
	}

	col, err := e.App.FindCollectionByNameOrId(faasboxOAuthGrantsCollection)
	if err != nil {
		e.App.Logger().Error("faasbox: cannot find the OAuth grants collection", "error", err)
		return deny("server_error", "failed to record the authorization request")
	}

	grant := core.NewRecord(col)
	grant.Set("client", client.Id)
	grant.Set("status", grantPending)
	grant.Set("redirectUri", redirectURI)
	grant.Set("codeChallenge", challenge)
	// What the client asked for, kept as it asked for it: 0475 revalidates it
	// when the token is presented.
	grant.Set("resource", resource)
	grant.Set("state", state)
	grant.Set("requestExpiresAt", types.NowDateTime().Add(oauthRequestTTL))
	if err := e.App.Save(grant); err != nil {
		e.App.Logger().Error("faasbox: failed to record an OAuth authorization request", "error", err)
		return deny("server_error", "failed to record the authorization request")
	}

	return e.Redirect(http.StatusFound, "/consent?request="+url.QueryEscape(grant.Id))
}
