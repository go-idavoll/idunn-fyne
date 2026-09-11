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

package fyneui

import (
	"fmt"

	"github.com/go-idavoll/idunn/core/hook"
)

// step is one position in the sequence a transaction is actually observed to
// move through, with the wording shown for it.
type step struct {
	label    string
	fraction float64
}

// steps maps a phase to its observed position.
//
// The order here is NOT the order the constants are declared in hook.Phase. It
// is the order idunn emits them, read off the emit call sites in
// core/updater/apply.go, and the two differ in ways that matter:
//
//   - PhaseVerify is emitted AFTER PhaseApply (apply.go:215 follows apply.go:206),
//     because it verifies what was installed. Using the declaration order, where
//     verify sits third, would drive the bar backwards near the end of every
//     update that has VerifyAfterApply switched on.
//   - PhaseStage is never emitted on success. It is the phase a staging FAILURE
//     is reported under, so it takes the position staging occupies in time:
//     between the download and the quiesce.
//   - PhaseQuiesce follows PhaseDownload rather than preceding it.
//
// A phase idunn adds later that is missing from this map renders as an unknown
// step and holds the bar, which is the safe way to be out of date.
var steps = map[hook.Phase]step{
	hook.PhaseCheck:    {"Checking", 0.05},
	hook.PhaseDownload: {"Downloading and staging", 0.20},
	hook.PhaseStage:    {"Staging", 0.35},
	hook.PhaseQuiesce:  {"Waiting for the application", 0.45},
	hook.PhaseMigrate:  {"Migrating data", 0.60},
	hook.PhaseApply:    {"Installing", 0.75},
	hook.PhaseVerify:   {"Verifying the installed files", 0.88},
	hook.PhaseCommit:   {"Finishing", 0.96},
	hook.PhaseGC:       {"Cleaning up", 1.00},
	hook.PhaseRollback: {"Undoing the update", hold},
}

// hold is the fraction that means "leave the bar where it is".
const hold = -1.0

// Fraction is the progress-bar value for a snapshot, in [0,1], or -1 meaning
// hold the current value.
//
// hook.Event.Progress is documented as "in [0,1], or -1 if indeterminate", and
// today both emitters in idunn hardcode -1 (core/updater/apply.go:431,
// core/launch/launch.go:225). So the value is derived from the phase: it is a
// step counter, not a byte count, and must never be presented as a percentage of
// a download. The Progress >= 0 branch below is written and tested so that this
// package needs no change on the day core starts reporting real progress.
func Fraction(s Snapshot) float64 {
	if s.Progress >= 0 && s.Progress <= 1 {
		return s.Progress
	}
	st, ok := steps[s.Phase]
	if !ok {
		return hold
	}
	return st.fraction
}

// StepLabel is the human wording for a phase, e.g. "Installing". An unknown
// phase renders as its own name rather than as empty space, so a phase added
// upstream is visible rather than silently blank.
func StepLabel(p hook.Phase) string {
	if p == "" {
		return "Idle"
	}
	if st, ok := steps[p]; ok {
		return st.label
	}
	return string(p)
}

// LogLine renders one event as a single line for the event log.
func LogLine(e hook.Event) string {
	if e.Err != nil {
		return fmt.Sprintf("%-9s %s: %v", e.Phase, e.Message, e.Err)
	}
	return fmt.Sprintf("%-9s %s", e.Phase, e.Message)
}
