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
	"context"
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn/core/updater"
)

// newPromptWindow builds a ProgressWindow with a window to parent dialogs and an
// inline UI-thread seam, which is what the Fyne test driver does anyway.
func newPromptWindow(t *testing.T) *ProgressWindow {
	t.Helper()
	test.NewTempApp(t)
	w := New(test.NewTempWindow(t, widget.NewLabel("host")))
	w.do = func(fn func()) { fn() }
	t.Cleanup(w.Close)
	return w
}

// answerWith runs Confirm on its own goroutine -- which is where a host must
// call it from -- waits for the dialog to be on screen, then taps one of its
// buttons by the label this package sets itself.
func answerWith(t *testing.T, w *ProgressWindow, ctx context.Context, label string) (bool, error) {
	t.Helper()

	shown := make(chan struct{})
	inline := func(fn func()) { fn() }
	var closed bool
	w.do = func(fn func()) {
		inline(fn)
		if !closed {
			closed = true
			close(shown)
		}
	}

	type result struct {
		ok  bool
		err error
	}
	res := make(chan result, 1)
	go func() {
		ok, err := w.Confirm(ctx, "Install demo 1.1.0 now?")
		res <- result{ok, err}
	}()

	select {
	case <-shown:
	case <-time.After(10 * time.Second):
		t.Fatal("the confirmation dialog was never shown")
	}

	tapButton(t, w.win, label)

	select {
	case r := <-res:
		return r.ok, r.err
	case <-time.After(10 * time.Second):
		t.Fatalf("Confirm did not return after %q was tapped", label)
		return false, nil
	}
}

func tapButton(t *testing.T, win fyne.Window, label string) {
	t.Helper()
	top := win.Canvas().Overlays().Top()
	if top == nil {
		t.Fatal("no overlay on the canvas; the dialog is not showing")
	}
	for _, o := range test.LaidOutObjects(top) {
		if b, ok := o.(*widget.Button); ok && b.Text == label {
			test.Tap(b)
			return
		}
	}
	t.Fatalf("no button labelled %q in the dialog", label)
}

// TestConfirmYes and TestConfirmNo are the two ordinary answers. The labels are
// set by this package (SetConfirmText / SetDismissText), so asserting on them is
// asserting on our own wording, not on Fyne's defaults or a locale.
func TestConfirmYes(t *testing.T) {
	w := newPromptWindow(t)
	ok, err := answerWith(t, w, context.Background(), "Install now")
	if err != nil {
		t.Fatalf("Confirm returned %v, want no error", err)
	}
	if !ok {
		t.Error("Confirm = false after the confirm button was tapped, want true")
	}
}

func TestConfirmNo(t *testing.T) {
	w := newPromptWindow(t)
	ok, err := answerWith(t, w, context.Background(), "Not now")
	if err != nil {
		t.Fatalf("Confirm returned %v, want no error", err)
	}
	if ok {
		t.Error("Confirm = true after the dismiss button was tapped, want false")
	}
	// idunn turns a plain false into updater.ErrDeclined, which Explain must
	// report as an ordinary outcome rather than a failure.
	if x := Explain(updater.ErrDeclined); !x.Benign() {
		t.Errorf("a declined update is presented as severity %v, want benign", x.Severity)
	}
}

// TestConfirmWithoutWindowFailsClosed is the fail-closed rule: a confirmation
// that cannot be shown is a refusal, never a silent yes.
func TestConfirmWithoutWindowFailsClosed(t *testing.T) {
	test.NewTempApp(t)
	w := New(nil)
	defer w.Close()

	ok, err := w.Confirm(context.Background(), "Install demo 1.1.0 now?")
	if ok {
		t.Fatal("Confirm said yes with no window to ask in")
	}
	if !errors.Is(err, ErrPrompt) {
		t.Fatalf("err = %v, want ErrPrompt", err)
	}
}

// TestConfirmNilReceiver keeps the nil-hook convention from panicking.
func TestConfirmNilReceiver(t *testing.T) {
	var w *ProgressWindow
	ok, err := w.Confirm(context.Background(), "Install?")
	if ok || !errors.Is(err, ErrPrompt) {
		t.Fatalf("got (%v, %v), want (false, ErrPrompt)", ok, err)
	}
}

// TestConfirmHonoursACancelledContext covers a context already done on entry:
// no dialog should be raised at all.
func TestConfirmHonoursACancelledContext(t *testing.T) {
	w := newPromptWindow(t)

	shown := false
	w.do = func(fn func()) { shown = true; fn() }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, err := w.Confirm(ctx, "Install demo 1.1.0 now?")
	if ok {
		t.Fatal("Confirm said yes on a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if shown {
		t.Error("a dialog was raised for a context that was already cancelled")
	}
}

// TestConfirmCancelledWhileWaiting cancels once the dialog is already up. The
// dialog must be taken down, and the answer must be no.
func TestConfirmCancelledWhileWaiting(t *testing.T) {
	w := newPromptWindow(t)

	ctx, cancel := context.WithCancel(context.Background())
	shown := make(chan struct{})
	var closed bool
	w.do = func(fn func()) {
		fn()
		if !closed {
			closed = true
			close(shown)
		}
	}

	go func() {
		<-shown
		cancel()
	}()

	ok, err := w.Confirm(ctx, "Install demo 1.1.0 now?")
	if ok {
		t.Fatal("a cancelled prompt must not read as approval")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestConfirmWatchdogFiresWhenTheUIIsStalled is the deadlock guard. It
// reproduces exactly what a host does wrong -- calling Apply on the Fyne
// goroutine, so the closure that would draw the dialog never runs -- and asserts
// that Confirm gives up rather than hanging forever.
func TestConfirmWatchdogFiresWhenTheUIIsStalled(t *testing.T) {
	w := newPromptWindow(t)
	w.do = func(func()) {} // the UI goroutine is busy; nothing is ever drawn.

	fired := make(chan time.Time, 1)
	fired <- time.Now()
	w.after = func(time.Duration) <-chan time.Time { return fired }

	ok, err := w.Confirm(context.Background(), "Install demo 1.1.0 now?")
	if ok {
		t.Fatal("a prompt nobody could see must not read as approval")
	}
	if !errors.Is(err, ErrPrompt) {
		t.Fatalf("err = %v, want ErrPrompt", err)
	}
}

// TestConfirmWatchdogStandsDownOnceTheDialogIsUp: a person reading a dialog is
// not a stalled interface, and timing them out would be wrong.
func TestConfirmWatchdogStandsDownOnceTheDialogIsUp(t *testing.T) {
	w := newPromptWindow(t)

	fired := make(chan time.Time, 1)
	fired <- time.Now() // expires immediately, but only after the dialog is up.
	w.after = func(time.Duration) <-chan time.Time { return fired }

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ok, err := w.Confirm(ctx, "Install demo 1.1.0 now?")
	if ok {
		t.Fatal("nobody confirmed")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded: the watchdog must stand "+
			"down once the dialog is actually on screen", err)
	}
}

// TestConfirmTimeoutDefaults covers the accessor rather than waiting five
// seconds for the real default.
func TestConfirmTimeoutDefaults(t *testing.T) {
	w := New(nil)
	defer w.Close()
	if got := w.confirmTimeout(); got != DefaultConfirmTimeout {
		t.Errorf("confirmTimeout() = %v, want %v", got, DefaultConfirmTimeout)
	}
	w.ConfirmTimeout = time.Second
	if got := w.confirmTimeout(); got != time.Second {
		t.Errorf("confirmTimeout() = %v, want the configured 1s", got)
	}
}
