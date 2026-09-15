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
	"math"
	"time"

	"github.com/go-idavoll/idunn/core/hook"
)

// rateWindow is how much of the past the throughput estimate remembers. Short
// enough to react when a release moves from reused files to a download, long
// enough not to swing on one buffer.
const rateWindow = 3 * time.Second

// Model turns the event stream into a [Snapshot].
//
// It is deliberately separate from the widgets: the updater calls the Observer
// from its own goroutine, and what has to happen there is cheap bookkeeping,
// not a repaint. It has no Fyne in it, so what a release's progress *means* is
// worked out in one place and tested without a display.
type Model struct {
	snap Snapshot

	// The throughput estimate: an exponentially weighted average over the
	// samples seen, which is enough to be steady without keeping a history.
	lastAt    time.Time
	lastBytes int64
	rate      float64
}

// Apply folds one event into the model. now is the observation time, injected so
// the throughput estimate is testable without sleeping.
func (m *Model) Apply(e hook.Event, now time.Time) {
	m.snap.Phase = e.Phase
	m.snap.Message = e.Message
	m.snap.Seq++
	if e.Err != nil {
		// The first failure is the one worth showing: what follows it is
		// usually the rollback describing the same accident.
		if m.snap.Err == nil {
			m.snap.Err = e.Err
		}
	}
	switch e.Phase {
	case hook.PhaseCommit, hook.PhaseRollback:
		m.snap.Done = true
	}

	m.snap.Headline = headline(e)
	m.snap.Detail = detail(e)
	m.snap.Progress = e.Progress
	m.snap.BytesDone, m.snap.BytesTotal = e.BytesDone, e.BytesTotal

	if e.BytesTotal == 0 {
		// Outside staging there is no throughput to report, and carrying the
		// last one forward would show a rate for a migration.
		m.reset()
		return
	}
	m.sample(e.BytesDone, now)
	m.snap.Rate = m.rate
	m.snap.Remaining = remaining(e.BytesTotal-e.BytesDone, m.rate)
}

// Snapshot returns what the widgets should draw. The log and the sequence
// number are the Panel's to fill in; everything the arithmetic produces is here.
func (m *Model) Snapshot() Snapshot { return m.snap }

// sample folds one observation into the throughput estimate.
//
// Progress can go backwards exactly once per file — a reuse candidate that
// failed verification had its scratch file discarded, and those bytes were never
// staged — so a negative delta is not an error to report but a sample to drop.
func (m *Model) sample(done int64, now time.Time) {
	if m.lastAt.IsZero() {
		m.lastAt, m.lastBytes = now, done
		return
	}
	elapsed := now.Sub(m.lastAt)
	if elapsed <= 0 || done < m.lastBytes {
		m.lastAt, m.lastBytes = now, done
		return
	}
	instant := float64(done-m.lastBytes) / elapsed.Seconds()
	if m.rate == 0 {
		m.rate = instant
	} else {
		// A first-order filter with the window as its time constant.
		weight := math.Min(elapsed.Seconds()/rateWindow.Seconds(), 1)
		m.rate += weight * (instant - m.rate)
	}
	m.lastAt, m.lastBytes = now, done
}

func (m *Model) reset() {
	m.lastAt, m.lastBytes, m.rate = time.Time{}, 0, 0
	m.snap.Rate, m.snap.Remaining = 0, 0
}

// remaining is how long the rest takes at the current rate, or zero when that
// cannot honestly be said.
func remaining(left int64, rate float64) time.Duration {
	if left <= 0 || rate <= 0 {
		return 0
	}
	seconds := float64(left) / rate
	if seconds > float64(math.MaxInt64/int64(time.Second)) {
		return 0
	}
	return time.Duration(seconds * float64(time.Second)).Round(time.Second)
}

// headline is the sentence at the top of the window.
//
// The updater's own message already says what is happening in the words core
// chose; this capitalises it and, while a release is being written, appends the
// size so the user can see what they are waiting for.
func headline(e hook.Event) string {
	msg := e.Message
	if msg == "" {
		msg = string(e.Phase)
	}
	if e.Err != nil {
		return upperFirst(msg) + " — failed"
	}
	if e.BytesTotal > 0 {
		return fmt.Sprintf("%s of %s", Bytes(e.BytesDone), Bytes(e.BytesTotal))
	}
	return upperFirst(msg)
}

// detail names the file being written, where its bytes come from, and its place
// in the release.
//
// Naming the source matters: a UI that says "downloading" while a gigabyte is
// copied off the local disk is telling the user the wrong thing about how long
// this will take.
func detail(e hook.Event) string {
	if e.File == "" {
		return ""
	}
	verb := "Staging"
	switch e.Source {
	case hook.SourceReuse:
		verb = "Reusing"
	case hook.SourcePatch:
		verb = "Patching"
	case hook.SourceDownload:
		verb = "Downloading"
	}
	if e.FileCount > 0 {
		return fmt.Sprintf("%s %s (%d of %d)", verb, e.File, e.FileIndex, e.FileCount)
	}
	return verb + " " + e.File
}

// Status is the line under the bar: throughput and what is left of it.
func (s Snapshot) Status() string {
	if s.Err != nil {
		return s.Err.Error()
	}
	if s.Rate <= 0 {
		return s.Detail
	}
	line := fmt.Sprintf("%s/s", Bytes(int64(s.Rate)))
	if s.Remaining > 0 {
		line += ", " + s.Remaining.String() + " left"
	}
	if s.Detail == "" {
		return line
	}
	return s.Detail + " · " + line
}

// Bytes renders a size the way a person reads it. Binary multiples, because
// that is what a file manager and a download dialog both show.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}
