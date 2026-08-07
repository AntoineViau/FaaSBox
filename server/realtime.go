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

// depsStateMessage is what a subscriber receives.
//
// FunctionId is always set, and it is what the client matches on: it survives a
// rename, which the name does not. A save that renames publishes its state under
// the new name before the HTTP response carrying that name reaches the client, so
// a client matching on the name drops those messages — harmless while an install
// follows and republishes, permanent when the same save also empties
// package.json and the only message is the terminal one.
//
// FunctionName rides along for whoever reads the stream, never for identification.
type depsStateMessage struct {
	FunctionId   string `json:"functionId"`
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
func broadcastDepsState(app core.App, functionId, functionName, status, errMsg string) {
	payload, err := json.Marshal(depsStateMessage{
		FunctionId:   functionId,
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
