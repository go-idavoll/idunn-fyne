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
	"errors"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"github.com/go-idavoll/idunn/core/hook"
)

// newTestWindow builds a Panel whose UI-thread seam runs inline, which
// is what the Fyne test driver does anyway. Tests that need to control when a
// render happens replace do themselves.
func newTestWindow(t *testing.T) *Panel {
	t.Helper()
	test.NewTempApp(t)
	w := NewPanel(nil)
	w.do = func(fn func()) { fn() }
	t.Cleanup(w.Close)
	return w
}

// TestOnEventNeverBlocks is the central guarantee of this package. idunn calls
// OnEvent synchronously, on the updater's goroutine, inside an open transaction:
// an OnEvent that blocks stalls an update mid-flight. Here nothing is draining
// the render channel at all, which is the worst case, and it must still return.
func TestOnEventNeverBlocks(t *testing.T) {
	w := NewPanel(nil) // deliberately not started: no pump, nothing draining kick.
	defer w.Close()

	const events = 100_000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < events; i++ {
			w.OnEvent(hook.Event{Phase: hook.PhaseStage, Message: "staging", Progress: -1})
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("OnEvent blocked with no render pump running; an update would have stalled")
	}

	if got := len(w.Events()); got != LogSize {
		t.Errorf("retained log = %d events, want the bound %d", got, LogSize)
	}
}

// TestOnEventNeverPanics covers the states a host can put this in before its UI
// exists. core does not recover a panicking Observer, so any panic here would
// take the host process down with the update.
func TestOnEventNeverPanics(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    *Panel
	}{
		{"nil receiver", nil},
		{"zero value", &Panel{}},
		{"constructed but never started", NewPanel(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("OnEvent panicked: %v", r)
				}
			}()
			tc.w.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "checking", Progress: -1})
			tc.w.OnEvent(hook.Event{Phase: hook.PhaseCheck, Err: errors.New("boom")})
		})
	}
}

// TestOnEventCoalesces proves the dropped render signals lose nothing: a render
// reads the current model, so the one that does run reports the latest event.
func TestOnEventCoalesces(t *testing.T) {
	w := newTestWindow(t)

	for i := 0; i < 1000; i++ {
		w.OnEvent(hook.Event{Phase: hook.PhaseStage, Message: "staging", Progress: -1})
	}
	w.OnEvent(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.1.0", Progress: -1})

	s := w.Snapshot()
	if s.Phase != hook.PhaseCommit {
		t.Errorf("phase = %q, want %q", s.Phase, hook.PhaseCommit)
	}
	if s.Message != "installed 1.1.0" {
		t.Errorf("message = %q, want the most recent one", s.Message)
	}
	if s.Seq != 1001 {
		t.Errorf("seq = %d, want 1001: every event must be counted even when renders are dropped", s.Seq)
	}
}

// TestStartRendersEventsThatArrivedFirst is the "no window yet" case: a host can
// wire the Observer into updater.Options long before it builds its UI, and the
// events from that window must not be lost.
func TestStartRendersEventsThatArrivedFirst(t *testing.T) {
	test.NewTempApp(t)
	w := NewPanel(nil)
	defer w.Close()

	var (
		mu       sync.Mutex
		rendered []Snapshot
	)
	w.do = func(fn func()) { fn() }

	w.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "checking for updates", Progress: -1})
	w.OnEvent(hook.Event{Phase: hook.PhaseDownload, Message: "staging 1.1.0", Progress: -1})

	if len(rendered) != 0 {
		t.Fatal("rendered before Start")
	}

	// Swap in a recorder now so the first delivery after Start is observable.
	w.do = func(fn func()) {
		mu.Lock()
		rendered = append(rendered, w.Snapshot())
		mu.Unlock()
		fn()
	}
	w.Start()

	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		n := len(rendered)
		var last Snapshot
		if n > 0 {
			last = rendered[n-1]
		}
		mu.Unlock()
		if n > 0 && last.Seq == 2 {
			if last.Phase != hook.PhaseDownload {
				t.Fatalf("first render after Start showed phase %q, want the accumulated %q",
					last.Phase, hook.PhaseDownload)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("Start never rendered the events that arrived before it")
		default:
		}
	}
}

// TestLogIsBounded guards the one unbounded thing OnEvent could otherwise do.
func TestLogIsBounded(t *testing.T) {
	w := NewPanel(nil)
	defer w.Close()

	for i := 0; i < LogSize*3; i++ {
		w.OnEvent(hook.Event{Phase: hook.PhaseGC, Message: "gc", Progress: -1})
	}
	events := w.Events()
	if len(events) != LogSize {
		t.Fatalf("log length = %d, want %d", len(events), LogSize)
	}
	// The retained window must be the newest events, not the oldest.
	w.OnEvent(hook.Event{Phase: hook.PhaseCommit, Message: "last", Progress: -1})
	events = w.Events()
	if got := events[len(events)-1].Message; got != "last" {
		t.Errorf("newest retained event = %q, want %q", got, "last")
	}
}

// TestCloseIsIdempotent covers the ordinary defer-Close-twice shape, and Close
// on a window that was never started.
func TestCloseIsIdempotent(t *testing.T) {
	w := newTestWindow(t)
	w.Start()
	w.Close()
	w.Close()

	never := NewPanel(nil)
	never.Close()
	never.Close()
}

// timeoutAfter is the shared deadline for tests that wait on a goroutine.
func timeoutAfter() <-chan time.Time { return time.After(10 * time.Second) }
