package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// FaaSBox is an OAuth 2.1 authorization server as well as a resource server, and
// this file is where that decision is wired: what the instance is called from
// outside, whether the endpoints go up at all, and the handful of primitives the
// four OAuth files share.
//
// It is both roles at once because there is no external identity here. The user
// who authorizes is the PocketBase superuser, already signed in to the editor;
// standing up a second identity provider for a single-user product would buy
// nothing. The MCP specification allows the combination explicitly.
//
// The public address comes from FAASBOX_PUBLIC_URL and from nowhere else. An
// authorization server has to publish its own issuer and the URL of the resource
// it protects, and nothing in the server computed an origin before. Two other
// sources were weighed and refused:
//
//   - PocketBase's AppURL defaults to http://localhost:8090 and lives in the
//     database rather than in the deployment configuration, so a first boot in
//     production would publish a discovery document pointing at localhost until
//     somebody thought to fix it in the admin;
//   - deriving it from X-Forwarded-* would make the identity of the server
//     depend on whatever a proxy feels like saying.
//
// Absent or invalid, the routes are simply not mounted: /mcp keeps answering to
// an API key and nothing is broken. That is the whole failure mode — and it is
// also what turns the token off, since a route only accepts one when it is
// handed the configuration this file derives (cf. oauthbearer.go).
//
// The tokens issued here are opaque and stored hashed. What one is worth on the
// way back in is oauthbearer.go's subject.

const (
	// publicURLEnv names the only source of this instance's public address.
	publicURLEnv = "FAASBOX_PUBLIC_URL"

	// oauthScope is the single scope this server issues. There is no selector on
	// the consent screen: a token grants everything a canManage key with an open
	// scope grants, and the screen says so in words.
	oauthScope = "faasbox"

	// oauthSecretPrefix marks a FaaSBox OAuth secret the way fbx_ marks an API
	// key. Codes, access tokens and refresh tokens share it: they are all
	// bearer strings this server minted, and they are told apart by the column
	// their hash is looked up in, never by their shape.
	oauthSecretPrefix = "fbo_"
	oauthSecretRandom = 40
	oauthSecretLen    = len(oauthSecretPrefix) + oauthSecretRandom

	// The four lifetimes of a grant.
	//
	// The pending window is what a user has to sign in and click; ten minutes
	// covers a login detour without leaving requests lying around. The code is
	// one minute and one use. The refresh token rotates on every use, which is
	// what OAuth 2.1 asks of a client that holds no secret — see oauthtoken.go.
	oauthRequestTTL = 10 * time.Minute
	oauthCodeTTL    = time.Minute
	oauthAccessTTL  = time.Hour
	oauthRefreshTTL = 90 * 24 * time.Hour

	// oauthClientGrace is how long an unused registration survives. /oauth/register
	// is open by specification, so the table would grow without end.
	oauthClientGrace = 24 * time.Hour
)

// oauthConfig is the public identity of this instance, derived once at startup.
type oauthConfig struct {
	// Issuer is the origin, normalised without a trailing slash.
	Issuer string
	// Resource is the protected resource this server issues tokens for.
	Resource string
}

func (c oauthConfig) authorizeEndpoint() string { return c.Issuer + "/oauth/authorize" }
func (c oauthConfig) tokenEndpoint() string     { return c.Issuer + "/oauth/token" }
func (c oauthConfig) registerEndpoint() string  { return c.Issuer + "/oauth/register" }

// bearerChallenge is the signpost a 401 carries: the address of the document
// that tells a client where to go and authenticate (RFC 9728 §5.1). Without it
// an agent handed nothing but a URL has no starting point at all.
//
// The zero configuration yields an empty string, which is how a route that
// takes API keys only, and an instance with no FAASBOX_PUBLIC_URL, both end up
// posting no challenge: announcing a discovery document that is not mounted
// would walk the agent into a wall.
func (c oauthConfig) bearerChallenge() string {
	if c.Issuer == "" {
		return ""
	}
	return `Bearer resource_metadata="` + c.Issuer + `/.well-known/oauth-protected-resource"`
}

// oauthConfigFromEnv reads FAASBOX_PUBLIC_URL. The error is the reason the OAuth
// routes will not be mounted, worded for whoever is reading the startup output.
func oauthConfigFromEnv() (oauthConfig, error) {
	return parsePublicURL(os.Getenv(publicURLEnv))
}

// parsePublicURL turns the configured address into an issuer and a resource, or
// says why it cannot.
//
// It insists on a bare origin. A path, a query or a fragment would end up in the
// issuer, which RFC 8414 forbids, and credentials would be dropped silently by
// the normalisation below — a silent change to the identity of the server is
// worse than a refusal. http:// is accepted on a loopback host only: anywhere
// else it is what a misconfigured proxy looks like, and OAuth 2.1 forbids it.
func parsePublicURL(raw string) (oauthConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return oauthConfig{}, errors.New(publicURLEnv + " is not set")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return oauthConfig{}, fmt.Errorf("%s=%q cannot be parsed: %w", publicURLEnv, raw, err)
	}

	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return oauthConfig{}, fmt.Errorf("%s=%q is not an absolute http:// or https:// URL", publicURLEnv, raw)
	case u.Host == "":
		return oauthConfig{}, fmt.Errorf("%s=%q carries no host, so it is not an absolute URL", publicURLEnv, raw)
	case u.User != nil:
		return oauthConfig{}, fmt.Errorf("%s=%q carries credentials; it must be a bare origin", publicURLEnv, raw)
	case u.Path != "" && u.Path != "/":
		return oauthConfig{}, fmt.Errorf("%s=%q carries a path; it must be a bare origin", publicURLEnv, raw)
	case u.RawQuery != "" || u.ForceQuery:
		return oauthConfig{}, fmt.Errorf("%s=%q carries a query; it must be a bare origin", publicURLEnv, raw)
	case u.Fragment != "":
		return oauthConfig{}, fmt.Errorf("%s=%q carries a fragment; it must be a bare origin", publicURLEnv, raw)
	case u.Scheme == "http" && !isLoopbackHost(u.Host):
		return oauthConfig{}, fmt.Errorf(
			"%s=%q uses http:// on a host that is not a loopback address; OAuth 2.1 forbids it, "+
				"and it usually means a reverse proxy is wired wrong", publicURLEnv, raw)
	}

	issuer := u.Scheme + "://" + u.Host
	return oauthConfig{Issuer: issuer, Resource: issuer + "/mcp"}, nil
}

// mountOAuth creates the two collections and registers every OAuth route.
//
// The three discovery documents, /oauth/register and /oauth/authorize are
// public: a client that has not been authorized yet has no credential to present,
// and RFC 7591 requires an unauthenticated registration endpoint. Registering is
// not access — a registered client holds nothing until a human clicks Authorize.
//
// The two consent endpoints are superuser-only, because they are the click. They
// live under /api/faasbox/ rather than /oauth/ for the same reason the consent
// page is not served at /oauth/authorize: they are the editor talking to its own
// server, not part of the protocol a client speaks.
func mountOAuth(e *core.ServeEvent, cfg oauthConfig) error {
	if err := ensureOAuthClientsCollection(e.App); err != nil {
		return fmt.Errorf("failed to create %s collection: %w", faasboxOAuthClientsCollection, err)
	}
	if err := ensureOAuthGrantsCollection(e.App); err != nil {
		return fmt.Errorf("failed to create %s collection: %w", faasboxOAuthGrantsCollection, err)
	}

	resourceMeta := apis.WrapStdHandler(protectedResourceMetadataHandler(cfg))
	e.Router.GET("/.well-known/oauth-protected-resource", resourceMeta)
	// RFC 9728 §3: a resource served under a path is discovered with that path
	// inserted after the well-known segment. Same document either way.
	e.Router.GET("/.well-known/oauth-protected-resource/mcp", resourceMeta)

	serverMeta, err := authServerMetadataHandler(cfg)
	if err != nil {
		return fmt.Errorf("failed to build the authorization server metadata: %w", err)
	}
	e.Router.GET("/.well-known/oauth-authorization-server", serverMeta)

	e.Router.POST("/oauth/register", registerClientHandler)
	e.Router.GET("/oauth/authorize", func(re *core.RequestEvent) error {
		return authorizeHandler(re, cfg)
	})
	e.Router.POST("/oauth/token", tokenHandler)

	e.Router.GET("/api/faasbox/oauth/requests/{id}", consentRequestHandler).
		Bind(apis.RequireSuperuserAuth())
	e.Router.POST("/api/faasbox/oauth/requests/{id}/decision", func(re *core.RequestEvent) error {
		return consentDecisionHandler(re, cfg)
	}).Bind(apis.RequireSuperuserAuth())

	return nil
}

// reportOAuthDisabled says at startup why no OAuth route went up. It goes to the
// standard logger rather than app.Logger(): the batch handler would hold it, and
// this is the one line that explains an endpoint answering 404.
func reportOAuthDisabled(reason error) {
	log.Printf("faasbox: OAuth authorization is disabled — %v. /mcp keeps answering to an API key.", reason)
}

// newOAuthSecret mints a bearer string. Only its hash is ever stored.
func newOAuthSecret() string {
	return oauthSecretPrefix + security.RandomString(oauthSecretRandom)
}

// hashOAuthSecret is hashAPIKey under the name of what it hashes here. The two
// deliberately share one function: a stolen database must not yield a usable
// credential, whichever kind it is.
func hashOAuthSecret(raw string) string {
	return hashAPIKey(raw)
}

// verifyPKCE checks a code_verifier against the stored S256 challenge.
//
// S256 only, on every client. plain is not offered and not accepted: the client
// is public and holds no secret, so PKCE is the whole security of the exchange.
func verifyPKCE(challenge, verifier string) bool {
	if challenge == "" || verifier == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// isLoopbackHost reports whether a host — with or without a port — is a loopback
// address. It answers two questions: whether http:// is acceptable for the public
// URL, and whether a redirect URI may float its port (RFC 8252).
func isLoopbackHost(host string) bool {
	name := hostWithoutPort(host)
	if strings.EqualFold(name, "localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// hostWithoutPort strips the port and the brackets of an IPv6 literal.
func hostWithoutPort(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.Trim(host, "[]")
}

// oauthFailure renders an OAuth error object: the code the specification defines,
// and a description worded for whoever reads the response.
func oauthFailure(e *core.RequestEvent, status int, code, description string) error {
	return e.JSON(status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// redirectWithError builds the redirection an authorization failure takes once
// the client and its redirect URI are known.
//
// iss travels on every authorization response, error ones included (RFC 9207):
// a client that cannot tell which server answered cannot tell a mix-up attack
// from a normal reply.
func redirectWithError(redirectURI, code, description, state, issuer string) string {
	return appendQuery(redirectURI, url.Values{
		"error":             {code},
		"error_description": {description},
		"state":             {state},
		"iss":               {issuer},
	})
}

// appendQuery adds parameters to a redirect URI, keeping whatever query the
// client registered. Empty values are dropped: an absent state must not come
// back as state=.
func appendQuery(raw string, extra url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for key, values := range extra {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		q.Set(key, values[0])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// allowPublicMetadataOrigin opens a document to cross-origin reads. Discovery
// documents are public configuration by design (RFC 9728 §3.1), and a browser
// based client has to be able to fetch them.
func allowPublicMetadataOrigin(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}
