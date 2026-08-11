package main

import (
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// A grant is one authorization, from the request that opens it to the tokens it
// ends up carrying. This file holds the collection, every write that moves a
// grant from one state to the next, and the purge that bounds them all. The
// endpoint that creates one is oauthauthorize.go; the endpoints that spend one
// are oauthconsent.go and oauthtoken.go.
//
// The state machine is pending -> code -> active -> revoked, and the
// authorization codes live here rather than in a third table because they are
// the first state of an authorization, not an object of their own.
//
// **Why /oauth/authorize cannot be the consent page.** The SPA is served by the
// catch-all /{path...} in main.go, so any server route wins over the Angular
// route of the same path — the trap already documented for /functions and /mcp.
// And the superuser session lives in localStorage, put on the wire as an
// Authorization header by the Angular interceptor: a top-level navigation arrives
// bare, so the server *cannot* gate GET /oauth/authorize on a session.
//
// Hence three steps. /oauth/authorize validates the request with no
// authentication, creates a pending grant, and redirects to /consent?request=<id>
// — a path no server route claims. The page is behind the existing authGuard,
// reads the request through an authenticated call, and posts the decision. The
// request id is opaque: the SPA never carries an OAuth parameter it could alter,
// and validation happens exactly once, at the authorization endpoint.

const faasboxOAuthGrantsCollection = "faasbox_oauth_grants"

// The four states of a grant. There is no separate revocation flag: a second
// spelling of the same fact is a second thing to keep in step.
const (
	grantPending = "pending"
	grantCode    = "code"
	grantActive  = "active"
	grantRevoked = "revoked"
)

// ensureOAuthGrantsCollection creates faasbox_oauth_grants if it is absent.
//
// It requires faasbox_oauth_clients to exist already — the relation field needs
// the id of the collection it points at — which is the order mountOAuth uses.
//
// Every credential column stores a SHA-256 and is marked Hidden. A stolen
// database must not yield a usable token, and the raw value never touches a
// record.
func ensureOAuthGrantsCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(faasboxOAuthGrantsCollection); err == nil {
		return nil
	}

	clients, err := app.FindCollectionByNameOrId(faasboxOAuthClientsCollection)
	if err != nil {
		return fmt.Errorf("collection %s not found: %w", faasboxOAuthClientsCollection, err)
	}

	col := core.NewBaseCollection(faasboxOAuthGrantsCollection)
	col.Fields.Add(
		&core.RelationField{
			Name:          "client",
			Required:      true,
			MaxSelect:     1,
			CascadeDelete: true,
			CollectionId:  clients.Id,
		},
		&core.SelectField{
			Name:     "status",
			Required: true,
			Values:   []string{grantPending, grantCode, grantActive, grantRevoked},
		},
		&core.TextField{Name: "redirectUri"},
		&core.TextField{Name: "codeChallenge"},
		&core.TextField{Name: "resource"},
		&core.TextField{Name: "state"},
		&core.TextField{Name: "codeHash", Hidden: true},
		&core.TextField{Name: "accessTokenHash", Hidden: true},
		&core.TextField{Name: "refreshTokenHash", Hidden: true},
		// One rotation back. Presenting it is how a stolen refresh token
		// announces itself — see oauthtoken.go.
		&core.TextField{Name: "previousRefreshHash", Hidden: true},
		&core.DateField{Name: "requestExpiresAt"},
		&core.DateField{Name: "codeExpiresAt"},
		&core.DateField{Name: "accessExpiresAt"},
		&core.DateField{Name: "refreshExpiresAt"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)

	// Not unique: every one of these columns is empty on most rows, and SQLite
	// treats two empty strings as equal.
	col.AddIndex("idx_faasbox_oauth_grants_codeHash", false, "codeHash", "")
	col.AddIndex("idx_faasbox_oauth_grants_refreshTokenHash", false, "refreshTokenHash", "")
	col.AddIndex("idx_faasbox_oauth_grants_previousRefreshHash", false, "previousRefreshHash", "")

	return app.Save(col)
}

// findGrantByHash returns the grant whose column holds this hash.
//
// An empty hash is refused rather than looked up: most rows carry an empty string
// in most of these columns, so the query would match an unrelated grant.
func findGrantByHash(app core.App, column, hash string) (*core.Record, error) {
	if hash == "" {
		return nil, fmt.Errorf("no %s to look up", column)
	}
	return app.FindFirstRecordByData(faasboxOAuthGrantsCollection, column, hash)
}

// The two writes below move a grant from one state to the next, and both go
// through a **targeted SQL statement** rather than through app.Save.
//
// The reason is the same for both, and it is not performance. A grant is read,
// weighed, and written back; between the read and the write another request can
// have moved it. app.Save rewrites every column from the record in memory, so the
// second write silently undoes the first — a revocation would erase the tokens
// the winner just issued, and two exchanges of one code would each believe they
// were the only one. Naming the condition in the statement that writes is what
// closes that window; SQLite evaluates it and reports how many rows it touched.
//
// Neither collection carries a hook, so nothing is skipped by going around the
// record layer. `updated` is stamped by hand for the same reason: the autodate
// field is applied by app.Save, which is precisely what is not being used. The
// project already writes this way where the same argument holds — markCronJobRun
// and pruneOldLogs.

// revokeGrant kills an authorization whole.
//
// The token hashes are left in place: a later replay then still resolves to this
// grant and is refused by its state, rather than looking like an unknown
// credential. Only the status moves, so a stale record in the caller's hand
// cannot clobber anything.
func revokeGrant(app core.App, grantId, reason string) {
	result, err := app.DB().NewQuery(
		"UPDATE " + faasboxOAuthGrantsCollection +
			" SET status = {:revoked}, updated = {:now}" +
			" WHERE id = {:id} AND status != {:revoked}",
	).Bind(dbx.Params{
		"revoked": grantRevoked,
		"now":     types.NowDateTime().String(),
		"id":      grantId,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to revoke an OAuth grant",
			"grant", grantId, "reason", reason, "error", err)
		return
	}
	// Already revoked: the row is untouched and there is nothing new to report.
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return
	}
	app.Logger().Warn("faasbox: OAuth grant revoked", "grant", grantId, "reason", reason)
}

// grantGuard is the condition a claim must still find true to win. It names the
// column the caller matched the grant on — the status for a code exchange, the
// refresh hash for a rotation — and the value it expects to still be there.
//
// Both fields are package constants or values read from this same row; neither is
// ever caller input, which is what makes it safe to build the column name into
// the statement.
type grantGuard struct {
	Column string
	Value  string
}

// issuedTokens is the pair a claim stores, already hashed by the caller.
type issuedTokens struct {
	AccessHash  string
	RefreshHash string
	// RetiredHash is the refresh hash this claim consumes, empty on a first
	// issue. One rotation of memory is what turns a stale refresh token into a
	// signal instead of an unknown credential.
	RetiredHash string
}

// claimGrant writes the new credentials and reports whether this request is the
// one that won the guard. A false return is not an error: it means another
// request got there first, which for both callers is a credential presented
// twice.
func claimGrant(app core.App, grantId string, guard grantGuard, tokens issuedTokens) (bool, error) {
	now := types.NowDateTime()
	result, err := app.DB().NewQuery(
		"UPDATE " + faasboxOAuthGrantsCollection + " SET " +
			"status = {:status}, accessTokenHash = {:access}, refreshTokenHash = {:refresh}, " +
			"previousRefreshHash = {:retired}, accessExpiresAt = {:accessAt}, " +
			"refreshExpiresAt = {:refreshAt}, updated = {:now} " +
			"WHERE id = {:id} AND " + guard.Column + " = {:guard}",
	).Bind(dbx.Params{
		"status":    grantActive,
		"access":    tokens.AccessHash,
		"refresh":   tokens.RefreshHash,
		"retired":   tokens.RetiredHash,
		"accessAt":  now.Add(oauthAccessTTL).String(),
		"refreshAt": now.Add(oauthRefreshTTL).String(),
		"now":       now.String(),
		"id":        grantId,
		"guard":     guard.Value,
	}).Execute()
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// sameResource compares a requested resource with the one this server protects.
// A trailing slash is the one difference tolerated: clients build the value from
// the discovery document, and a stray slash is not a different resource.
func sameResource(requested, own string) bool {
	return strings.TrimSuffix(requested, "/") == strings.TrimSuffix(own, "/")
}

// grantExpired reports whether a date column is set and already past.
func grantExpired(grant *core.Record, column string) bool {
	at := grant.GetDateTime(column)
	return !at.IsZero() && at.Before(types.NowDateTime())
}

// pruneOAuthRecords is the second task of the hourly internal cron.
//
// /oauth/register being open, the client table would grow without end, and dead
// grants would pile up beside it. It returns silently when the collections are
// absent: OAuth is not mounted on this instance, and there is nothing to prune.
//
// Four kinds of dead row, and the second one is easy to forget: a grant that was
// approved and then abandoned sits at `code` with a code nobody will ever
// exchange. It is not pending, not revoked, and carries no refresh token, so the
// other three clauses walk straight past it. Nothing else collects it either —
// the revocation on an expired code only fires if somebody presents the code.
//
// Grants go first, clients after. A client whose every grant has just been
// collected is a client nothing follows any more, and the twenty-four hour grace
// keeps a registration that is still mid-flow.
func pruneOAuthRecords(app core.App) {
	if _, err := app.FindCollectionByNameOrId(faasboxOAuthGrantsCollection); err != nil {
		return
	}

	now := types.NowDateTime()
	_, err := app.DB().NewQuery(
		"DELETE FROM " + faasboxOAuthGrantsCollection + " WHERE " +
			"(status = {:pending} AND requestExpiresAt != '' AND requestExpiresAt < {:now}) " +
			"OR (status = {:code} AND codeExpiresAt != '' AND codeExpiresAt < {:now}) " +
			"OR status = {:revoked} " +
			"OR (refreshExpiresAt != '' AND refreshExpiresAt < {:now})",
	).Bind(dbx.Params{
		"pending": grantPending,
		"code":    grantCode,
		"revoked": grantRevoked,
		"now":     now,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to prune OAuth grants", "error", err)
		return
	}

	if _, err := app.FindCollectionByNameOrId(faasboxOAuthClientsCollection); err != nil {
		return
	}
	_, err = app.DB().NewQuery(
		"DELETE FROM " + faasboxOAuthClientsCollection + " WHERE created < {:cutoff} " +
			"AND id NOT IN (SELECT client FROM " + faasboxOAuthGrantsCollection + ")",
	).Bind(dbx.Params{"cutoff": now.Add(-oauthClientGrace)}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to prune OAuth clients", "error", err)
	}
}
