package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// The triggers a function carries, seen from the management contract: how a
// request body describes them, and how they are written back.
//
// Split out of manage.go on the same criterion as deps.go and depsstate.go: one
// file answers "what is a function over HTTP", this one "what are its triggers".
// They live in package main like everything else, so the cut changes no
// visibility.

// manageCron is one trigger in a request body. Active is a pointer so an omitted
// field can default to true: declaring a trigger is asking for it to fire, and
// the zero value of a bool would answer the opposite.
type manageCron struct {
	Name     string          `json:"name"`
	Schedule string          `json:"schedule"`
	Payload  json.RawMessage `json:"payload"`
	Active   *bool           `json:"active"`
	MaxQueue int             `json:"maxQueue"`
	// Kind is written as received, with no default of its own: an absent field
	// writes an empty column, which cronKind reads back as "cron". The
	// normalisation lives in the accessor and nowhere else — the PocketBase
	// admin can write an empty column outside of any contract.
	Kind                string `json:"kind"`
	StartupDelayMinutes int    `json:"startupDelayMinutes"`
}

// cronContract is one trigger in a response. Kind is normalised, because it is
// read through the accessor.
type cronContract struct {
	Name                string          `json:"name"`
	Schedule            string          `json:"schedule"`
	Payload             json.RawMessage `json:"payload"`
	Active              bool            `json:"active"`
	MaxQueue            int             `json:"maxQueue"`
	Kind                string          `json:"kind"`
	StartupDelayMinutes int             `json:"startupDelayMinutes"`
}

// refusedTrigger marks a write that a *trigger* refused, rather than the
// function record. Both carry a field called "name", so the field names alone
// cannot say which of the two a validation failure is about — and the caller has
// to know, especially with several triggers in one body.
type refusedTrigger struct {
	name  string
	index int // position in the submitted list, 0-based
	err   error
}

func (r *refusedTrigger) Error() string { return r.subject() + ": " + r.err.Error() }
func (r *refusedTrigger) Unwrap() error { return r.err }

// subject names the offending trigger for the caller. It falls back to the
// position when the name is what was refused — `Trigger ""` names nothing, and a
// body may well carry several.
func (r *refusedTrigger) subject() string {
	if r.name == "" {
		return fmt.Sprintf("Trigger #%d", r.index+1)
	}
	return fmt.Sprintf("Trigger %q", r.name)
}

// replaceCronJobs makes the triggers of a function match the list given, and
// nothing else. The replacement is whole — matching what the editor's Triggers
// tab does — so a list is read as the complete set, never as a patch.
//
// It runs in a transaction because the alternative is destructive: deleting the
// existing triggers first and having the cron hook refuse an invalid expression
// halfway through would leave the function with none. Rolling back also spares a
// second validation of the expressions here, which would be the same rule
// written twice.
//
// Its caller opens a transaction of its own, around the function record as well.
// PocketBase nests by reusing the running transaction, so this one costs a type
// switch — and it is kept rather than dropped, on the same principle as validName:
// each entry point defends itself, and a helper that is only atomic when someone
// else remembered to wrap it is a trap.
//
// The relation is set to the function id, never to its name (cf. the cron
// collection: a trigger wired on a name fired into the void the day it changed).
func replaceCronJobs(app core.App, fn *core.Record, crons []manageCron) error {
	return app.RunInTransaction(func(txApp core.App) error {
		existing, err := txApp.FindAllRecords(faasboxCronJobsCollection, dbx.HashExp{"function": fn.Id})
		if err != nil {
			return err
		}
		for _, record := range existing {
			if err := txApp.Delete(record); err != nil {
				return err
			}
		}

		col, err := txApp.FindCollectionByNameOrId(faasboxCronJobsCollection)
		if err != nil {
			return err
		}
		for i, c := range crons {
			record := core.NewRecord(col)
			record.Set("name", c.Name)
			record.Set("schedule", c.Schedule)
			record.Set("function", fn.Id)
			record.Set("active", c.Active == nil || *c.Active)
			record.Set("maxQueue", c.MaxQueue)
			record.Set("kind", c.Kind)
			record.Set("startupDelayMinutes", c.StartupDelayMinutes)
			if len(c.Payload) > 0 {
				record.Set("payload", c.Payload)
			}
			if err := txApp.Save(record); err != nil {
				return &refusedTrigger{name: c.Name, index: i, err: err}
			}
		}
		return nil
	})
}

// functionCronContracts reads the triggers of a function, ordered by name. The
// order is settled server-side, like the file listing, so a client renders what
// it receives instead of inventing a sort of its own.
func functionCronContracts(app core.App, functionId string) ([]cronContract, error) {
	records, err := app.FindAllRecords(faasboxCronJobsCollection, dbx.HashExp{"function": functionId})
	if err != nil {
		return nil, err
	}

	crons := make([]cronContract, 0, len(records))
	for _, record := range records {
		crons = append(crons, cronContract{
			Name:                cronName(app, record),
			Schedule:            cronSchedule(app, record),
			Payload:             cronPayload(app, record),
			Active:              record.GetBool("active"),
			MaxQueue:            int(record.GetFloat("maxQueue")),
			Kind:                cronKind(record),
			StartupDelayMinutes: int(record.GetFloat("startupDelayMinutes")),
		})
	}
	slices.SortFunc(crons, func(a, b cronContract) int {
		return strings.Compare(a.Name, b.Name)
	})
	return crons, nil
}

// cronPayload reads the stored payload back as raw JSON. An unset field reads as
// the empty object rather than as null, which is what runFunction feeds a
// trigger that carries none.
func cronPayload(app core.App, record *core.Record) json.RawMessage {
	payload := cronPayloadText(app, record)
	if payload == "" || payload == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(payload)
}
