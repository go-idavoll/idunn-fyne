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
	"github.com/go-idavoll/idunn/core/hook"
)

// OnEvent records one lifecycle event. It implements hook.Observer.
//
// idunn calls this synchronously, on the updater's goroutine, from inside an
// open transaction (core/updater/apply.go:425). So it does four things and
// nothing else: take a mutex, copy the event into the model, append to a bounded
// ring, and make one non-blocking send. Every one of those is total — no
// allocation that can fail, no interface call, no Fyne call — so OnEvent cannot
// block the update and cannot panic.
//
// "Cannot panic" is a correctness requirement here, not politeness: core does
// not recover a panicking Observer, so one would unwind through Apply and take
// the host process with it.
//
// A nil ProgressWindow is a no-op, matching the convention that a nil hook does
// nothing.
func (w *ProgressWindow) OnEvent(e hook.Event) {
	if w == nil {
		return
	}

	w.mu.Lock()
	w.snap.Phase = e.Phase
	w.snap.Message = e.Message
	w.snap.Progress = e.Progress
	w.snap.Err = e.Err
	w.snap.Seq++
	if len(w.ring) == LogSize {
		// Drop the oldest. copy rather than reslicing so the backing array does
		// not grow without bound over a long-lived window.
		copy(w.ring, w.ring[1:])
		w.ring = w.ring[:LogSize-1]
	}
	w.ring = append(w.ring, e)
	w.mu.Unlock()

	w.nudge()
}

// nudge asks for a render without ever waiting for one.
//
// A send on a nil channel is never ready, so a zero-valued ProgressWindow takes
// the default branch instead of blocking forever. A full channel means a render
// is already pending, and that render reads the current model — so dropping this
// signal loses nothing. Coalescing here is exact, not an approximation.
func (w *ProgressWindow) nudge() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// pump turns render requests into renders on the Fyne goroutine. It is the only
// goroutine in this package that touches Fyne, and it exists only between Start
// and Close, by which time the host's app is running.
func (w *ProgressWindow) pump() {
	defer close(w.pumpDone)
	for {
		select {
		case <-w.stop:
			// Deliberately no final render.
			//
			// Close usually runs as the application is shutting down, and once
			// Fyne's main loop has drained its queue it stops marshalling and
			// runs the work inline on the calling goroutine instead. A render
			// here would therefore touch widgets from this goroutine, after the
			// window is already going away -- which Fyne's own thread check
			// catches, and which nobody could see anyway.
			//
			// Nothing is lost: every event nudges the pump, so the terminal
			// event of a transaction has already been drawn, and a host that
			// wants it afterwards has Snapshot and Events.
			return
		case <-w.kick:
			w.deliver()
		}
	}
}

func (w *ProgressWindow) deliver() {
	s := w.Snapshot()
	w.do(func() { w.render(s) })
}

// render writes the snapshot into the widgets. It runs on the Fyne goroutine.
func (w *ProgressWindow) render(s Snapshot) {
	if s.Seq == 0 {
		// Either nothing has happened yet or Reset was just called. Either way
		// the panel goes back to how it started rather than keeping the last
		// transaction's wording under a fresh log.
		w.phase.SetText("")
		w.message.SetText("Idle.")
		w.bar.SetValue(0)
		w.banner.Hide()
		w.list.Refresh()
		return
	}

	w.phase.SetText(StepLabel(s.Phase))
	w.message.SetText(s.Message)

	// A rollback is not negative progress, it is the way back. Moving the bar
	// backwards would read as "it is doing the update again", so the value is
	// held and only the wording changes.
	if f := Fraction(s); f >= 0 {
		w.bar.SetValue(f)
	}

	switch {
	case s.Err != nil:
		x := Explain(s.Err)
		w.banner.SetText(x.Title + " — " + x.Detail)
		w.banner.Importance = importanceFor(x.Severity)
		w.banner.Refresh()
		w.banner.Show()
	default:
		w.banner.Hide()
	}

	w.list.Refresh()
	if n := len(s.Log); n > 0 {
		w.list.ScrollTo(n - 1)
	}
}
