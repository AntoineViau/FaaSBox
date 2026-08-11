package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/pocketbase/pocketbase/core"
)

// The three documents a client reads before it talks to anything.
//
// A client hitting /mcp is told where to look by the WWW-Authenticate challenge
// on its 401 (cf. apikeys.go); from there it fetches the protected resource
// metadata (RFC 9728) to learn which authorization server guards the resource,
// then the authorization server metadata (RFC 8414) to learn where /authorize
// and /token are. All three are public and cacheable — they carry
// configuration, never a credential.
//
// The types come from the MCP SDK's oauthex package rather than from hand-written
// structs: the field names of a wire format are exactly the thing not to retype.

// protectedResourceMetadata describes /mcp and points at ourselves.
func protectedResourceMetadata(cfg oauthConfig) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               cfg.Resource,
		AuthorizationServers:   []string{cfg.Issuer},
		ScopesSupported:        []string{oauthScope},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "FaaSBox",
	}
}

// protectedResourceMetadataHandler serves that document. The SDK's own handler is
// used: it already answers the CORS preflight and refuses anything but GET.
func protectedResourceMetadataHandler(cfg oauthConfig) http.Handler {
	return auth.ProtectedResourceMetadataHandler(protectedResourceMetadata(cfg))
}

// authServerMetadata is what an OAuth client reads to drive the flow.
//
// Every declaration here is a commitment the rest of the implementation keeps:
// S256 and nothing else, no client authentication at the token endpoint because
// the client is public, and iss on every authorization response.
func authServerMetadata(cfg oauthConfig) *oauthex.AuthServerMeta {
	return &oauthex.AuthServerMeta{
		Issuer:                                     cfg.Issuer,
		AuthorizationEndpoint:                      cfg.authorizeEndpoint(),
		TokenEndpoint:                              cfg.tokenEndpoint(),
		RegistrationEndpoint:                       cfg.registerEndpoint(),
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		ScopesSupported:                            []string{oauthScope},
		AuthorizationResponseIssParameterSupported: true,
	}
}

// encodeAuthServerMeta serialises the document without its empty jwks_uri.
//
// AuthServerMeta declares that field without omitempty, and FaaSBox has no key
// set to publish: its tokens are opaque strings looked up in the database, not
// signed JWTs. Serving "jwks_uri": "" would hand a client an empty URL to fetch,
// which is worse than the absent optional field RFC 8414 allows.
func encodeAuthServerMeta(meta *oauthex.AuthServerMeta) ([]byte, error) {
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	if value, ok := document["jwks_uri"].(string); ok && value == "" {
		delete(document, "jwks_uri")
	}

	return json.Marshal(document)
}

// authServerMetadataHandler renders the document once, at mount time.
//
// It cannot change while the server runs — it is derived from a startup
// configuration — so encoding it per request would be work done to produce the
// same bytes. An encoding failure is a startup failure, not a 500 discovered by
// the first client.
func authServerMetadataHandler(cfg oauthConfig) (func(*core.RequestEvent) error, error) {
	body, err := encodeAuthServerMeta(authServerMetadata(cfg))
	if err != nil {
		return nil, fmt.Errorf("cannot encode the authorization server metadata: %w", err)
	}

	return func(e *core.RequestEvent) error {
		allowPublicMetadataOrigin(e.Response)
		return e.Blob(http.StatusOK, "application/json", body)
	}, nil
}
