// Copyright 2026 The idunn Authors
//
// Licensed under the MIT License. See LICENSE for details.

package fyneui

import (
	"context"
	"errors"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/go-idavoll/idunn/core/hook"
)

// ProgressWindow renders an update into a Fyne window. It implements
// hook.Observer and hook.Prompter, which is the whole surface a sidecar needs
// (docs/design.md §8).
//
// # Threads
//
// The updater calls OnEvent and Confirm from its own goroutine, and Fyne widgets
// may only be touched on the main one. Everything here therefore splits in two:
// OnEvent does nothing but fold the event into the Model under a mutex, and the
// repaint is handed to the main loop through fyne.Do.
//
// It also coalesces. A multi-gigabyte release reports every megabyte, and a
// window that repainted once per report would spend the update queueing work it
// then throws away. While a repaint is already queued, further events only
// update the Model, so what the main loop eventually draws is the latest state
// and never a backlog of stale ones.
type ProgressWindow struct {
	// Confirmation is what Confirm answers with when no window is showing —
	// a host that wires a Prompter and then runs without a UI still has to get
	// an answer. The zero value declines, which is the safe reading: an update
	// nobody could agree to is one that does not happen.
	Confirmation bool

	win  fyne.Window
	bar  *widget.ProgressBar
	head *widget.Label
	note *widget.Label

	// do schedules f on the Fyne main loop. It is a field so the tests can run
	// the whole observer path without a driver.
	do func(f func())
	// now is the injected clock behind the throughput estimate.
	now func() time.Time

	mu     sync.Mutex
	model  Model
	queued bool
	prompt *dialog.ConfirmDialog
}

var (
	_ hook.Observer = (*ProgressWindow)(nil)
	_ hook.Prompter = (*ProgressWindow)(nil)
)

// ErrNoWindow reports that a question was asked of a ProgressWindow that has no
// window to ask it in. The configured Confirmation answers instead; this is the
// error only where there is nothing to fall back to.
var ErrNoWindow = errors.New("idunn-fyne: no window to prompt in")

// New builds a ProgressWindow on app a, titled for the application being
// updated. The window is not shown; a host decides when an update becomes
// visible — a background check that finds nothing should not raise a window.
func New(a fyne.App, title string) *ProgressWindow {
	w := &ProgressWindow{
		win:  a.NewWindow(title),
		bar:  widget.NewProgressBar(),
		head: widget.NewLabel("Checking for updates…"),
		note: widget.NewLabel(""),
		do:   fyne.Do,
		now:  time.Now,
	}
	w.head.TextStyle = fyne.TextStyle{Bold: true}
	w.note.Wrapping = fyne.TextWrapWord
	w.win.SetContent(container.NewVBox(w.head, w.bar, w.note))
	w.win.Resize(fyne.NewSize(460, 150))
	return w
}

// Window is the window this renders into, so a host can place, show or close it
// on its own terms.
func (w *ProgressWindow) Window() fyne.Window { return w.win }

// OnEvent implements hook.Observer.
//
// It is called from the goroutine running the update and must not block it: all
// it does is fold the event into the model and, if no repaint is queued yet,
// ask for one.
func (w *ProgressWindow) OnEvent(e hook.Event) {
	w.mu.Lock()
	w.model.Apply(e, w.now())
	already := w.queued
	w.queued = true
	w.mu.Unlock()

	if !already {
		w.do(w.Refresh)
	}
}

// Refresh draws the current state. It runs on the Fyne main loop.
func (w *ProgressWindow) Refresh() {
	w.mu.Lock()
	state := w.model.State()
	w.queued = false
	w.mu.Unlock()

	w.head.SetText(state.Headline)
	w.note.SetText(state.Status())
	switch {
	case state.Fraction >= 0:
		w.bar.Max = 1
		w.bar.SetValue(state.Fraction)
	case state.Done && state.Err == nil:
		// A finished update is full, whatever the last event said about its
		// fraction: the phases after staging have none, and a bar left at
		// nine tenths is how a completed install looks unfinished.
		w.bar.Max = 1
		w.bar.SetValue(1)
	}
}

// State is what the window is currently showing. It exists for tests and for a
// host that wants to render the same numbers somewhere else, such as a tray
// tooltip.
func (w *ProgressWindow) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.model.State()
}

// Confirm implements hook.Prompter: it asks the question in a modal dialog and
// blocks the updater until the user answers.
//
// Blocking is correct here — the updater is asking for a decision and there is
// nothing for it to do until it has one — but it must not block forever. A
// cancelled context answers no and returns the context's error, so an update
// that is being torn down does not wait on a dialog nobody is looking at.
func (w *ProgressWindow) Confirm(ctx context.Context, question string) (bool, error) {
	if w.win == nil {
		return w.Confirmation, ErrNoWindow
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	answer := make(chan bool, 1)
	w.do(func() {
		d := dialog.NewConfirm("Update", question, func(ok bool) {
			// Buffered, so a second answer — a dialog the user manages to
			// dismiss twice — cannot block the main loop.
			select {
			case answer <- ok:
			default:
			}
		}, w.win)
		w.mu.Lock()
		w.prompt = d
		w.mu.Unlock()
		d.Show()
	})

	select {
	case ok := <-answer:
		w.forget()
		return ok, nil
	case <-ctx.Done():
		// The update is being torn down, so the question is moot: take the
		// dialog away rather than leave one on screen that nothing is waiting
		// for an answer to.
		w.mu.Lock()
		d := w.prompt
		w.prompt = nil
		w.mu.Unlock()
		if d != nil {
			w.do(d.Hide)
		}
		return false, ctx.Err()
	}
}

func (w *ProgressWindow) forget() {
	w.mu.Lock()
	w.prompt = nil
	w.mu.Unlock()
}
