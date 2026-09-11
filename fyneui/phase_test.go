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

package fyneui_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn/core/hook"
)

// observedOrder is the sequence idunn actually emits for a successful update,
// read off the emit call sites in core/updater/apply.go and updater.go:
//
//	updater.go:324  check     "checking for updates"
//	updater.go:365  check     "update available: 1.1.0"
//	apply.go:137    check     "running pre-flight checks"
//	apply.go:172    download  "staging 1.1.0"
//	apply.go:319    quiesce   "waiting for the application to stop writing"
//	apply.go:197    migrate   "migrating state"
//	apply.go:206    apply     "installing 1.1.0"
//	apply.go:215    verify    "verifying the installed files"
//	apply.go:233    commit    "installed 1.1.0"
//
// Note what this is NOT: the order hook.Phase declares its constants in. There,
// verify sits third, before quiesce and stage. Driving a progress bar from the
// declaration order would send it backwards at apply.go:215 on every update with
// VerifyAfterApply switched on.
var observedOrder = []hook.Phase{
	hook.PhaseCheck,
	hook.PhaseDownload,
	hook.PhaseQuiesce,
	hook.PhaseMigrate,
	hook.PhaseApply,
	hook.PhaseVerify,
	hook.PhaseCommit,
	hook.PhaseGC,
}

// TestFractionRisesOverTheOrderIdunnActuallyEmits is the regression test for
// that bug. A bar that goes backwards reads as "it is doing the update again".
func TestFractionRisesOverTheOrderIdunnActuallyEmits(t *testing.T) {
	prev := -1.0
	for _, p := range observedOrder {
		got := fyneui.Fraction(fyneui.Snapshot{Phase: p, Progress: -1})
		if got < 0 {
			t.Fatalf("Fraction(%q) = %v, want a real position in [0,1]", p, got)
		}
		if got <= prev {
			t.Errorf("Fraction(%q) = %v, which does not advance past the previous %v; "+
				"the progress bar would stall or go backwards", p, got, prev)
		}
		prev = got
	}
	if prev != 1 {
		t.Errorf("the last observed phase reaches %v, want 1", prev)
	}
}

// TestFractionPlacesStageBetweenDownloadAndQuiesce. PhaseStage is never emitted
// on success -- it is the phase a staging FAILURE is reported under -- so its
// position must be where staging happens in time, not where the constant sits.
func TestFractionPlacesStageBetweenDownloadAndQuiesce(t *testing.T) {
	at := func(p hook.Phase) float64 {
		return fyneui.Fraction(fyneui.Snapshot{Phase: p, Progress: -1})
	}
	if !(at(hook.PhaseDownload) < at(hook.PhaseStage) && at(hook.PhaseStage) < at(hook.PhaseQuiesce)) {
		t.Errorf("stage (%v) is not between download (%v) and quiesce (%v)",
			at(hook.PhaseStage), at(hook.PhaseDownload), at(hook.PhaseQuiesce))
	}
}

// TestFractionHoldsOnRollback. A rollback is not negative progress, it is the
// way back; moving the bar down would read as a second attempt.
func TestFractionHoldsOnRollback(t *testing.T) {
	got := fyneui.Fraction(fyneui.Snapshot{Phase: hook.PhaseRollback, Progress: -1})
	if got >= 0 {
		t.Errorf("Fraction(rollback) = %v, want a negative hold value", got)
	}
}

// TestFractionHoldsOnAnUnknownPhase: a phase added upstream must not reset the
// bar to zero. Holding is the safe way to be out of date.
func TestFractionHoldsOnAnUnknownPhase(t *testing.T) {
	if got := fyneui.Fraction(fyneui.Snapshot{Phase: "teleport", Progress: -1}); got >= 0 {
		t.Errorf("Fraction(unknown) = %v, want a negative hold value", got)
	}
}

// TestFractionHonoursRealProgress covers the branch production does not reach
// yet. Both emitters in idunn hardcode Progress: -1; on the day one of them
// reports a real fraction, this package must defer to it rather than override it
// with a phase guess.
func TestFractionHonoursRealProgress(t *testing.T) {
	for _, tc := range []struct {
		progress float64
		want     float64
	}{
		{0, 0},
		{0.42, 0.42},
		{1, 1},
	} {
		got := fyneui.Fraction(fyneui.Snapshot{Phase: hook.PhaseCheck, Progress: tc.progress})
		if got != tc.want {
			t.Errorf("Fraction(check, progress=%v) = %v, want %v: a real progress "+
				"value must win over the derived one", tc.progress, got, tc.want)
		}
	}
	// Out of range is not progress; fall back to the phase.
	got := fyneui.Fraction(fyneui.Snapshot{Phase: hook.PhaseCommit, Progress: 4})
	if got != fyneui.Fraction(fyneui.Snapshot{Phase: hook.PhaseCommit, Progress: -1}) {
		t.Errorf("an out-of-range progress of 4 was used as a fraction")
	}
}

func TestStepLabel(t *testing.T) {
	for _, tc := range []struct {
		phase hook.Phase
		want  string
	}{
		{"", "Idle"},
		{hook.PhaseApply, "Installing"},
		{hook.PhaseRollback, "Undoing the update"},
		{"teleport", "teleport"}, // an unknown phase shows itself rather than nothing.
	} {
		if got := fyneui.StepLabel(tc.phase); got != tc.want {
			t.Errorf("StepLabel(%q) = %q, want %q", tc.phase, got, tc.want)
		}
	}
}

func TestLogLine(t *testing.T) {
	plain := fyneui.LogLine(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.1.0"})
	if !strings.Contains(plain, "commit") || !strings.Contains(plain, "installed 1.1.0") {
		t.Errorf("LogLine = %q, want the phase and the message", plain)
	}
	withErr := fyneui.LogLine(hook.Event{
		Phase:   hook.PhaseRollback,
		Message: "rollback failed",
		Err:     errors.New("disk full"),
	})
	if !strings.Contains(withErr, "disk full") {
		t.Errorf("LogLine = %q, want the error text included", withErr)
	}
}
