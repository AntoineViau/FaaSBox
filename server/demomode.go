package main

import (
	"net/http"
	"os"

	"github.com/pocketbase/pocketbase/core"
)

// The three variables that turn an instance into a showcase.
//
// Only the first commands anything. The other two fill the two fields of the
// sign-in form and nothing else: they create no account, and the one they name
// has to exist already and be a superuser.
const (
	demoModeEnv         = "FAASBOX_DEMOMODE"
	demoModeEmailEnv    = "FAASBOX_DEMOMODE_EMAIL"
	demoModePasswordEnv = "FAASBOX_DEMOMODE_PASSWORD"
)

// demoSettings is what those three variables said, read once at startup.
type demoSettings struct {
	Enabled  bool
	Email    string
	Password string
}

// demoSettingsFromEnv reads the three. The error comes from the flag alone —
// the credentials are taken as they stand, absent included.
func demoSettingsFromEnv() (demoSettings, error) {
	enabled, err := envBool(demoModeEnv, false)
	if err != nil {
		return demoSettings{}, err
	}
	return demoSettings{
		Enabled:  enabled,
		Email:    os.Getenv(demoModeEmailEnv),
		Password: os.Getenv(demoModePasswordEnv),
	}, nil
}

// The exact method+path pairs a showcase still has to accept, and the reason
// each one is here. Exact, not prefixed: spelling the first as a prefix would
// let "POST /api/collections/{x}/records" through with it.
var writeExceptions = map[string]struct{}{
	"POST /api/collections/_superusers/auth-with-password": {}, // sign in
	"POST /api/collections/_superusers/auth-refresh":       {}, // validate the session
	"POST /api/realtime": {}, // place the subscriptions
}

// The pairs that look safe and are not. A method-based rule assumes a GET
// reads, and this is where that assumption breaks: GET /oauth/authorize records
// a pending grant before it redirects (oauthauthorize.go), so the method says
// "read" while the handler writes.
//
// Closing it costs the showcase nothing. The flow it opens cannot finish
// anyway — POST /oauth/token and the POST carrying the consent decision are
// both refused — so all it would leave behind is a table of pending grants that
// nothing collects any more: the hourly prune is one of the five startups a
// showcase skips, and a caller can loop the request.
//
// **Every GET route that writes has to be named here.** That is the price of
// deciding by method, and it is the one thing that does not take care of itself
// when a route is added.
var writingReads = map[string]struct{}{
	"GET /oauth/authorize": {},
}

// refuseWrites turns the instance into a read-only showcase.
//
// The rule is the HTTP **method**, not the route: everything that is not safe
// is refused unless writeExceptions names it. That is what makes it tenable —
// a route added tomorrow is refused by default, without anyone having to think
// about it. POST /invoke/{name} has no exception and falls with the rest, and
// so do /mcp and the three OAuth routes that carry a POST.
//
// writingReads is the counterweight, read first: a safe method is only safe
// until a handler proves otherwise.
//
// 403 and not 405: the method is understood, it is the policy that refuses.
func refuseWrites(e *core.RequestEvent) error {
	route := e.Request.Method + " " + e.Request.URL.Path

	if _, ok := writingReads[route]; ok {
		return refuseDemoWrite(e)
	}

	switch e.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return e.Next()
	}
	if _, ok := writeExceptions[route]; ok {
		return e.Next()
	}
	return refuseDemoWrite(e)
}

// refuseDemoWrite is the one refusal, so the two callers above cannot drift into
// two different bodies.
func refuseDemoWrite(e *core.RequestEvent) error {
	return e.JSON(http.StatusForbidden, map[string]string{
		"error": "this instance is a read-only demo",
	})
}

// instanceHandler publishes what mode this instance runs in, without
// authentication, in both modes.
//
// The two credential fields are only rendered in demo mode, and their absence
// outside it is not decorative: the two variables may well be set on an
// instance where the flag is not, and the route would then publish them
// without anyone having meant it.
//
// It is not folded into /health, which answers "am I alive" to an orchestrator
// and whose contract is documented for one.
func instanceHandler(demo demoSettings) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if !demo.Enabled {
			return e.JSON(http.StatusOK, map[string]any{"demoMode": false})
		}
		return e.JSON(http.StatusOK, map[string]any{
			"demoMode": true,
			"email":    demo.Email,
			"password": demo.Password,
		})
	}
}
