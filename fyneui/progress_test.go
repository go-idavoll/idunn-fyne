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

// What a sidecar has to get right is not drawing — Fyne does that — but the
// arithmetic between an event stream and a window: which number is honest, which
// one is a guess, and what happens when the update says something a progress bar
// does not expect. That lives in Model, which has no Fyne in it, so these tests
// run without a display.
package fyneui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-idavoll/idunn/core/hook"
)

// at is a fixed instant plus an offset, so the throughput estimate is driven by
// arithmetic rather than by sleeping.
func at(seconds float64) time.Time {
	return time.Unix(1_700_000_000, 0).Add(time.Duration(seconds * float64(time.Second)))
}

// A staging event carries bytes, and that is the one place a real fraction
// exists. It is taken as given rather than recomputed: core derived it from the
// signed lengths, which is the only total that is known before the update
// starts.
func TestStagingEventsDriveTheBar(t *testing.T) {
	var m Model
	m.Apply(hook.Event{
		Phase:      hook.PhaseDownload,
		Message:    "downloading lib/libcef.so",
		Progress:   0.25,
		File:       "lib/libcef.so",
		FileIndex:  2,
		FileCount:  8,
		Source:     hook.SourceDownload,
		BytesDone:  256 << 20,
		BytesTotal: 1 << 30,
	}, at(0))

	s := m.Snapshot()
	if s.Progress != 0.25 {
		t.Errorf("Progress = %v, want 0.25", s.Progress)
	}
	if s.BytesDone != 256<<20 || s.BytesTotal != 1<<30 {
		t.Errorf("bytes = %d/%d", s.BytesDone, s.BytesTotal)
	}
	if want := "256.0 MiB of 1.0 GiB"; s.Headline != want {
		t.Errorf("Headline = %q, want %q", s.Headline, want)
	}
	if want := "Downloading lib/libcef.so (2 of 8)"; s.Detail != want {
		t.Errorf("Detail = %q, want %q", s.Detail, want)
	}
}

// Where the bytes come from is the difference between "this will take a while"
// and "this is nearly done", so it is said in the words that mean it.
func TestTheSourceIsNamed(t *testing.T) {
	for source, want := range map[hook.Source]string{
		hook.SourceReuse:    "Reusing",
		hook.SourcePatch:    "Patching",
		hook.SourceDownload: "Downloading",
		hook.Source(""):     "Staging",
	} {
		var m Model
		m.Apply(hook.Event{
			Phase: hook.PhaseDownload, File: "bin/app", Source: source,
			BytesTotal: 100, FileCount: 1, FileIndex: 1,
		}, at(0))
		if got := m.Snapshot().Detail; !strings.HasPrefix(got, want) {
			t.Errorf("source %q: detail = %q, want it to start with %q", source, got, want)
		}
	}
}

// Everything outside staging has no byte count, and the model must not invent
// one — nor keep showing the throughput of the staging that came before it.
func TestNonStagingEventsHaveNoRate(t *testing.T) {
	var m Model
	m.Apply(hook.Event{Phase: hook.PhaseDownload, Progress: 0.5, BytesDone: 500, BytesTotal: 1000}, at(0))
	m.Apply(hook.Event{Phase: hook.PhaseDownload, Progress: 1, BytesDone: 1000, BytesTotal: 1000}, at(1))
	if m.Snapshot().Rate <= 0 {
		t.Fatal("staging reported no throughput, so the case below proves nothing")
	}

	m.Apply(hook.Event{Phase: hook.PhaseMigrate, Message: "migrating state", Progress: -1}, at(2))
	s := m.Snapshot()
	if s.Rate != 0 || s.Remaining != 0 {
		t.Errorf("a migration reported %v B/s, %v left", s.Rate, s.Remaining)
	}
	if s.Progress != -1 {
		t.Errorf("Progress = %v, want -1 for a phase with no byte count", s.Progress)
	}
	if s.Headline != "Migrating state" {
		t.Errorf("Headline = %q", s.Headline)
	}
}

// Throughput is estimated from the progress itself, and the estimate has to
// settle on a steady rate rather than swing with every report.
func TestThroughputSettlesOnASteadyRate(t *testing.T) {
	var m Model
	const perSecond = 4 << 20
	for i := range 12 {
		m.Apply(hook.Event{
			Phase: hook.PhaseDownload, Progress: float64(i) / 12,
			BytesDone: int64(i) * perSecond, BytesTotal: 12 * perSecond,
			File: "bin/app", Source: hook.SourceDownload,
		}, at(float64(i)))
	}
	s := m.Snapshot()
	if s.Rate < 0.9*perSecond || s.Rate > 1.1*perSecond {
		t.Errorf("Rate = %.0f B/s, want about %d", s.Rate, perSecond)
	}
	if s.Remaining < 500*time.Millisecond || s.Remaining > 2*time.Second {
		t.Errorf("Remaining = %v, want about a second", s.Remaining)
	}
	if !strings.Contains(s.Status(), "left") {
		t.Errorf("Status = %q, want it to say how long is left", s.Status())
	}
}

// A release's progress goes backwards exactly once per file: a reuse candidate
// that failed verification had its scratch file discarded, and those bytes were
// never staged. That is a sample to drop, not a negative rate to show.
func TestProgressGoingBackwardsDoesNotProduceANegativeRate(t *testing.T) {
	var m Model
	m.Apply(hook.Event{
		Phase: hook.PhaseDownload, File: "lib/libcef.so", Source: hook.SourceReuse,
		BytesDone: 900, BytesTotal: 1000, Progress: 0.9,
	}, at(0))
	m.Apply(hook.Event{
		Phase: hook.PhaseDownload, File: "lib/libcef.so", Source: hook.SourceDownload,
		BytesDone: 0, BytesTotal: 1000, Progress: 0,
	}, at(1))
	m.Apply(hook.Event{
		Phase: hook.PhaseDownload, File: "lib/libcef.so", Source: hook.SourceDownload,
		BytesDone: 500, BytesTotal: 1000, Progress: 0.5,
	}, at(2))

	s := m.Snapshot()
	if s.Rate < 0 {
		t.Errorf("Rate = %v; a discarded attempt must not produce a negative throughput", s.Rate)
	}
	if !strings.HasPrefix(s.Detail, "Downloading") {
		t.Errorf("Detail = %q, want the source that is actually running now", s.Detail)
	}
}

// The first failure is the one worth showing. What follows it is usually the
// rollback describing the same accident, and an update that went wrong must not
// be redrawn as one that is merely busy.
func TestTheFirstFailureIsTheOneKept(t *testing.T) {
	first := errors.New("an installed file does not match its verified target")
	var m Model
	m.Apply(hook.Event{Phase: hook.PhaseVerify, Message: "verifying", Err: first}, at(0))
	m.Apply(hook.Event{Phase: hook.PhaseRollback, Message: "rolled back"}, at(1))

	s := m.Snapshot()
	if !errors.Is(s.Err, first) {
		t.Errorf("Err = %v, want the first failure", s.Err)
	}
	if !s.Done {
		t.Error("a rollback did not end the update")
	}
	if s.Status() != first.Error() {
		t.Errorf("Status = %q, want the failure", s.Status())
	}
}

func TestCommitEndsTheUpdate(t *testing.T) {
	var m Model
	m.Apply(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.3.0", Progress: -1}, at(0))
	s := m.Snapshot()
	if !s.Done || s.Err != nil {
		t.Errorf("state after a commit: done=%v err=%v", s.Done, s.Err)
	}
	if s.Headline != "Installed 1.3.0" {
		t.Errorf("Headline = %q", s.Headline)
	}
}

// An event with nothing to say still has to produce a line: a window with an
// empty headline looks broken in a way a phase name does not.
func TestAnEventWithoutAMessageFallsBackToItsPhase(t *testing.T) {
	var m Model
	m.Apply(hook.Event{Phase: hook.PhaseQuiesce, Progress: -1}, at(0))
	if got := m.Snapshot().Headline; got != "Quiesce" {
		t.Errorf("Headline = %q, want the phase", got)
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0:                "0 B",
		512:              "512 B",
		1 << 10:          "1.0 KiB",
		1536:             "1.5 KiB",
		1 << 20:          "1.0 MiB",
		1 << 30:          "1.0 GiB",
		1 << 40:          "1.0 TiB",
		1 << 50:          "1.0 PiB",
		(1 << 50) * 2048: "2048.0 PiB",
	} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// remaining has to refuse to answer rather than overflow: a rate near zero
// against a large release produces a duration no int64 holds.
func TestRemainingRefusesWhatItCannotSay(t *testing.T) {
	for name, tc := range map[string]struct {
		left int64
		rate float64
	}{
		"nothing left":     {0, 1000},
		"no rate yet":      {1000, 0},
		"an absurd wait":   {1 << 62, 1e-9},
		"a negative delta": {-10, 1000},
	} {
		if got := remaining(tc.left, tc.rate); got != 0 {
			t.Errorf("%s: remaining = %v, want 0", name, got)
		}
	}
}
