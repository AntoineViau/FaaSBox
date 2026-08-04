package main

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/routine"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// depsRealtimeTopic carries dependency installation state changes to the editor.
//
// It is a subscription name of our own, not a collection: PocketBase does not
// validate subscription names against collections, it only bounds their length.
// The consequence matters — no collection means no access rule, so the
// authentication filter in broadcastDepsState is the *only* thing standing
// between a state message and an anonymous client.
const depsRealtimeTopic = "faasbox_deps"

// depsStateMessage is what a subscriber receives. The function is identified by
// name and not by record id, because that is what both writers hold: the save
// path knows the record, but the invocation path never loads it and only ever
// holds a name. The name column carries a unique index, so it identifies the
// function just as well.
type depsStateMessage struct {
	FunctionName string `json:"functionName"`
	DepsStatus   string `json:"depsStatus"`
	DepsError    string `json:"depsError"`
}

// broadcastDepsState pushes a state change to the realtime clients that asked
// for it, and to no one else.
//
// It is called after the state has been written, never instead of writing it:
// a client that missed a message re-reads the record on reconnection, so the
// database stays the source of truth and the channel only saves it a round trip.
func broadcastDepsState(app core.App, functionName, status, errMsg string) {
	payload, err := json.Marshal(depsStateMessage{
		FunctionName: functionName,
		DepsStatus:   status,
		DepsError:    errMsg,
	})
	if err != nil {
		app.Logger().Error("faasbox: cannot encode dependency state message",
			"function", functionName, "error", err)
		return
	}

	msg := subscriptions.Message{Name: depsRealtimeTopic, Data: payload}
	for _, client := range app.SubscriptionsBroker().Clients() {
		if !client.HasSubscription(depsRealtimeTopic) {
			continue
		}
		// Comma-ok rather than a nil comparison: Get returns an interface, and a
		// typed nil inside one does not satisfy `== nil`. This is the form
		// PocketBase itself uses to read the same key.
		if _, ok := client.Get(apis.RealtimeClientAuthKey).(*core.Record); !ok {
			continue
		}
		// Detached, exactly as PocketBase sends its own record messages. A client
		// channel is unbuffered, so Send blocks until the SSE writer picks the
		// message up: sending inline would let one stalled browser hold up the
		// install goroutine, or an HTTP handler, for as long as it stays stalled.
		routine.FireAndForget(func() {
			client.Send(msg)
		})
	}
}
