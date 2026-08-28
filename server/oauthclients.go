package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// Dynamic client registration, and the rule that says where a client may be sent
// back to.
//
// **RFC 7591 is deprecated by the 2026-07-28 revision of the MCP specification**,
// in favour of Client ID Metadata Documents. It is implemented anyway, and that
// is a deliberate divergence rather than an oversight: the ecosystem has not
// followed. Claude Code and Codex only know in practice how to connect to a
// server that exposes /register, and Claude Code has been observed attempting
// registration even when handed a client_id. A server exposing only the
// recommended mechanism would be unreachable by the very agents this endpoint
// exists for. Revisit when the clients do.
//
// The endpoint is **unauthenticated**, as RFC 7591 requires. Anyone can create a
// registration; a registration is not access. What a client holds after
// registering is a name and a list of URLs — nothing happens until a human clicks
// Authorize on the consent screen. The purge in oauthgrants.go bounds the growth
// that openness allows.
//
// No client_secret is issued and none is accepted. An agent running on the user's
// machine cannot keep one: it would sit in a readable file, or be identical for
// every user of that agent. PKCE is what secures the exchange.

const faasboxOAuthClientsCollection = "faasbox_oauth_clients"

// clientIDLen is the length of the identifier handed back. It is not a secret —
// it travels in a URL the user can read — so it carries no fbo_ prefix.
const clientIDLen = 32

// ensureOAuthClientsCollection creates faasbox_oauth_clients if it is absent.
//
// It stays separate from faasbox_oauth_grants on purpose. This table is written
// by strangers and holds nothing but names and URLs; the other one holds the
// material that opens the instance. A single table would make an access rule
// opened by mistake in the admin, a badly written purge filter, or an endpoint
// that lists reach both at once. Their lifetimes differ too: a client lives
// indefinitely, a grant expires and is revoked.
//
// No API rule is set, so the collection is superuser-only, like faasbox_api_keys.
func ensureOAuthClientsCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(faasboxOAuthClientsCollection); err == nil {
		return nil
	}

	col := core.NewBaseCollection(faasboxOAuthClientsCollection)
	col.Fields.Add(
		&core.TextField{Name: "clientId", Required: true},
		// The fingerprint of the identifier, and what carries its uniqueness: the
		// identifier itself is sealed, so an index on it would constrain nothing
		// and a lookup on it would find nothing.
		&core.TextField{Name: "clientIdHash"},
		&core.TextField{Name: "name"},
		&core.JSONField{Name: "redirectUris"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)
	col.AddIndex("idx_faasbox_oauth_clients_clientIdHash", true, "clientIdHash", "")

	return app.Save(col)
}

// registerClientHandler answers POST /oauth/register.
func registerClientHandler(e *core.RequestEvent) error {
	var metadata oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(e.Request.Body).Decode(&metadata); err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_client_metadata",
			"the request body is not valid JSON")
	}

	if err := validateRedirectURIs(metadata.RedirectURIs); err != nil {
		return oauthFailure(e, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
	}

	col, err := e.App.FindCollectionByNameOrId(faasboxOAuthClientsCollection)
	if err != nil {
		e.App.Logger().Error("faasbox: cannot find the OAuth clients collection", "error", err)
		return oauthFailure(e, http.StatusInternalServerError, "server_error",
			"failed to register the client")
	}

	clientId := security.RandomString(clientIDLen)
	record := core.NewRecord(col)
	record.Set("clientId", clientId)
	record.Set("name", strings.TrimSpace(metadata.ClientName))
	record.Set("redirectUris", metadata.RedirectURIs)
	if err := e.App.Save(record); err != nil {
		e.App.Logger().Error("faasbox: failed to register an OAuth client", "error", err)
		return oauthFailure(e, http.StatusInternalServerError, "server_error",
			"failed to register the client")
	}

	// The response echoes the registered metadata, with the one field the server
	// decides rather than the client: this is a public client, so the token
	// endpoint authenticates nobody. No client_secret is issued, and the field is
	// omitted rather than sent empty.
	metadata.TokenEndpointAuthMethod = "none"
	noStore(e)
	return e.JSON(http.StatusCreated, &oauthex.ClientRegistrationResponse{
		ClientRegistrationMetadata: metadata,
		ClientID:                   clientId,
		ClientIDIssuedAt:           time.Now(),
	})
}

// validateRedirectURIs refuses a registration that could not be redirected to.
//
// At least one URI is required — a client with none can never complete a flow,
// and accepting the registration would only move the refusal to /authorize. Each
// one has to be absolute and carry no fragment, which OAuth 2.1 forbids on a
// redirect URI. The three scripting schemes are refused outright: they are XSS
// vectors the moment anything renders the value.
func validateRedirectURIs(uris []string) error {
	if len(uris) == 0 {
		return errors.New(`"redirect_uris" is required and must list at least one URI`)
	}
	for _, raw := range uris {
		parsed, err := url.Parse(raw)
		if err != nil || !parsed.IsAbs() {
			return fmt.Errorf("%q is not an absolute redirect URI", raw)
		}
		if parsed.Fragment != "" {
			return fmt.Errorf("%q carries a fragment, which a redirect URI may not", raw)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "javascript", "data", "vbscript":
			return fmt.Errorf("%q uses a scheme that is not allowed for a redirect URI", raw)
		}
	}
	return nil
}

// findOAuthClient returns the registration a client_id designates.
//
// The search is on the fingerprint, the identifier being sealed: two writings of
// the same value differ, so no equality on the column itself can ever hold.
func findOAuthClient(app core.App, clientId string) (*core.Record, error) {
	if clientId == "" {
		return nil, errors.New("no client_id")
	}
	return app.FindFirstRecordByData(faasboxOAuthClientsCollection, "clientIdHash", blindIndex(clientId))
}

// oauthClientId is the only way to read the identifier of a registration. It is
// encrypted at rest like the name beside it; what SQL finds it by is the
// fingerprint, never this value.
func oauthClientId(app core.App, record *core.Record) string {
	return decryptedText(app, record, "clientId")
}

// clientDisplayName is what the consent screen shows. A registration may carry no
// name — client_name is only RECOMMENDED — and the identifier is then the only
// honest thing to put in front of the user.
//
// Both columns are encrypted at rest, so both are read through their accessor:
// what the user must not be shown, on the one screen where they decide whether
// to trust a stranger, is base64.
func clientDisplayName(app core.App, record *core.Record) string {
	if name := strings.TrimSpace(decryptedText(app, record, "name")); name != "" {
		return name
	}
	return oauthClientId(app, record)
}

// clientRedirectURIs is the only way to read the registered redirect URIs.
//
// It exists because the column is a JSON list that is now stored as a sealed
// string: record.GetStringSlice on it hands back nothing resembling the list,
// redirectURIAllowed then matches nothing, and **every /authorize is refused
// from the first one**. The failure is loud rather than silent, but it is total.
func clientRedirectURIs(app core.App, record *core.Record) []string {
	payload := decryptedJSON(app, record, "redirectUris")
	if payload == "" {
		return nil
	}

	var uris []string
	if err := json.Unmarshal([]byte(payload), &uris); err != nil {
		app.Logger().Error("faasbox: an OAuth client carries unreadable redirect URIs",
			"client", record.Id, "error", err)
		return nil
	}
	return uris
}

// redirectURIAllowed reports whether a client may be sent back to a URI.
//
// The comparison is exact, with one documented exception. Comparing by prefix is
// the open redirect itself: a registered https://app.example/cb would accept
// https://app.example/cb.attacker.test. Nothing here compares prefixes.
//
// The exception is the loopback interface. A command-line agent listens on an
// ephemeral port it cannot know at registration time, so RFC 8252 §7.3 has the
// server ignore the port — and only the port — for http:// URIs on a loopback
// host. Everything else, scheme, host and path included, still has to match.
func redirectURIAllowed(registered []string, candidate string) bool {
	if candidate == "" {
		return false
	}
	parsed, err := url.Parse(candidate)
	if err != nil || !parsed.IsAbs() || parsed.Fragment != "" {
		return false
	}
	for _, known := range registered {
		if known == candidate {
			return true
		}
		if loopbackSiblings(known, candidate) {
			return true
		}
	}
	return false
}

// loopbackSiblings reports whether two loopback redirect URIs differ by their
// port and by nothing else.
func loopbackSiblings(registered, candidate string) bool {
	a, errA := url.Parse(registered)
	b, errB := url.Parse(candidate)
	if errA != nil || errB != nil {
		return false
	}
	if a.Scheme != "http" || b.Scheme != "http" {
		return false
	}
	if !isLoopbackHost(a.Host) || !isLoopbackHost(b.Host) {
		return false
	}
	// The host name still has to match: 127.0.0.1 and localhost are different
	// registrations, and RFC 8252 asks a client to use the literal address it
	// registered.
	if !strings.EqualFold(hostWithoutPort(a.Host), hostWithoutPort(b.Host)) {
		return false
	}
	return a.Path == b.Path && a.RawQuery == b.RawQuery
}
