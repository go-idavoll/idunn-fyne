// Copyright 2026 The idunn Authors
//
// Licensed under the MIT License. See LICENSE for details.

package fyneui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/test"

	"github.com/go-idavoll/idunn/core/hook"
)

// newTestWindow builds a ProgressWindow on Fyne's test driver, with the main
// loop replaced by a queue this test drains itself. That is what makes the
// observer path — which is concurrent by contract — assertable without a
// display and without sleeping.
func newTestWindow(t *testing.T) (*ProgressWindow, func()) {
	t.Helper()
	w := New(test.NewApp(), "Acme")
	t.Cleanup(test.NewApp().Quit)

	var mu sync.Mutex
	var queue []func()
	w.do = func(f func()) {
		mu.Lock()
		queue = append(queue, f)
		mu.Unlock()
	}
	w.now = func() time.Time { return at(0) }

	drain := func() {
		for {
			mu.Lock()
			if len(queue) == 0 {
				mu.Unlock()
				return
			}
			f := queue[0]
			queue = queue[1:]
			mu.Unlock()
			f()
		}
	}
	return w, drain
}

// The window draws what the model worked out, and nothing it was not told.
func TestWindowRendersStagingProgress(t *testing.T) {
	w, drain := newTestWindow(t)

	w.OnEvent(hook.Event{
		Phase: hook.PhaseDownload, Message: "downloading lib/libcef.so", Progress: 0.5,
		File: "lib/libcef.so", FileIndex: 3, FileCount: 9, Source: hook.SourceDownload,
		BytesDone: 512 << 20, BytesTotal: 1 << 30,
	})
	drain()

	if got := w.head.Text; got != "512.0 MiB of 1.0 GiB" {
		t.Errorf("headline = %q", got)
	}
	if got := w.note.Text; !strings.Contains(got, "Downloading lib/libcef.so (3 of 9)") {
		t.Errorf("note = %q", got)
	}
	if w.bar.Value != 0.5 {
		t.Errorf("bar = %v, want 0.5", w.bar.Value)
	}
}

// A multi-gigabyte release reports every megabyte. A window that repainted once
// per report would spend the update queueing work it then throws away, so while
// a repaint is queued further events only update the model — and what the main
// loop finally draws is the latest state, never a backlog of stale ones.
func TestRepaintsAreCoalesced(t *testing.T) {
	w, drain := newTestWindow(t)

	var painted int
	var mu sync.Mutex
	queued := 0
	inner := w.do
	w.do = func(f func()) {
		mu.Lock()
		queued++
		mu.Unlock()
		inner(func() {
			mu.Lock()
			painted++
			mu.Unlock()
			f()
		})
	}

	for i := range 100 {
		w.OnEvent(hook.Event{
			Phase: hook.PhaseDownload, Progress: float64(i) / 100,
			BytesDone: int64(i), BytesTotal: 100, File: "bin/app", Source: hook.SourceDownload,
		})
	}
	if queued != 1 {
		t.Errorf("%d repaints queued for 100 events, want 1 until the first one runs", queued)
	}
	drain()
	if painted != 1 {
		t.Errorf("%d repaints ran, want 1", painted)
	}
	// And the one repaint drew the newest state, not the oldest.
	if w.bar.Value != 0.99 {
		t.Errorf("bar = %v, want the latest 0.99", w.bar.Value)
	}

	// Once the queue has drained, the next event asks for a repaint again.
	w.OnEvent(hook.Event{Phase: hook.PhaseDownload, Progress: 1, BytesDone: 100, BytesTotal: 100})
	if queued != 2 {
		t.Errorf("%d repaints queued, want a second one after the first drained", queued)
	}
}

// The updater calls OnEvent from its own goroutine. Nothing here may block it,
// and nothing may race: the model is the only shared state and it is behind a
// mutex. Run with -race, this is the test that says so.
func TestOnEventIsSafeFromManyGoroutines(t *testing.T) {
	w, drain := newTestWindow(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				w.OnEvent(hook.Event{
					Phase: hook.PhaseDownload, Progress: float64(j) / 50,
					BytesDone: int64(j), BytesTotal: 50,
				})
				if j%7 == 0 {
					_ = w.State()
				}
			}
			_ = i
		}()
	}
	wg.Wait()
	drain()
}

// A finished update is full, whatever the last event said about its fraction:
// the phases after staging have none, and a bar left at nine tenths is how a
// completed install looks unfinished.
func TestACommittedUpdateFillsTheBar(t *testing.T) {
	w, drain := newTestWindow(t)

	w.OnEvent(hook.Event{Phase: hook.PhaseDownload, Progress: 0.9, BytesDone: 90, BytesTotal: 100})
	drain()
	w.OnEvent(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.3.0", Progress: -1})
	drain()

	if w.bar.Value != 1 {
		t.Errorf("bar = %v, want a full bar after a commit", w.bar.Value)
	}
	if w.head.Text != "Installed 1.3.0" {
		t.Errorf("headline = %q", w.head.Text)
	}
}

// A failed update keeps the bar where it stopped and says what went wrong. It
// must not be filled: a full bar over an error message is the worst of both.
func TestAFailedUpdateDoesNotFillTheBar(t *testing.T) {
	w, drain := newTestWindow(t)
	boom := errors.New("an installed file does not match its verified target")

	w.OnEvent(hook.Event{Phase: hook.PhaseDownload, Progress: 0.4, BytesDone: 40, BytesTotal: 100})
	drain()
	w.OnEvent(hook.Event{Phase: hook.PhaseVerify, Message: "verifying", Progress: -1, Err: boom})
	drain()
	w.OnEvent(hook.Event{Phase: hook.PhaseRollback, Message: "rolled back", Progress: -1})
	drain()

	if w.bar.Value != 0.4 {
		t.Errorf("bar = %v, want it left where the update stopped", w.bar.Value)
	}
	if w.note.Text != boom.Error() {
		t.Errorf("note = %q, want the failure", w.note.Text)
	}
}

// Confirm blocks the updater until the user answers, which is correct — there is
// nothing for the updater to do until it has a decision.
func TestConfirmAnswersFromTheDialog(t *testing.T) {
	for _, want := range []bool{true, false} {
		w, drain := newTestWindow(t)

		answered := make(chan bool, 1)
		go func() {
			ok, err := w.Confirm(context.Background(), "Install Acme 1.3.0 now?")
			if err != nil {
				t.Errorf("Confirm: %v", err)
			}
			answered <- ok
		}()

		// Wait for the dialog to be queued, then show it and answer it.
		waitFor(t, func() bool {
			drain()
			return w.showing() != nil
		})
		answer := w.showing()
		if want {
			answer.Confirm()
		} else {
			answer.Dismiss()
		}

		select {
		case got := <-answered:
			if got != want {
				t.Errorf("Confirm = %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Confirm did not return after the dialog was answered")
		}
	}
}

// A cancelled update must not sit waiting on a dialog nobody is looking at.
func TestConfirmGivesUpWithTheContext(t *testing.T) {
	w, drain := newTestWindow(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := w.Confirm(ctx, "Install Acme 1.3.0 now?")
		done <- err
	}()
	waitFor(t, func() bool {
		drain()
		return w.showing() != nil
	})
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Confirm ignored the cancelled context")
	}
}

// A context that was already cancelled is answered without raising a dialog at
// all: the update is being torn down, and a window appearing at that moment is
// worse than no window.
func TestConfirmRefusesAnAlreadyCancelledContext(t *testing.T) {
	w, _ := newTestWindow(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, err := w.Confirm(ctx, "Install Acme 1.3.0 now?")
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("Confirm = %v, %v; want false, context.Canceled", ok, err)
	}
	if w.showing() != nil {
		t.Error("a dialog was raised for a cancelled update")
	}
}

// A host that wires a Prompter and then runs without a window still has to get
// an answer. The zero Confirmation declines, which is the safe reading: an
// update nobody could agree to is one that does not happen.
func TestConfirmWithoutAWindow(t *testing.T) {
	var w ProgressWindow
	ok, err := w.Confirm(context.Background(), "Install?")
	if ok || !errors.Is(err, ErrNoWindow) {
		t.Errorf("Confirm = %v, %v; want false, ErrNoWindow", ok, err)
	}

	w.Confirmation = true
	if ok, _ := w.Confirm(context.Background(), "Install?"); !ok {
		t.Error("a host that configured yes was answered no")
	}
}

// waitFor runs check until it is true, failing the test rather than hanging if
// it never is.
func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition never became true")
}

// showing is the confirmation dialog currently raised, if any.
func (w *ProgressWindow) showing() *dialog.ConfirmDialog {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prompt
}
