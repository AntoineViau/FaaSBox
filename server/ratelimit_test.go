package main

import (
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// countSettingsWrites binds a counter on the settings row and returns a reader
// for it. Nothing else tells a caller whether app.Save(settings) ran: the
// in-memory settings read the same whether they were persisted or not.
func countSettingsWrites(app core.App) func() int {
	writes := 0
	count := func(e *core.ModelEvent) error {
		if _, ok := e.Model.(*core.Settings); ok {
			writes++
		}
		return e.Next()
	}
	app.OnModelAfterCreateSuccess().BindFunc(count)
	app.OnModelAfterUpdateSuccess().BindFunc(count)
	return func() int { return writes }
}

// TestEnsureRateLimits_ArmsTheLimiterAndLeavesTheRulesAlone covers what the
// startup call is for: a fresh database comes up with the limiter off, and the
// switch is the only thing we flip. The rules stay upstream's — *:auth at 2
// requests per 3 seconds is the throttle on superuser password guessing this
// exists for, and rewriting the list here would freeze a policy of our own
// against PocketBase's.
func TestEnsureRateLimits_ArmsTheLimiterAndLeavesTheRulesAlone(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if app.Settings().RateLimits.Enabled {
		t.Fatal("the fixture already has the limiter armed, so the call under test would prove nothing")
	}
	rules := append([]core.RateLimitRule(nil), app.Settings().RateLimits.Rules...)

	if err := ensureRateLimits(app); err != nil {
		t.Fatalf("ensureRateLimits: %v", err)
	}

	// Read back from the database rather than the object just mutated: what
	// matters is that the next boot finds the limiter on.
	if err := app.ReloadSettings(); err != nil {
		t.Fatalf("failed to reload the settings: %v", err)
	}
	if !app.Settings().RateLimits.Enabled {
		t.Error("the rate limiter is still off after ensureRateLimits")
	}
	if got := app.Settings().RateLimits.Rules; !reflect.DeepEqual(got, rules) {
		t.Errorf("the rules changed:\n got %+v\nwant %+v", got, rules)
	}
}

// TestEnsureRateLimits_DoesNotRewriteSettingsWhenAlreadyArmed pins the early
// return. Without it every boot rewrites the settings row for nothing, which is
// the same fault as saving an unchanged collection.
func TestEnsureRateLimits_DoesNotRewriteSettingsWhenAlreadyArmed(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	writes := countSettingsWrites(app)

	if err := ensureRateLimits(app); err != nil {
		t.Fatalf("first ensureRateLimits: %v", err)
	}
	if got := writes(); got != 1 {
		t.Fatalf("arming the limiter wrote the settings %d time(s), want 1 — the counter is not seeing the write", got)
	}

	if err := ensureRateLimits(app); err != nil {
		t.Fatalf("second ensureRateLimits: %v", err)
	}
	if got := writes(); got != 1 {
		t.Errorf("the settings were written %d time(s) in total, want 1 — the second call saved an unchanged row", got)
	}
}
