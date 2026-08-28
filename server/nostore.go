package main

import "github.com/pocketbase/pocketbase/core"

// noStore marks a response that must never be written down.
//
// The rule it carries: **a response that governs what the interface allows
// declares itself unstorable.** A stale copy of one of these does not make a
// page slightly wrong, it makes it lie about what it permits. Tokens and codes
// ride these bodies, and so does the mode an instance runs in — which decides
// which controls the editor offers at all, so a visitor holding an old answer
// discovers the refusal by clicking.
//
// `no-store` and not `no-cache`: the second lets a cache keep the response and
// revalidate it, the first forbids writing it down. There is nothing to save
// either way — these bodies are small and read once per exchange.
//
// `Pragma` is a request header in HTTP/1.1 and no compliant cache reads it on a
// response. It stays because RFC 6749 §5.1 requires it on the token response,
// and one helper for the four routes is worth more than sparing the other three
// a header that costs nothing.
func noStore(e *core.RequestEvent) {
	e.Response.Header().Set("Cache-Control", "no-store")
	e.Response.Header().Set("Pragma", "no-cache")
}
