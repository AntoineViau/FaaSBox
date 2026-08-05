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
// subscriptions, authenticated or not, and returns it.
func registerRealtimeClient(t testing.TB, app core.App, authenticated bool, subs ...string) *subscriptions.DefaultClient {
	t.Helper()
	client := subscriptions.NewDefaultClient()
	client.Subscribe(subs...)
	if authenticated {
		// Any auth record does: what the filter reads is the presence of one.
		superuser, err := app.FindFirstRecordByFilter(core.CollectionNameSuperusers, "1=1")
		if err != nil {
			t.Fatalf("no superuser in the test app: %v", err)
		}
		client.Set(apis.RealtimeClientAuthKey, superuser)
	}
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

func TestBroadcastDepsState_ReachesASubscribedClient(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	client := registerRealtimeClient(t, app, true, depsRealtimeTopic)

	broadcastDepsState(app, "rec123", "with-deps", depsStatusInstalling, "")

	state, ok := receivedState(t, client)
	if !ok {
		t.Fatal("a subscribed and authenticated client received nothing")
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

	anonymous := registerRealtimeClient(t, app, false, depsRealtimeTopic)

	broadcastDepsState(app, "rec123", "with-deps", depsStatusError, "secret-looking install output")

	if state, ok := receivedState(t, anonymous); ok {
		t.Errorf("an unauthenticated client received %+v, want nothing", state)
	}
}

func TestBroadcastDepsState_SkipsClientsWithoutTheSubscription(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	other := registerRealtimeClient(t, app, true, "faasbox_logs")

	broadcastDepsState(app, "rec123", "with-deps", depsStatusReady, "")

	if state, ok := receivedState(t, other); ok {
		t.Errorf("a client subscribed elsewhere received %+v, want nothing", state)
	}
}

// TestSetDepsState_BroadcastsEveryTransition ties the two writers to the
// channel: whichever one publishes the state, a subscriber must learn about it.
func TestSetDepsState_BroadcastsEveryTransition(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	functionsDir := t.TempDir()
	record := saveTestFunction(t, app, functionsDir, "broadcast-deps",
		"console.log('hi')", `{"dependencies":{}}`)
	client := registerRealtimeClient(t, app, true, depsRealtimeTopic)

	// By id, the way the save path writes it. The message must carry that id:
	// it is what lets a client keep following a function across a rename.
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

	// By name, the way the invocation path writes it. The id stays empty on
	// purpose — resolving it would cost a read per state write — so the client
	// falls back to matching on the name, which must therefore still be there.
	setDepsStateByName(app, "broadcast-deps", depsStatusError, "install output")
	state, ok = receivedState(t, client)
	if !ok {
		t.Fatal("no message after a write by name")
	}
	if state.DepsStatus != depsStatusError || state.DepsError != "install output" {
		t.Errorf("message = %+v, want the error status and its message", state)
	}
	if state.FunctionId != "" {
		t.Errorf("functionId = %q, want it empty on the invocation path", state.FunctionId)
	}
	if state.FunctionName != "broadcast-deps" {
		t.Errorf("functionName = %q, want the name to carry the message", state.FunctionName)
	}
}
