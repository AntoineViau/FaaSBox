package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// registerRealtimeClient adds a client to the broker with the given
// subscriptions, authenticated on authCollection or anonymous when it is empty,
// and returns it.
//
// An anonymous client gets a typed nil `*core.Record` under the auth key rather
// than no key at all, because that is what PocketBase actually leaves behind: it
// runs Set(RealtimeClientAuthKey, e.Auth) on every subscription, without
// condition, and e.Auth is a nil `*core.Record` for an unauthenticated request.
// Leaving the key absent would test a state the server never produces, and would
// let a filter that only asserts the type pass.
func registerRealtimeClient(t testing.TB, app core.App, authCollection string, subs ...string) *subscriptions.DefaultClient {
	t.Helper()
	client := subscriptions.NewDefaultClient()
	client.Subscribe(subs...)
	var auth *core.Record
	if authCollection != "" {
		record, err := app.FindFirstRecordByFilter(authCollection, "1=1")
		if err != nil {
			t.Fatalf("no record in auth collection %q of the test app: %v", authCollection, err)
		}
		auth = record
	}
	client.Set(apis.RealtimeClientAuthKey, auth)
	app.SubscriptionsBroker().Register(client)
	t.Cleanup(func() { app.SubscriptionsBroker().Unregister(client.Id()) })
	return client
}

// receivedState reads one message off a client's channel, or reports that none
// came. The broker sends without blocking on a buffered channel, so a message
// that was going to arrive is already there.
func receivedState(t testing.TB, client *subscriptions.DefaultClient) (depsStateMessage, bool) {
	t.Helper()
	select {
	case msg := <-client.Channel():
		if msg.Name != depsRealtimeTopic {
			t.Fatalf("message name = %q, want %q", msg.Name, depsRealtimeTopic)
		}
		var state depsStateMessage
		if err := json.Unmarshal(msg.Data, &state); err != nil {
			t.Fatalf("undecodable message payload %q: %v", msg.Data, err)
		}
		return state, true
	case <-time.After(200 * time.Millisecond):
		return depsStateMessage{}, false
	}
}

// TestBroadcastDepsState_ReachesASubscribedSuperuser is the other half of the
// filter below: closing the topic must not cut off its one legitimate consumer,
// the editor, which subscribes on a superuser session.
func TestBroadcastDepsState_ReachesASubscribedSuperuser(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	client := registerRealtimeClient(t, app, core.CollectionNameSuperusers, depsRealtimeTopic)

	broadcastDepsState(app, "rec123", "with-deps", depsStatusInstalling, "")

	state, ok := receivedState(t, client)
	if !ok {
		t.Fatal("a subscribed superuser client received nothing")
	}
	if state.FunctionName != "with-deps" || state.DepsStatus != depsStatusInstalling {
		t.Errorf("message = %+v, want the function name and the new status", state)
	}
}

// TestBroadcastDepsState_SkipsUnauthenticatedClients guards the only protection
// this topic has: it is not a collection, so PocketBase applies no access rule
// to it.
func TestBroadcastDepsState_SkipsUnauthenticatedClients(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	anonymous := registerRealtimeClient(t, app, "", depsRealtimeTopic)

	broadcastDepsState(app, "rec123", "with-deps", depsStatusError, "secret-looking install output")

	if state, ok := receivedState(t, anonymous); ok {
		t.Errorf("an unauthenticated client received %+v, want nothing", state)
	}
}

// TestBroadcastDepsState_SkipsNonSuperuserClients covers the case the nil check
// alone would miss. This instance authenticates superusers only today, so any
// other auth collection is hypothetical — which is exactly why the test uses one
// the PocketBase test app provides: the day such a collection appears here, a
// filter that only asked "is someone authenticated" would hand it the inventory.
func TestBroadcastDepsState_SkipsNonSuperuserClients(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	user := registerRealtimeClient(t, app, "users", depsRealtimeTopic)

	broadcastDepsState(app, "rec123", "with-deps", depsStatusError, "secret-looking install output")

	if state, ok := receivedState(t, user); ok {
		t.Errorf("a client authenticated outside %s received %+v, want nothing",
			core.CollectionNameSuperusers, state)
	}
}

func TestBroadcastDepsState_SkipsClientsWithoutTheSubscription(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	other := registerRealtimeClient(t, app, core.CollectionNameSuperusers, "faasbox_logs")

	broadcastDepsState(app, "rec123", "with-deps", depsStatusReady, "")

	if state, ok := receivedState(t, other); ok {
		t.Errorf("a client subscribed elsewhere received %+v, want nothing", state)
	}
}

// TestSetDepsState_BroadcastsEveryTransition ties the writer to the channel: a
// published state must reach a subscriber, and it must carry the record id —
// which is the only thing a client is allowed to match on, since it is the only
// thing that survives a rename.
func TestSetDepsState_BroadcastsEveryTransition(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "broadcast-deps",
		"console.log('hi')", `{"dependencies":{}}`)
	client := registerRealtimeClient(t, app, core.CollectionNameSuperusers, depsRealtimeTopic)

	setDepsState(app, record.Id, "broadcast-deps", depsStatusInstalling, "")
	state, ok := receivedState(t, client)
	if !ok {
		t.Fatal("no message after a write by id")
	}
	if state.DepsStatus != depsStatusInstalling {
		t.Errorf("depsStatus = %q, want %q", state.DepsStatus, depsStatusInstalling)
	}
	if state.FunctionId != record.Id {
		t.Errorf("functionId = %q, want the record id %q", state.FunctionId, record.Id)
	}
	if state.FunctionName != "broadcast-deps" {
		t.Errorf("functionName = %q, want it carried alongside the id", state.FunctionName)
	}

	// The terminal state goes out the same way, from whichever path produced it:
	// the invocation path holds the record too, so no message ever leaves without
	// an id for the client to match on.
	setDepsState(app, record.Id, "broadcast-deps", depsStatusError, "install output")
	state, ok = receivedState(t, client)
	if !ok {
		t.Fatal("no message after the terminal write")
	}
	if state.DepsStatus != depsStatusError || state.DepsError != "install output" {
		t.Errorf("message = %+v, want the error status and its message", state)
	}
	if state.FunctionId != record.Id {
		t.Errorf("functionId = %q, want the record id %q", state.FunctionId, record.Id)
	}
}
