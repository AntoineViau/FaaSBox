package main

import (
	"log"
	"net/http"
	"os"
	"regexp"

	"github.com/pocketbase/pocketbase/core"
)

// This file owns what an instance publishes about itself: the name it carries,
// and the one public route that says what it is.
//
// The name and the demo flag are two independent facets — one is a label, the
// other decides whether every write route is reachable — so the mode and its
// refusal stay in demomode.go, and the route that publishes both lives here.

// The variable that names an instance. Optional: absent, the instance is
// unnamed, which is what every instance was before it existed.
const instanceNameEnv = "FAASBOX_NAME"

// The cap tells you where the value lands: an <h1> beside the wordmark, and an
// MCP server name a human types into a command.
const maxInstanceNameLen = 32

// The character set is wider than validName's: the dot and the underscore are
// accepted, and nothing is imposed at either end. A name designates no route,
// no record and no directory — it is read by people, not resolved by code — so
// the rule that makes a function name safe has no reason to bind here.
var validInstanceName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// instanceNameFromEnv reads the name once, on envInt's policy rather than
// envBool's.
//
// The frontier is the one config.go already draws: a flag that falls back gets
// the mode of the whole instance wrong, so envBool hands the error up and the
// server stops. A label that falls back costs a label. Refusing to start an
// instance over the spelling of its own name would be out of proportion, so a
// value outside the character set leaves the instance unnamed and says so.
//
// Nothing is normalised — no trimming, no lowercasing. FAASBOX_NAME=" toto "
// is refused by the regex and logged, exactly as a non-numeric value is refused
// by envInt. A name silently turned into something else is a name its author
// would not recognise in the header.
func instanceNameFromEnv() string {
	s := os.Getenv(instanceNameEnv)
	if s == "" {
		return ""
	}
	if !validInstanceName.MatchString(s) || len(s) > maxInstanceNameLen {
		// %q escapes what the regex refused, so a control character prints
		// rather than travelling raw into the log.
		log.Printf("faasbox: invalid %s=%q, this instance stays unnamed", instanceNameEnv, s)
		return ""
	}
	return s
}

// instanceHandler publishes what this instance is, without authentication, in
// both modes.
//
// The name is **always** rendered, empty string included. It does not follow
// the rule the two credential fields follow: those are only rendered in demo
// mode because the two variables may well be set on an instance where the flag
// is not, and the route would then publish them without anyone having meant it.
// A label is not a secret, and a field that is always there spares the client a
// missing-value case it would have to handle anyway.
//
// It is not folded into /health, which answers "am I alive" to an orchestrator
// and whose contract is documented for one.
//
// The answer is never stored. Turning demo mode on takes a restart, so a
// browser that visited the instance before the switch would otherwise be free
// to keep the answer it got then, and open an editor whose controls promise
// what the server now refuses. noStore says why the rule is not this route's
// alone.
func instanceHandler(demo demoSettings, instanceName string) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		noStore(e)

		if !demo.Enabled {
			return e.JSON(http.StatusOK, map[string]any{
				"demoMode": false,
				"name":     instanceName,
			})
		}
		return e.JSON(http.StatusOK, map[string]any{
			"demoMode": true,
			"name":     instanceName,
			"email":    demo.Email,
			"password": demo.Password,
		})
	}
}
