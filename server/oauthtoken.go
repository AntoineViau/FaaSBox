package main

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/pocketbase/pocketbase/core"
)

// The token endpoint: the code exchange, and the rotation that follows it.
//
// Two refusals here do more than refuse — they **revoke the grant whole**, and
// that is the point of the file:
//
//   - a code presented twice means two holders, and only one of them is the
//     client that asked for it;
//   - a refresh token presented after it has been rotated means the same thing.
//
// Rotation is what OAuth 2.1 asks of a client that holds no secret, and it is the
// only way a theft announces itself at all: the refresh token lives in a
// configuration file on the user's disk, so without rotation a copy of that file
// is a permanent, invisible access.
//
// The client authenticates with nothing. It is public by design (see
// oauthclients.go), so client_id is checked against the grant rather than
// verified as a credential, and PKCE carries the proof that the caller is the one
// who started the flow.

// The two ways a token request can name the wrong thing. They are worded for the
// caller: an agent reading them has to be able to fix its request.
var (
	errRequestedResourceMissing = errors.New(`"resource" is required (RFC 8707)`)
	errRequestedResourceForeign = errors.New(`"resource" is not the one this grant was issued for`)
)

// tokenHandler answers POST /oauth/token.
func tokenHandler(e *core.RequestEvent) error {
	oauthNoStore(e)

	if err := e.Request.ParseForm(); err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_request",
			"the request body must be application/x-www-form-urlencoded")
	}

	switch grantType := e.Request.PostForm.Get("grant_type"); grantType {
	case "authorization_code":
		return exchangeAuthorizationCode(e, e.Request.PostForm)
	case "refresh_token":
		return rotateRefreshToken(e, e.Request.PostForm)
	default:
		return oauthFailure(e, http.StatusBadRequest, "unsupported_grant_type",
			`"grant_type" must be "authorization_code" or "refresh_token"`)
	}
}

// exchangeAuthorizationCode turns a code into the first pair of tokens.
func exchangeAuthorizationCode(e *core.RequestEvent, form url.Values) error {
	code := form.Get("code")
	if code == "" {
		return oauthFailure(e, http.StatusBadRequest, "invalid_request", `"code" is required`)
	}

	grant, err := findGrantByHash(e.App, "codeHash", hashOAuthSecret(code))
	if err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"the authorization code is unknown")
	}

	// The hash is kept after a successful exchange precisely so this branch can
	// exist: a second presentation resolves to the grant instead of looking like
	// an unknown code, and the grant dies.
	if grant.GetString("status") != grantCode {
		return refuseReplayedCode(e, grant.Id)
	}

	if grantExpired(grant, "codeExpiresAt") {
		// Not evidence of theft, but the grant is dead either way. Marking it
		// revoked ends it now; the hourly purge collects grants left at `code`
		// whether or not anybody ever comes back with the code.
		revokeGrant(e.App, grant.Id, "the authorization code expired unused")
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"the authorization code has expired")
	}

	if !grantBelongsToClient(e, grant, form.Get("client_id")) {
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"this authorization code was not issued to that client_id")
	}
	if form.Get("redirect_uri") != grantRedirectURI(e.App, grant) {
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			`"redirect_uri" does not match the one the authorization request carried`)
	}
	if err := checkRequestedResource(e.App, grant, form.Get("resource")); err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_target", err.Error())
	}
	if !verifyPKCE(grantCodeChallenge(e.App, grant), form.Get("code_verifier")) {
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			`"code_verifier" does not match the code_challenge of the authorization request`)
	}

	// The guard is the status this exchange was just weighed against: if another
	// presentation of the same code moved it in the meantime, this one loses and
	// is the replay above, arriving a few microseconds earlier.
	err = issueTokens(e, grant.Id, grantGuard{Column: "status", Value: grantCode}, "")
	if !errors.Is(err, errGrantMoved) {
		return err
	}
	return refuseReplayedCode(e, grant.Id)
}

// refuseReplayedCode is the answer to a code presented a second time, whether the
// second presentation arrived after the first finished or during it.
func refuseReplayedCode(e *core.RequestEvent, grantId string) error {
	revokeGrant(e.App, grantId, "the authorization code was presented twice")
	return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
		"this authorization code has already been used; the grant has been revoked")
}

// rotateRefreshToken exchanges a refresh token for a fresh pair and retires the
// one it was handed.
func rotateRefreshToken(e *core.RequestEvent, form url.Values) error {
	raw := form.Get("refresh_token")
	if raw == "" {
		return oauthFailure(e, http.StatusBadRequest, "invalid_request", `"refresh_token" is required`)
	}
	hash := hashOAuthSecret(raw)

	grant, err := findGrantByHash(e.App, "refreshTokenHash", hash)
	if err != nil {
		// One rotation of memory is what turns a stale token into a signal
		// instead of a shrug.
		if previous, err := findGrantByHash(e.App, "previousRefreshHash", hash); err == nil {
			return refuseRotatedRefresh(e, previous.Id)
		}
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"the refresh token is unknown")
	}

	if grant.GetString("status") != grantActive {
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"this grant is no longer active")
	}
	if grantExpired(grant, "refreshExpiresAt") {
		revokeGrant(e.App, grant.Id, "the refresh token expired")
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"the refresh token has expired")
	}
	if !grantBelongsToClient(e, grant, form.Get("client_id")) {
		return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
			"this refresh token was not issued to that client_id")
	}
	if err := checkRequestedResource(e.App, grant, form.Get("resource")); err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_target", err.Error())
	}

	// The guard is the very hash this rotation consumes: a second presentation of
	// the same refresh token loses here and is treated as what it is.
	err = issueTokens(e, grant.Id, grantGuard{Column: "refreshTokenHash", Value: hash}, hash)
	if !errors.Is(err, errGrantMoved) {
		return err
	}
	return refuseRotatedRefresh(e, grant.Id)
}

// refuseRotatedRefresh is the answer to a refresh token presented after it has
// been retired, whether it was retired a day ago or in the same instant.
func refuseRotatedRefresh(e *core.RequestEvent, grantId string) error {
	revokeGrant(e.App, grantId, "a rotated refresh token was presented again")
	return oauthFailure(e, http.StatusBadRequest, "invalid_grant",
		"this refresh token has already been rotated; the grant has been revoked")
}

// errGrantMoved marks a claim that lost its guard: another request changed the
// grant between the checks above and the write. It is not a failure of the
// server, it is the second holder of a one-use credential — the caller turns it
// into the refusal its own vocabulary calls for.
var errGrantMoved = errors.New("the grant was claimed by another request")

// issueTokens mints a pair, stores their hashes under the guard, and answers.
//
// retired is the hash of the refresh token this call consumed, empty on the first
// issue. Keeping exactly one rotation back is what lets the next call tell a
// stale token from an unknown one.
//
// It returns errGrantMoved and writes nothing when the guard no longer holds.
// Every other outcome — the tokens, or a 500 — is already rendered, and the
// error it returns is nil.
func issueTokens(e *core.RequestEvent, grantId string, guard grantGuard, retired string) error {
	access := newOAuthSecret()
	refresh := newOAuthSecret()

	won, err := claimGrant(e.App, grantId, guard, issuedTokens{
		AccessHash:  hashOAuthSecret(access),
		RefreshHash: hashOAuthSecret(refresh),
		RetiredHash: retired,
	})
	if err != nil {
		e.App.Logger().Error("faasbox: failed to issue OAuth tokens", "grant", grantId, "error", err)
		return oauthFailure(e, http.StatusInternalServerError, "server_error",
			"failed to issue the tokens")
	}
	if !won {
		return errGrantMoved
	}

	// The scope is answered even though it was never negotiated: this server
	// issues one scope, so a client that asked for something else is told what it
	// actually got.
	return e.JSON(http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(oauthAccessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         oauthScope,
	})
}

// grantBelongsToClient reports whether a client_id names the client the grant was
// issued to. A missing client_id fails it: this server authenticates no client,
// so the identifier is the only tie there is.
func grantBelongsToClient(e *core.RequestEvent, grant *core.Record, clientId string) bool {
	if clientId == "" {
		return false
	}
	client, err := e.App.FindRecordById(faasboxOAuthClientsCollection, grant.GetString("client"))
	if err != nil {
		e.App.Logger().Error("faasbox: an OAuth grant points at a client that is gone",
			"grant", grant.Id, "error", err)
		return false
	}
	return client.GetString("clientId") == clientId
}

// checkRequestedResource enforces RFC 8707 on the token request: the parameter is
// required, and it has to be the resource the grant was opened for.
func checkRequestedResource(app core.App, grant *core.Record, requested string) error {
	if requested == "" {
		return errRequestedResourceMissing
	}
	if !sameResource(requested, grantResource(app, grant)) {
		return errRequestedResourceForeign
	}
	return nil
}
