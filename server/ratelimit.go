package main

import (
	"github.com/pocketbase/pocketbase/core"
)

// ensureRateLimits turns the built-in rate limiter on, once.
//
// PocketBase ships the rules already filled in and the switch off. What is
// missing is therefore the switch, not a policy of our own — and it has to be
// flipped by code rather than in the admin UI, because settings live in the
// database and a fresh one would come up unprotected.
//
// The rules are left exactly as PocketBase provides them: *:auth at 2 requests
// per 3 seconds is the throttle on password guessing this exists for, and
// writing our own would freeze a policy against upstream's.
//
// The early return is not a nicety. Without it the settings row is rewritten at
// every boot, which is the same fault as saving an unchanged collection.
func ensureRateLimits(app core.App) error {
	settings := app.Settings()
	if settings.RateLimits.Enabled {
		return nil
	}
	settings.RateLimits.Enabled = true
	return app.Save(settings)
}
