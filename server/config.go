package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
)

// envInt reads a strictly positive integer from the environment.
//
// An absent variable yields the default silently — that is the nominal case.
// A value that is unparsable, negative or zero yields the default too, but with
// a message: a misconfigured bound must not take the server down, and it must
// not pass unnoticed either.
//
// Every tunable bound of the server goes through this function. Each one is
// still declared in the file that owns its domain; only the reading is shared,
// so a change of policy — logging level, acceptance rule — happens once.
func envInt(name string, def int) int {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		log.Printf("faasbox: invalid %s=%q, using default %d", name, s, def)
		return def
	}
	return n
}

// envBool reads a boolean from the environment.
//
// The policy is the opposite of envInt's, deliberately: a bound that falls back
// to its default costs a misconfigured limit, while a flag that falls back gets
// the mode of the whole instance wrong. FAASBOX_DEMOMODE=treu would leave every
// write route reachable on something published as a showcase. So the caller is
// handed the error and stops.
//
// An absent variable is the nominal case and yields the default, silently.
func envBool(name string, def bool) (bool, error) {
	s := os.Getenv(name)
	if s == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def, fmt.Errorf("invalid %s=%q: expected a boolean", name, s)
	}
	return v, nil
}
