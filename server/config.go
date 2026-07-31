package main

import (
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
