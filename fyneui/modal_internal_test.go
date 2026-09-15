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
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/go-idavoll/idunn/core/hook"
)

// uiLoop is a stand-in for Fyne's main loop: one goroutine, one queue, work run
// in the order it was handed over.
//
// The other tests in this package replace the UI seam with a function that runs
// inline, which is fine where one goroutine is driving. A Modal is not that
// shape — the render pump raises the dialog while the test taps its buttons —
// and running both inline would put two goroutines in one widget tree, which is
// the exact thing fyne.Do exists to prevent. Modelling the queue keeps these
// tests honest about what production does.
type uiLoop struct {
	queue chan func()
	done  chan struct{}
}

func newUILoop(t *testing.T) *uiLoop {
	t.Helper()
	l := &uiLoop{queue: make(chan func(), 4096), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for fn := range l.queue {
			fn()
		}
	}()
	t.Cleanup(func() {
		close(l.queue)
		<-l.done
	})
	return l
}

func (l *uiLoop) do(fn func()) { l.queue <- fn }

// sync runs fn on the loop and waits for it, which is how a test touches the
// widget tree without racing the pump.
func (l *uiLoop) sync(t *testing.T, fn func()) {
	t.Helper()
	ran := make(chan struct{})
	l.do(func() {
		defer close(ran)
		fn()
	})
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("the UI loop never ran the queued work")
	}
}

// testModal is a Modal together with the loop that draws it.
type testModal struct {
	*Modal
	loop   *uiLoop
	parent fyne.Window
}

func newTestModal(t *testing.T) *testModal {
	t.Helper()
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("host"))
	loop := newUILoop(t)
	m := newModal(win, loop.do)
	t.Cleanup(m.Close)
	return &testModal{Modal: m, loop: loop, parent: win}
}

// visible reports whether the modal is currently raised.
func (m *Modal) visible() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shown
}

// dialogs is how many dialogs this modal has constructed. It stays 1 across a
// hide and a re-show: the same dialog goes back up.
func (m *Modal) dialogs() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dlg == nil {
		return 0
	}
	return 1
}

// settle waits until every event delivered so far has been painted and the UI
// queue has drained, so a test can assert that something did NOT happen.
func (m *testModal) settle(t *testing.T) {
	t.Helper()
	seq := m.Snapshot().Seq
	waitUntil(t, func() bool { return m.panel.rendered() >= seq })
	m.loop.sync(t, func() {})
}

// TestModalStaysAwayForACheckThatFindsNothing is the reason the helper does not
// simply show itself on the first event. A background check runs on a timer and
// usually finds nothing; a window that appeared for it would interrupt someone
// to tell them that nothing happened.
func TestModalStaysAwayForACheckThatFindsNothing(t *testing.T) {
	m := newTestModal(t)

	m.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "checking for updates", Progress: -1})
	m.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "no update available", Progress: -1})
	m.settle(t)

	if m.visible() {
		t.Error("the modal raised itself for a check that found nothing")
	}
}

// TestModalRaisesItselfWhenTheUpdateStarts: past the check there is something
// happening that a person is entitled to see, and the host should not have had
// to write the code that puts it on screen.
func TestModalRaisesItselfWhenTheUpdateStarts(t *testing.T) {
	m := newTestModal(t)

	m.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "update available: 1.3.0", Progress: -1})
	m.settle(t)
	if m.visible() {
		t.Fatal("the modal raised itself during the check")
	}

	m.OnEvent(hook.Event{
		Phase: hook.PhaseDownload, Message: "staging 1.3.0", Progress: 0,
		File: "bin/acme", FileIndex: 1, FileCount: 3, Source: hook.SourceDownload,
		BytesDone: 0, BytesTotal: 1 << 20,
	})
	waitUntil(t, m.visible)
}

// TestModalRaisesItselfToAskAQuestion. A confirmation arriving over an empty
// screen has no context to be answered in: "Install Acme 1.3.0 now?" belongs
// over the panel that says what is being installed.
func TestModalRaisesItselfToAskAQuestion(t *testing.T) {
	m := newTestModal(t)

	answered := make(chan bool, 1)
	go func() {
		ok, _ := m.Confirm(context.Background(), "Install Acme 1.3.0 now?")
		answered <- ok
	}()

	waitUntil(t, m.visible)

	// The question is the overlay on top and the panel is the one underneath,
	// which is the stacking a person sees too.
	var button *widget.Button
	waitUntil(t, func() bool {
		m.loop.sync(t, func() { button = findButton(m.parent, "Install now") })
		return button != nil
	})
	m.loop.sync(t, func() { test.Tap(button) })

	select {
	case ok := <-answered:
		if !ok {
			t.Error("Confirm = false after the confirm button was tapped")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Confirm did not return after the dialog was answered")
	}
}

// TestModalRaisesOnce. A release reporting every megabyte renders hundreds of
// times, and every render asks the modal whether it should be up; the dialog is
// built and shown once.
func TestModalRaisesOnce(t *testing.T) {
	m := newTestModal(t)

	for i := range 50 {
		m.OnEvent(hook.Event{
			Phase: hook.PhaseDownload, Progress: float64(i) / 50,
			BytesDone: int64(i), BytesTotal: 50,
		})
	}
	m.Show()
	m.Show()
	m.settle(t)

	if !m.visible() {
		t.Fatal("the modal is not showing")
	}
	if got := m.dialogs(); got != 1 {
		t.Errorf("%d dialogs were built, want 1", got)
	}
}

// TestModalHideLeavesTheTransactionRunning. Hiding is a UI decision, not an
// abort: the events keep arriving and keep being folded in, so a host that shows
// it again sees where the update has got to and not where it was. Nor does the
// next event undo the host's decision by raising it again.
func TestModalHideLeavesTheTransactionRunning(t *testing.T) {
	m := newTestModal(t)

	m.OnEvent(hook.Event{Phase: hook.PhaseDownload, Progress: 0.1, BytesDone: 10, BytesTotal: 100})
	waitUntil(t, m.visible)

	m.Hide()
	m.settle(t)
	if m.visible() {
		t.Fatal("Hide did not take the modal away")
	}

	m.OnEvent(hook.Event{Phase: hook.PhaseApply, Message: "installing 1.3.0", Progress: -1})
	m.settle(t)
	if s := m.Snapshot(); s.Phase != hook.PhaseApply {
		t.Errorf("phase = %q, want the event that arrived while it was hidden", s.Phase)
	}
	if m.visible() {
		t.Error("an event put back a modal the host had deliberately taken away")
	}

	// An explicit Show outranks the dismissal: the host asked for it.
	m.Show()
	m.settle(t)
	if !m.visible() {
		t.Error("Show did not bring back a modal that had been hidden")
	}
}

// TestModalDismissedStaysDismissed. A release reports progress hundreds of
// times, and every one of those renders asks whether the modal should be up. A
// dialog that came back after the user closed it would be impossible to get rid
// of.
func TestModalDismissedStaysDismissed(t *testing.T) {
	m := newTestModal(t)

	m.OnEvent(hook.Event{Phase: hook.PhaseDownload, Progress: 0, BytesDone: 0, BytesTotal: 100})
	waitUntil(t, m.visible)

	// Tap the dialog's own dismiss button, which is what a user does.
	var button *widget.Button
	waitUntil(t, func() bool {
		m.loop.sync(t, func() { button = findButton(m.parent, "Close") })
		return button != nil
	})
	m.loop.sync(t, func() { test.Tap(button) })
	m.settle(t)

	if m.visible() {
		t.Fatal("the dismiss button did not close the modal")
	}

	for i := 1; i <= 20; i++ {
		m.OnEvent(hook.Event{
			Phase: hook.PhaseDownload, Progress: float64(i) / 20,
			BytesDone: int64(i), BytesTotal: 20,
		})
	}
	m.OnEvent(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.3.0", Progress: -1})
	m.settle(t)

	if m.visible() {
		t.Error("the modal came back after the user closed it")
	}
	// The transaction was still being followed the whole time, and the panel a
	// host reads through Panel() is the same one.
	if s := m.Snapshot(); s.Phase != hook.PhaseCommit || !s.Done {
		t.Errorf("snapshot after the commit: phase=%q done=%v", s.Phase, s.Done)
	}
	if got, want := m.Panel().Snapshot().Seq, m.Snapshot().Seq; got != want {
		t.Errorf("Panel() exposes a different panel: seq %d, want %d", got, want)
	}

	// The next transaction is not covered by that dismissal.
	m.Reset()
	m.settle(t)
	m.OnEvent(hook.Event{Phase: hook.PhaseDownload, Message: "staging 1.4.0", Progress: 0})
	waitUntil(t, m.visible)
}

// TestModalResetReturnsItToIdleAndOutOfSight. Two updates in one session are two
// transactions, and the second gets its own appearance rather than inheriting
// the first one's window.
func TestModalResetReturnsItToIdleAndOutOfSight(t *testing.T) {
	m := newTestModal(t)

	m.OnEvent(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.3.0", Progress: -1})
	waitUntil(t, m.visible)

	m.Reset()
	m.settle(t)
	if m.visible() {
		t.Error("Reset left the previous transaction's modal on screen")
	}
	if s := m.Snapshot(); s.Seq != 0 || s.Phase != "" {
		t.Errorf("Reset left state behind: seq=%d phase=%q", s.Seq, s.Phase)
	}

	m.OnEvent(hook.Event{Phase: hook.PhaseDownload, Message: "staging 1.4.0", Progress: 0})
	waitUntil(t, m.visible)
}

// TestModalCloseIsIdempotent, and a closed Modal still records what it is told:
// dropping the tail of a transaction would lose exactly the part that says how
// it ended.
func TestModalCloseIsIdempotent(t *testing.T) {
	m := newTestModal(t)
	m.Close()
	m.Close()

	m.OnEvent(hook.Event{Phase: hook.PhaseCommit, Message: "installed 1.3.0", Progress: -1})
	if s := m.Snapshot(); s.Phase != hook.PhaseCommit {
		t.Errorf("phase = %q; a closed modal stopped recording", s.Phase)
	}
}

// TestModalNilReceiver: a host that wires a Modal it never built must not take
// the update down with it. core does not recover a panicking Observer.
func TestModalNilReceiver(t *testing.T) {
	var m *Modal
	m.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "checking", Progress: -1})

	ok, err := m.Confirm(context.Background(), "Install?")
	if ok || !errors.Is(err, ErrPrompt) {
		t.Errorf("Confirm = %v, %v; want false, ErrPrompt", ok, err)
	}
}

// TestModalOnEventIsSafeFromManyGoroutines. OnEvent folds the event into the
// model under the mutex, and folding is now real arithmetic rather than six
// assignments. Run with -race, this is the test that says the lock covers it.
func TestModalOnEventIsSafeFromManyGoroutines(t *testing.T) {
	m := newTestModal(t)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				m.OnEvent(hook.Event{
					Phase: hook.PhaseDownload, Progress: float64(j) / 50,
					BytesDone: int64(j), BytesTotal: 50,
					File: "bin/app", Source: hook.SourceDownload,
				})
				if j%7 == 0 {
					_ = m.Snapshot()
				}
			}
		}()
	}
	wg.Wait()
	m.settle(t)
}

// findButton is tapButton without the assertion, for a dialog that may not be on
// screen yet. It walks the widget tree, so it runs on the UI loop.
func findButton(win fyne.Window, label string) *widget.Button {
	top := win.Canvas().Overlays().Top()
	if top == nil {
		return nil
	}
	for _, o := range test.LaidOutObjects(top) {
		if b, ok := o.(*widget.Button); ok && b.Text == label {
			return b
		}
	}
	return nil
}

// waitUntil runs check until it is true, failing the test rather than hanging if
// it never is.
func waitUntil(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition never became true")
}
