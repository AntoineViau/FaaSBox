package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
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

// parseBareOrigin reads the value of an address variable as a bare http:// or
// https:// origin, or says why it cannot. name is the variable it came from: the
// error is printed at startup, and it is the only thing that explains a feature
// that did not go up.
//
// A path, a query or a fragment would end up in whatever is built on the
// origin, and credentials would be dropped silently by the scheme://host
// rebuild the callers make — a silent change to an address is worse than a
// refusal. What else a caller requires of its origin (OAuth wants https:// off
// loopback, the pages want a host of their own) is that caller's rule, not this
// one's.
func parseBareOrigin(name, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New(name + " is not set")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s=%q cannot be parsed: %w", name, raw, err)
	}

	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%s=%q is not an absolute http:// or https:// URL", name, raw)
	case u.Host == "":
		return nil, fmt.Errorf("%s=%q carries no host, so it is not an absolute URL", name, raw)
	case u.User != nil:
		return nil, fmt.Errorf("%s=%q carries credentials; it must be a bare origin", name, raw)
	case u.Path != "" && u.Path != "/":
		return nil, fmt.Errorf("%s=%q carries a path; it must be a bare origin", name, raw)
	case u.RawQuery != "" || u.ForceQuery:
		return nil, fmt.Errorf("%s=%q carries a query; it must be a bare origin", name, raw)
	case u.Fragment != "":
		return nil, fmt.Errorf("%s=%q carries a fragment; it must be a bare origin", name, raw)
	}
	return u, nil
}
