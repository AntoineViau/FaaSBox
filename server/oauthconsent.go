package main

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The two endpoints the consent page talks to, and the sentences it shows.
//
// They are not part of the protocol a client speaks: they are the editor asking
// "who is this, and what would I be handing over" and then posting the click.
// Hence /api/faasbox/ rather than /oauth/, and hence the superuser guard — this
// is the one step of the flow where a human is required.
//
// They live apart from oauthgrants.go on the criterion the whole server uses: that
// file answers "what is an authorization and how does /authorize open one", this
// one "what does the person in front of the screen see and decide".

// oauthGrantedPowers is what a token is worth, spelled out for the user.
//
// **A token grants everything.** It is worth an unscoped key carrying canManage:
// every function of the instance, read and write. That is a decision and not an
// oversight — splitting the reach between several agents would mean mapping the
// two dimensions of a key onto OAuth scopes and handling step-up, for a
// single-user product where the agent is a personal tool.
//
// Because there is nothing to tick, the screen has to *say* what it gives. This
// list is the source of that wording, server-side, so the page and the contract
// cannot drift apart.
var oauthGrantedPowers = []string{
	"Read the code, the dependencies and the triggers of every function on this instance",
	"Create, rewrite and delete any function — which is code this server then executes",
	"Run any function, with that function's secrets in its environment",
	"Read the execution history of any function, including payloads and output of runs it did not trigger",
}

// consentRequest is what the page renders.
type consentRequest struct {
	Client      string   `json:"client"`
	RedirectUri string   `json:"redirectUri"`
	Scope       string   `json:"scope"`
	Grants      []string `json:"grants"`
}

// consentRequestHandler answers GET /api/faasbox/oauth/requests/{id}.
func consentRequestHandler(e *core.RequestEvent) error {
	grant, client, err := pendingConsentRequest(e)
	if err != nil {
		return err
	}

	return e.JSON(http.StatusOK, consentRequest{
		Client:      clientDisplayName(e.App, client),
		RedirectUri: grantRedirectURI(e.App, grant),
		Scope:       oauthScope,
		Grants:      oauthGrantedPowers,
	})
}

// consentDecisionHandler answers POST /api/faasbox/oauth/requests/{id}/decision.
//
// It hands back the redirection rather than performing it: the caller is the SPA,
// which follows the URL itself. Approving mints the code; refusing marks the
// grant revoked and emits no code at all.
func consentDecisionHandler(e *core.RequestEvent, cfg oauthConfig) error {
	grant, _, err := pendingConsentRequest(e)
	if err != nil {
		return err
	}

	var body struct {
		Approve bool `json:"approve"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
	}

	// Both opened before they are used: the URI is where the browser goes next,
	// and the state is what the client matches its own request on.
	redirectURI := grantRedirectURI(e.App, grant)
	state := grantState(e.App, grant)

	if !body.Approve {
		revokeGrant(e.App, grant.Id, "refused on the consent screen")
		return e.JSON(http.StatusOK, map[string]string{
			"redirectUrl": redirectWithError(redirectURI, "access_denied",
				"the request was refused", state, cfg.Issuer),
		})
	}

	// One use, one minute. The raw code exists only in the URL the browser is
	// about to follow; what is stored is its hash.
	code := newOAuthSecret()
	grant.Set("codeHash", hashOAuthSecret(code))
	grant.Set("status", grantCode)
	grant.Set("codeExpiresAt", types.NowDateTime().Add(oauthCodeTTL))
	if err := e.App.Save(grant); err != nil {
		e.App.Logger().Error("faasbox: failed to issue an OAuth authorization code",
			"grant", grant.Id, "error", err)
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to issue the authorization code",
		})
	}

	noStore(e)
	return e.JSON(http.StatusOK, map[string]string{
		"redirectUrl": appendQuery(redirectURI, url.Values{
			"code":  {code},
			"state": {state},
			"iss":   {cfg.Issuer},
		}),
	})
}

// pendingConsentRequest resolves the {id} segment to a request still awaiting a
// decision, and its client.
//
// A grant that was already decided, or whose window has passed, is answered 404
// rather than 409: it is not a pending request any more, and the page has nothing
// to show for it. The refusal is a *router.ApiError and never a rendered body —
// a helper that answers by writing would hand its caller a nil error and be
// mistaken for a success.
func pendingConsentRequest(e *core.RequestEvent) (*core.Record, *core.Record, error) {
	notFound := func() error {
		return e.NotFoundError("no pending authorization request", nil)
	}

	grant, err := e.App.FindRecordById(faasboxOAuthGrantsCollection, e.Request.PathValue("id"))
	if err != nil {
		return nil, nil, notFound()
	}
	if grant.GetString("status") != grantPending || grantExpired(grant, "requestExpiresAt") {
		return nil, nil, notFound()
	}

	client, err := e.App.FindRecordById(faasboxOAuthClientsCollection, grant.GetString("client"))
	if err != nil {
		e.App.Logger().Error("faasbox: an OAuth grant points at a client that is gone",
			"grant", grant.Id, "error", err)
		return nil, nil, notFound()
	}

	return grant, client, nil
}
