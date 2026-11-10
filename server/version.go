package main

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// This file owns which build of FaaSBox is running, and the route that says so.
// Two readers: the MCP server, which advertises it as the version of its
// implementation, and the editor, which shows it to the owner once signed in.

// faasboxVersion is the build this binary belongs to. A released image carries
// its tag, injected at build time through -ldflags="-X main.faasboxVersion=..."
// from the FAASBOX_VERSION build argument the production Dockerfile takes.
// Anything built any other way keeps "dev", which is the honest answer for a
// binary that belongs to no release.
var faasboxVersion = "dev"

// versionHandler says which build of FaaSBox answers. Superuser only: the owner
// is the one who needs it, and a version handed to anyone who asks is a
// fingerprint for whoever scans for a vulnerable one. That is why it is not a
// field of the public instance route.
//
// The answer is not marked no-store: that rule is for responses that govern
// what the interface allows, and a version governs nothing.
func versionHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, map[string]string{"version": faasboxVersion})
}
