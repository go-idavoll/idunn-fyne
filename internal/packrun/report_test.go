// Copyright 2026 The idunn Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packrun_test

import (
	"strings"
	"testing"

	"github.com/go-idavoll/idunn-fyne/internal/packrun"
)

// TestParseReportReadsARealPublish uses the exact shape cmd/packer's report()
// writes, padding included.
func TestParseReportReadsARealPublish(t *testing.T) {
	got := packrun.ParseReport(sampleReport)

	if !got.Published {
		t.Fatal("Published = false for output that begins with a publish line")
	}
	if got.Name != "demo" || got.Version != "1.2.0" || got.Channel != "stable" {
		t.Errorf("got %s %s on %s, want demo 1.2.0 on stable", got.Name, got.Version, got.Channel)
	}
	if got.AddedTargets != 4 {
		t.Errorf("AddedTargets = %d, want 4", got.AddedTargets)
	}
	if len(got.Roles) != 5 || got.Roles["timestamp"] != 2 {
		t.Errorf("Roles = %v, want the five signed roles at version 2", got.Roles)
	}
	if got.Delegations["v1"] != 3 || got.Delegations["stable"] != 1 {
		t.Errorf("Delegations = %v", got.Delegations)
	}
	if len(got.Unparsed) != 0 {
		t.Errorf("lines this parser did not understand: %v", got.Unparsed)
	}
	if !strings.Contains(got.Summary(), "demo 1.2.0") {
		t.Errorf("Summary() = %q", got.Summary())
	}
}

// TestParseReportNeverFails is the whole design of this parser. The packer's
// stdout is human-readable text, not an API: it may change without notice, and
// when it does the assistant must degrade to showing the raw output rather than
// breaking or -- far worse -- claiming a publish did not happen when it did.
func TestParseReportNeverFails(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"whitespace", "\n\n   \n"},
		{"prose", "something went terribly wrong\nand then some more\n"},
		{"truncated publish line", "published demo\n"},
		{"role with a non-number", "  role targets      -> version many\n"},
		{"delegation with a non-number", "  delegation v1 holds lots targets\n"},
		{"targets with a non-number", "  many new targets\n"},
		{"binary-ish", "\x00\x01 published \xff\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := packrun.ParseReport(tc.in) // must not panic
			if got.Roles == nil || got.Delegations == nil {
				t.Error("the maps are nil; a caller ranging over them would be fine but "+
					"a caller writing to them would not", got)
			}
		})
	}
}

// TestParseReportKeepsWhatItCannotRead: an unrecognised line is shown to the
// operator verbatim, not swallowed.
func TestParseReportKeepsWhatItCannotRead(t *testing.T) {
	const out = `published demo 1.2.0 on channel stable
  role targets      -> version 2
  something entirely new appeared here
  1 new targets
`
	got := packrun.ParseReport(out)
	if len(got.Unparsed) != 1 || !strings.Contains(got.Unparsed[0], "entirely new") {
		t.Errorf("Unparsed = %v, want the one line this parser did not know", got.Unparsed)
	}
	// The lines it did understand must still be there.
	if !got.Published || got.AddedTargets != 1 || got.Roles["targets"] != 2 {
		t.Errorf("an unknown line disturbed the rest: %+v", got)
	}
}

func TestSummaryWithoutAPublishLine(t *testing.T) {
	got := packrun.ParseReport("idunn packer: packer: key: TUF_TARGETS_KEY is not set\n")
	if got.Published {
		t.Error("Published = true for output with no publish line")
	}
	if !strings.Contains(got.Summary(), "no publish summary") {
		t.Errorf("Summary() = %q, want it to admit there was none", got.Summary())
	}
}

func TestSummaryPluralisesOneTarget(t *testing.T) {
	got := packrun.ParseReport("published demo 1.0.0 on channel stable\n  1 new targets\n")
	if !strings.Contains(got.Summary(), "1 new target;") {
		t.Errorf("Summary() = %q, want a singular target", got.Summary())
	}
}
