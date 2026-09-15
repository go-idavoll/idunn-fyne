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

// step is one span of the sequence a transaction is actually observed to move
// through, with the wording shown for it.
//
// A phase owns a span rather than a point because idunn reports byte-level
// progress inside one of them: from and to are where the bar sits when the phase
// starts and when it has finished, and a real progress value is placed between
// the two. A phase that reports no progress simply sits at from for its whole
// duration, which is what every phase did before staging grew a byte count.
type step struct {
	label string
	from  float64
	to    float64
}

// steps maps a phase to the span of the bar it owns.
//
// The order here is NOT the order the constants are declared in hook.Phase. It
// is the order idunn emits them, read off the emit call sites in
// core/updater/apply.go, and the two differ in ways that matter:
//
//   - PhaseVerify is emitted AFTER PhaseApply, because it verifies what was
//     installed. Using the declaration order, where verify sits third, would
//     drive the bar backwards near the end of every update that has
//     VerifyAfterApply switched on.
//   - PhaseStage is never emitted on success. It is the phase a staging FAILURE
//     is reported under, so it takes the position staging occupies in time:
//     between the download and the quiesce.
//   - PhaseQuiesce follows PhaseDownload rather than preceding it.
//
// PhaseDownload owns the widest span because it is the only phase that reports a
// real fraction of itself: staging emits a byte count per file, and that count is
// of the staging job alone, not of the transaction. Handing it straight to the
// bar would fill it while the swap, the verify and the commit were still to come
// — and the next phase would then move it backwards, which is exactly what the
// integration test catches.
//
// A phase idunn adds later that is missing from this map renders as an unknown
// step and holds the bar, which is the safe way to be out of date.
var steps = map[hook.Phase]step{
	hook.PhaseCheck:    {"Checking", 0.05, 0.20},
	hook.PhaseDownload: {"Downloading and staging", 0.20, 0.45},
	hook.PhaseStage:    {"Staging", 0.35, 0.45},
	hook.PhaseQuiesce:  {"Waiting for the application", 0.45, 0.60},
	hook.PhaseMigrate:  {"Migrating data", 0.60, 0.75},
	hook.PhaseApply:    {"Installing", 0.75, 0.88},
	hook.PhaseVerify:   {"Verifying the installed files", 0.88, 0.96},
	hook.PhaseCommit:   {"Finishing", 0.96, 1.00},
	hook.PhaseGC:       {"Cleaning up", 1.00, 1.00},
	hook.PhaseRollback: {"Undoing the update", hold, hold},
}

// hold is the fraction that means "leave the bar where it is".
const hold = -1.0

// Fraction is the progress-bar value for a snapshot, in [0,1], or -1 meaning
// hold the current value.
//
// hook.Event.Progress is documented as "in [0,1], or -1 if indeterminate".
// Staging reports a real one — the bytes written against the total taken from the
// signed lengths — and every other phase still reports -1. Both are honoured the
// same way: the phase decides which span of the bar is being crossed, and the
// progress value, when there is one, says how far across that span the transaction
// has got. A phase with no progress sits at the start of its own span.
//
// That indirection is the whole point. A staging fraction is a fraction of
// staging, not of the update, so it is never handed to the bar as one: a release
// whose bytes are all in place is nowhere near installed, and a bar that said
// otherwise would have to move backwards to tell the truth afterwards.
//
// The bar is therefore still a step count over most of a transaction, and must
// never be presented as a percentage of a download.
func Fraction(s Snapshot) float64 {
	st, ok := steps[s.Phase]
	if !ok || st.from < 0 {
		return hold
	}
	if s.Progress >= 0 && s.Progress <= 1 {
		return st.from + s.Progress*(st.to-st.from)
	}
	return st.from
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
