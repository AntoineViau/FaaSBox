package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// What an OAuth access token is worth on the way back in.
//
// **One authorization chain, not two.** The chain in place is three links long:
// requireAPIKey finds the credential and leaves an API key record in the request
// context, requireManageKey reads canManage off it, and exposeKeyScope carries it
// onto the standard context the MCP handler receives. A token that deposited an
// object of its own would need a second branch in each of those three links —
// and two branches saying the same thing end up not saying it the same way.
//
// So a valid token deposits **an API key record built in memory**: never saved,
// declaring no scope, carrying canManage. Downstream, nothing has to know
// whether the caller presented a key or a token. That is the literal translation
// of the decision the authorization server already published on its consent
// screen — a token is worth an unscoped key carrying canManage.
//
// The record must never reach the database. Saving one would create a phantom
// API key carrying canManage, which is to say a credential nobody minted and
// nobody can see the shape of. Nothing here calls app.Save, and nothing
// downstream writes the record it is handed.

// bearerScheme is the one authentication scheme this server reads off the
// Authorization header. Matching is case-insensitive: RFC 9110 says the scheme
// is, and clients do differ.
const bearerScheme = "bearer"

// errBearerRefused is what every failure of the validation below wraps. The
// wording of each one travels to the caller, but the class never changes: a
// token that does not pass is a 401, exactly like a key that does not pass.
var errBearerRefused = errors.New("invalid access token")

// bearerToken returns the credential of an Authorization: Bearer header, or an
// empty string when the header is absent or carries another scheme.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	scheme, credential, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return ""
	}
	return strings.TrimSpace(credential)
}

// bearerAPIKey validates an access token and returns the API key record it
// stands for.
//
// The checks follow those of a key, in the same order and with the same
// outcome — a refusal is a 401, and it never says which check caught it:
//
//  1. the prefix and length this server mints;
//  2. the grant carrying that access token hash;
//  3. a grant that is still active, which is what a revocation moves;
//  4. an access token that has not expired;
//  5. the audience — the resource the grant was opened for has to be the one
//     this server protects.
//
// The fifth is a second barrier. /oauth/authorize already refuses to open a
// grant for a foreign resource, but a guard posted at issue time alone protects
// nothing against a database that came from somewhere else — and it is exactly
// what stops a token minted for another MCP server from being replayed here.
func bearerAPIKey(app core.App, raw string, oauth oauthConfig) (*core.Record, error) {
	if len(raw) != oauthSecretLen || !strings.HasPrefix(raw, oauthSecretPrefix) {
		return nil, fmt.Errorf("%w: wrong shape", errBearerRefused)
	}

	// Looked up in the access token column alone, which is what refuses a
	// refresh token presented here: the two are the same kind of string and are
	// only ever told apart by the column their hash is sought in.
	grant, err := findGrantByHash(app, "accessTokenHash", hashOAuthSecret(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: unknown token", errBearerRefused)
	}

	if grant.GetString("status") != grantActive {
		return nil, fmt.Errorf("%w: the grant is no longer active", errBearerRefused)
	}

	// An access token with no expiry is refused rather than read as perpetual.
	// Every token this server issues carries one; a row that does not is not a
	// token it minted, and the lenient reading is the one that widens access.
	expiresAt := grant.GetDateTime("accessExpiresAt")
	if expiresAt.IsZero() || grantExpired(grant, "accessExpiresAt") {
		return nil, fmt.Errorf("%w: the access token has expired", errBearerRefused)
	}

	if !sameResource(grantResource(app, grant), oauth.Resource) {
		return nil, fmt.Errorf("%w: issued for another resource", errBearerRefused)
	}

	return oauthAPIKeyRecord(app, grant)
}

// oauthAPIKeyRecord builds the in-memory API key record a valid token stands
// for: no scope, canManage, and a name that identifies the agent in the logs.
//
// The name is what the existing logging reads (exposeKeyScope, and any refusal
// that names the key), so it carries the two things that identify one
// authorization: who registered, and which grant it is. It is never a
// credential, and the record is never saved.
func oauthAPIKeyRecord(app core.App, grant *core.Record) (*core.Record, error) {
	col, err := app.FindCollectionByNameOrId(faasboxAPIKeysCollection)
	if err != nil {
		return nil, fmt.Errorf("collection %s not found: %w", faasboxAPIKeysCollection, err)
	}

	record := core.NewRecord(col)
	record.Set("name", oauthGrantLabel(app, grant))
	record.Set("active", true)
	record.Set("canManage", true)
	// An empty list is what "no restriction" is spelled as everywhere else. It
	// is set rather than left out so the reading does not depend on what an
	// unset JSON field happens to yield.
	record.Set("allowedFunctions", []string{})
	return record, nil
}

// oauthGrantLabel names a grant for a human reading a log line: the client that
// registered, and the grant itself. A client that has gone leaves the grant id,
// which still says which authorization ran.
func oauthGrantLabel(app core.App, grant *core.Record) string {
	client, err := app.FindRecordById(faasboxOAuthClientsCollection, grant.GetString("client"))
	if err != nil {
		return "OAuth grant " + grant.Id
	}
	return fmt.Sprintf("%s (OAuth grant %s)", clientDisplayName(app, client), grant.Id)
}
