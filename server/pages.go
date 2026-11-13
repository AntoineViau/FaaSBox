package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// The one road into a function that asks for no key: /f/{idOrName}, opened per
// function by a trigger of kind "route", and served on a host of its own.
//
// **Why a second host.** The editor keeps the superuser token in localStorage,
// which any script served from the same origin can read. A page written by a
// function and served beside the editor would only need one reflected XSS to
// hand over the whole instance. So the pages live on a host declared by
// FAASBOX_PAGES_URL, and a gate on the root router keeps the two apart in both
// directions: nothing but /f/ answers on the pages host, and /f/ answers
// nowhere else. Without the variable the feature does not exist — the same model
// as FAASBOX_PUBLIC_URL for OAuth, except that /f/ still answers 404 rather
// than the editor.
//
// The gate is what holds, more than the comparison with FAASBOX_PUBLIC_URL made
// at startup: a browser derives an origin from the host it asked for, and the
// gate decides on that same host. No origin can therefore be served both the
// editor and a page, whatever the operator configured.
//
// What a function answers is decoded by responseenvelope.go; running it is the
// shared verb of invokeops.go, with the two decisions that are this route's own:
// how a segment resolves (routedFunction) and what stdout must be.

// pagesURLEnv names the origin the public pages are served on.
const pagesURLEnv = "FAASBOX_PAGES_URL"

// pagesConfig is where the public pages are served, derived once at startup.
type pagesConfig struct {
	// Origin is scheme://host, with the default port of the scheme removed. It
	// is what the editor is told, to build the public URL of a function.
	Origin string
	// Host is the Host header a request to Origin carries.
	Host string
}

// noPages is the configuration of an instance that serves no page.
var noPages = pagesConfig{}

// pagesConfigFromEnv reads FAASBOX_PAGES_URL. The error is the reason the pages
// will not be served, worded for whoever reads the startup output.
//
// A showcase serves none, whatever the variable says. GET /f/... runs code and
// writes a log entry, and refuseWrites decides by method: its list of writing
// GETs names exact pairs, and a family of paths cannot be closed that way. Not
// mounting the route is the closure that holds.
func pagesConfigFromEnv(demoMode bool) (pagesConfig, error) {
	if demoMode {
		return noPages, errors.New("this instance is a read-only demo")
	}
	return parsePagesURL(os.Getenv(pagesURLEnv), os.Getenv(publicURLEnv))
}

// parsePagesURL turns the configured origin into a pages configuration, or
// says why it cannot. publicRaw is FAASBOX_PUBLIC_URL, read for one check only:
// the pages must not share the editor's host.
//
// The bare-origin rule is the shared one (parseBareOrigin). OAuth's rule on
// http:// is not applied: the pages are not OAuth, and pages.localhost — the
// natural host in development — is not a loopback name to isLoopbackHost.
func parsePagesURL(raw, publicRaw string) (pagesConfig, error) {
	u, err := parseBareOrigin(pagesURLEnv, raw)
	if err != nil {
		return noPages, err
	}
	host := withoutDefaultPort(u)

	// Compared on the same normalisation, so https://x and https://x:443 are the
	// one host they are. A FAASBOX_PUBLIC_URL that does not parse compares with
	// nothing: OAuth says why at its own startup line.
	if public, err := parseBareOrigin(publicURLEnv, publicRaw); err == nil &&
		strings.EqualFold(host, withoutDefaultPort(public)) {
		return noPages, fmt.Errorf(
			"%s=%q has the host of %s; the pages need a host of their own, or a page could read the editor's session",
			pagesURLEnv, strings.TrimSpace(raw), publicURLEnv)
	}

	return pagesConfig{Origin: u.Scheme + "://" + host, Host: host}, nil
}

// withoutDefaultPort returns the host of an origin, its port dropped when it is
// the default of the scheme. A browser never sends https://x:443 as Host:
// x:443, so a host kept in that form would match no request at all.
func withoutDefaultPort(u *url.URL) string {
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return strings.TrimSuffix(u.Host, ":"+port)
	}
	return u.Host
}

// reportPagesDisabled says at startup why no page is served. Standard logger,
// for the reason reportOAuthDisabled gives.
func reportPagesDisabled(reason error) {
	log.Printf("faasbox: public pages are disabled — %v. /f/ answers 404.", reason)
}

// reportPagesEnabled says where they are.
func reportPagesEnabled(cfg pagesConfig) {
	log.Printf("faasbox: public pages are served on %s/f/{idOrName}", cfg.Origin)
}

// pageRoute is the pattern /f/ is registered on, and what the gate recognises.
const pageRoute = "/f/{name}"

// mountPages poses the host gate and, when the pages are configured, registers
// /f/{idOrName}.
//
// The gate is posed **either way**. Without pages it is what answers 404 under
// /f/: left alone, a GET there would reach the SPA catch-all and show the
// editor to whoever followed a link to a page that is no longer served.
//
// It goes on the root router, like refuseWrites, so it covers PocketBase's own
// routes — /api/, the admin at /_/ — and the SPA catch-all as well as ours.
//
// The methods are registered one by one: Any("/f/{name}") panics at startup
// against the GET /{path...} catch-all, which ServeMux finds both more and less
// specific. GET covers HEAD. OPTIONS never reaches the function: PocketBase's
// CORS middleware answers a preflight before any route.
func mountPages(e *core.ServeEvent, cfg pagesConfig, functionsDir string) {
	e.Router.BindFunc(pagesHostGate(cfg.Host))
	if cfg == noPages {
		return
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		e.Router.Route(method, pageRoute, func(re *core.RequestEvent) error {
			return pageHandler(re, functionsDir)
		})
	}
}

// pagesHostGate keeps the pages and everything else on two hosts. One rule, two
// directions: on the pages host only /f/ answers — not the editor, not
// auth-with-password, whose session would land in the localStorage of the
// exposed origin — and anywhere else /f/ is closed, or a link to the main host
// would be all an attacker needs. An empty pagesHost — no pages — leaves the
// second direction alone: /f/ answers 404 everywhere.
//
// **The decision rests on the route ServeMux matched** (Request.Pattern), not
// on a reading of the path. The two disagree on encoded paths: ServeMux splits
// the escaped path on its literal slashes and unescapes each segment after, so
// /f%2Fx is one segment that only the SPA catch-all matches, while /%66/x is
// /f/{name} itself. Reading URL.Path would let the first serve the editor on the
// pages host and the second run a function on the main one. The decoded prefix
// is still refused off the pages host, which is what keeps /f/... from falling
// through to the SPA when no /f/ route is registered at all.
//
// The Host header is compared as received. X-Forwarded-Host is not read, for
// the reason the OAuth issuer ignores it: what this server is called must not be
// whatever a proxy feels like saying.
func pagesHostGate(pagesHost string) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		onPages := pagesHost != "" && strings.EqualFold(e.Request.Host, pagesHost)
		page := strings.HasSuffix(e.Request.Pattern, " "+pageRoute)

		allowed := page
		if !onPages {
			allowed = !page && !strings.HasPrefix(e.Request.URL.Path, "/f/")
		}
		if !allowed {
			return e.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return e.Next()
	}
}

// routedInvocation is the path /f/ takes: only a function a route opened is
// reachable, and stdout must be a response envelope.
var routedInvocation = invocationPath{
	resolve: routedFunction,
	decode: func(stdout string, truncated bool) (any, error) {
		return decodeResponseEnvelope(stdout, truncated)
	},
}

// routedFunction resolves a segment to a function that at least one active
// route trigger opens. A function without one is answered exactly as an absent
// function is: whether it exists is not the visitor's business.
//
// The three columns queried are left in the clear (cf. ensureTriggersCollection),
// so the filter runs in SQL. Several routes on one function are tolerated: one
// active is enough.
func routedFunction(app core.App, idOrName string) (*core.Record, error) {
	fn, err := resolveFunction(app, idOrName)
	if err != nil {
		return nil, err
	}
	_, err = app.FindFirstRecordByFilter(faasboxTriggersCollection,
		"function = {:id} && kind = 'route' && active = true",
		dbx.Params{"id": fn.Id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &errNotFound{Name: idOrName}
	}
	if err != nil {
		return nil, err
	}
	return fn, nil
}

// pageHandler answers /f/{idOrName}: transport only, like invokeHandler. It
// reads the body through the same bounds, builds the same envelope with
// "trigger": "route", and writes the response the function described.
func pageHandler(e *core.RequestEvent, functionsDir string) error {
	body, refused := readInvocationBody(e.Request)
	if refused != nil {
		return e.JSON(refused.status, map[string]string{"error": refused.message})
	}

	outcome, err := invokeFunction(e.Request.Context(), e.App, functionsDir, routedInvocation,
		e.Request.PathValue("name"), newHTTPInput(e.Request, body, triggerRoute))
	if err != nil {
		return answerPageFailure(e, outcome, err)
	}

	page, ok := outcome.Result.(*pageResponse)
	if !ok {
		// decodeResponseEnvelope is the only decoder of this path; reaching here
		// means the wiring above changed, and the visitor is owed a blank 500.
		e.App.Logger().Error("faasbox route: decoded result is not a page", "function", outcome.Function)
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": "function failed"})
	}
	return writePageResponse(e.Response, page)
}

// answerPageFailure turns a refused or failed /f/ request into its response.
//
// Every body is fixed or comes from one of our error types — never stdout,
// never stderr, never the raw execution error: the caller is anyone at all.
// The detail is in faasbox_logs, and in the server log.
func answerPageFailure(e *core.RequestEvent, outcome invokeOutcome, err error) error {
	var notFound *errNotFound
	switch {
	// A malformed segment designates nothing either, and a 400 would tell a
	// visitor more than the absence does.
	case errors.Is(err, errInvalidFunctionName), errors.As(err, &notFound):
		return e.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, errTooBusy):
		return e.JSON(http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	case errors.Is(err, errResolveFailed):
		return e.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	case errors.Is(err, errExecTimedOut):
		return e.JSON(http.StatusGatewayTimeout, map[string]string{"error": "function timed out"})
	}

	var refused *errResponseRefused
	if errors.As(err, &refused) {
		// Where the author of the function looks for why the page broke: the
		// reason, the function, and the field or header at fault when there is
		// one — that name came out of stdout, so it stays here.
		e.App.Logger().Warn("faasbox route: response envelope refused",
			"function", outcome.Function, "reason", refused.reason, "subject", refused.subject)
		return e.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	var unusable *errOutputUnusable
	if errors.As(err, &unusable) {
		return e.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}

	return e.JSON(http.StatusInternalServerError, map[string]string{"error": "function failed"})
}
