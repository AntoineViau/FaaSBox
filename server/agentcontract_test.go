package main

import (
	"strings"
	"testing"
)

// contractMarkers pins the six points of the writing contract, plus the two the
// opening paragraph carries, so a rewrite that drops one is caught rather than
// merely reformatted.
//
// Both readers are held to the same list — the MCP instructions and the guide
// at /llms.txt — because they teach the same contract. They differ by the sixth
// point only, and each form of it carries the last two markers.
var contractMarkers = []string{
	"bun:sqlite",                // the folder is a function's own, and holds a database
	"import.meta.dir",           // where a path into it is built from
	"stdin", "stdout", "stderr", // execution contract
	"envelope",      // what stdin actually carries
	"triggerName",   // and what tells two schedules apart
	"x-api-key",     // the headers a function never sees
	"hyphens",       // naming rule
	"1048576",       // size caps
	"background",    // installs do not finish with the write
	"day-of-week",   // cron expression
	"startup",       // the second kind of trigger
	"/f/{idOrName}", // the third, which publishes the function
	"1439",          // and the bound on its delay
	"Europe/Paris",  // the zone an expression is read in
	"empty object",  // plainEnv erases
	"do not patch",  // a write replaces
}

// TestWritingContractNamesNoTransport covers what makes the contract shareable:
// nothing in it speaks of a tool, a route or a sentence only one reader can act
// on. An agent reading the guide was told "an invocation you ask for" carries
// the MCP envelope, which its own POST /invoke does not produce.
func TestWritingContractNamesNoTransport(t *testing.T) {
	for _, phrase := range []string{"These tools", "update_function", "invoke_function", "PUT", "you ask for"} {
		if strings.Contains(writingContract, phrase) {
			t.Errorf("the shared contract says %q, which belongs to one transport", phrase)
		}
	}
}

// TestPagesParagraph covers the closing paragraph both readers end on.
func TestPagesParagraph(t *testing.T) {
	with := pagesParagraph(pagesConfig{Origin: "https://pages.example.com", Host: "pages.example.com"})
	if !strings.Contains(with, "https://pages.example.com/f/{idOrName}") {
		t.Errorf("the paragraph does not give the public address of a page:\n%s", with)
	}

	without := pagesParagraph(noPages)
	if !strings.Contains(without, "serves no public page") || strings.Contains(without, "/f/{idOrName}") {
		t.Errorf("the paragraph of an instance without pages does not say so:\n%s", without)
	}
}
