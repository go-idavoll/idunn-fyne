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

// Package fyneui is the Fyne UI sidecar for idunn: it renders update lifecycle
// events into Fyne widgets and asks the user to confirm an update.
//
// Two entry points, one renderer. [Modal] is the helper — a panel in a dialog
// that manages its own appearance — and [Panel] is the widget tree underneath
// it, for a host that would rather place the progress itself.
//
// It implements two of idunn's optional hooks and nothing else — hook.Observer
// and hook.Prompter — which is the whole of the contract a UI sidecar is allowed
// to have (idunn docs/design.md §8). Every decision about whether to trust or
// apply anything stays in idunn's core; a sidecar that started deciding things
// would be a fork of core wearing a UI.
//
// # The four facts this package is built around
//
// The hook surface is narrower and sharper than it looks, and each of these was
// read out of idunn's source rather than assumed:
//
//  1. OnEvent runs synchronously on the updater's goroutine, inside an open
//     transaction, and nothing buffers it (core/updater/apply.go:425). An
//     Observer that blocks stalls an update with a journal open.
//  2. A panic in an Observer is not recovered. idunn says so in as many words:
//     "an Observer that panics is the host's problem". This package is that part
//     of the host, so it simply never panics.
//  3. hook.Event.Progress carries a real fraction only while staging, where it
//     is the bytes written against the total taken from the signed lengths, and
//     -1 everywhere else. It is a fraction of staging and not of the update, so
//     [Fraction] places it inside the span the phase owns rather than handing it
//     to the bar; over the rest of a transaction the bar remains a step count,
//     and is never labelled as a percentage of a download.
//  4. fyne.Do dereferences fyne.CurrentApp, which is nil until an app is
//     started. Calling it from OnEvent before the host has built its app is a
//     nil-pointer panic on the updater's goroutine, which by (2) is fatal.
//
// Together those mean OnEvent must not reach a Fyne symbol at all. It records
// the event under a mutex and makes one non-blocking send on a one-slot channel;
// a pump goroutine this package owns picks that up and renders through the
// injected UI-thread seam.
//
// # Using it
//
// [Modal] is the whole sidecar with its window management already written, and
// it is what a host should reach for first: a panel in a modal dialog over the
// host's own window, raising itself when an update becomes something a person
// should see.
//
//	ui := fyneui.NewModal(win)
//	defer ui.Close()
//
//	u, err := updater.New(updater.Options{
//		// ...
//		Observe: ui,
//		Prompt:  ui,
//	})
//
// [Panel] is the same thing without the dialog, for a host that wants the
// progress in a place of its own — a tab, a preferences pane, a status area
// beside its own content:
//
//	ui := fyneui.NewPanel(win)
//	defer ui.Close()
//	win.SetContent(ui.Widget())
//	ui.Start() // from the Fyne goroutine, once the window exists
//
// Drive the updater with [Run], never from a widget callback directly — see the
// threading contract on [Panel.Confirm]. That is the one rule neither of them
// can take away, because Go cannot tell which goroutine is the UI one.
package fyneui

import (
	"errors"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn/core/hook"
)

// ErrPrompt is a confirmation that could not be put in front of a person. It is
// a refusal, not an approval: idunn treats a Prompter error as an abort, and an
// update nobody could confirm is one that must not be installed.
var ErrPrompt = errors.New("fyneui: cannot confirm")

// LogSize bounds the retained event log. A transaction emits on the order of ten
// events, so this is generous; it exists so a pathological run cannot grow the
// slice without limit on the updater's goroutine.
const LogSize = 256

// DefaultConfirmTimeout bounds how long [Panel.Confirm] waits for the
// UI goroutine to draw the dialog. See the deadlock note on Confirm.
const DefaultConfirmTimeout = 5 * time.Second

// Snapshot is everything the widgets need in order to draw, as of the last event
// received. It is a value: the pump copies it under the lock and renders outside
// the lock, so rendering can never block OnEvent.
//
// Everything above Log is worked out by [Model], which has no Fyne in it, so
// what a release's progress *means* is decided in one place and tested without a
// display. The widgets only copy the result into labels and a bar.
type Snapshot struct {
	// Phase is the lifecycle phase the last event came from, and Message is
	// what core said about it, in core's own words.
	Phase   hook.Phase
	Message string

	// Headline is the one-line description of what is happening now: the size
	// staged against the size to stage, while a release is being written, and
	// the message otherwise.
	Headline string

	// Detail names the file being written and where its bytes come from, or is
	// empty outside staging.
	Detail string

	// Progress is the fraction as received from core: how far through the
	// PHASE the transaction is, or -1 when there is nothing to be precise
	// about. Only staging reports a real one. [Fraction] turns it into a
	// position in the whole update; this is the raw value, and a bar must not
	// be driven from it directly.
	Progress float64

	// BytesDone and BytesTotal are the release's byte progress, both zero
	// outside staging.
	BytesDone  int64
	BytesTotal int64

	// Rate is the current throughput in bytes per second, 0 until there is
	// enough to estimate from. Remaining is how long the rest would take at
	// that rate, or 0 when it cannot be said.
	Rate      float64
	Remaining time.Duration

	// Err is set once something failed, and stays set: an update that went
	// wrong must not be redrawn as one that is merely busy.
	Err error

	// Done is set once the update reached its terminal phase, successfully or
	// not.
	Done bool

	// Seq is monotonic; 0 means nothing has happened yet.
	Seq uint64

	// Log is the retained event log, oldest first.
	Log []hook.Event
}

// Panel renders idunn lifecycle events and asks for confirmation. It
// implements hook.Observer and hook.Prompter.
//
// The zero value is not usable; call [New]. A Panel is safe for
// concurrent use, which is the whole point of it: OnEvent arrives on idunn's
// goroutine while the widgets live on Fyne's.
type Panel struct {
	// win is the parent for dialogs. It may be nil, in which case the widget
	// tree still renders and Confirm fails closed with ErrPrompt.
	win fyne.Window

	// ConfirmTimeout bounds the wait for the UI goroutine to draw a
	// confirmation. Zero selects DefaultConfirmTimeout.
	ConfirmTimeout time.Duration

	// do marshals a function onto the Fyne UI goroutine. It is injected rather
	// than hardcoded to fyne.Do for two reasons: tests drive the adapter without
	// a running app, and a stalled UI can be simulated exactly instead of
	// approximately. New installs fyne.Do.
	do func(func())

	// after is the watchdog timer, injected so the timeout is testable in
	// microseconds instead of seconds.
	after func(time.Duration) <-chan time.Time

	// now is the clock behind the throughput estimate, injected so the rate is
	// testable by arithmetic instead of by sleeping.
	now func() time.Time

	// mu guards the model. The model, not the widgets, is the ground truth:
	// every render reads it, so a render that arrives late still paints the
	// truth rather than a stale frame.
	mu    sync.Mutex
	model Model
	ring  []hook.Event

	// drawn is the Seq of the last snapshot actually painted. The model runs
	// ahead of the widgets by design -- that is what coalescing means -- and
	// this is the watermark that says by how much.
	drawn uint64

	// afterRender runs at the end of every render, on the Fyne goroutine. It
	// is the seam [Modal] uses to decide when to raise itself, and it is a
	// field rather than an interface because the only implementer lives in
	// this package.
	afterRender func(Snapshot)

	// kick carries at most one pending render. A dropped send is not a lost
	// update: the render already pending will read the current model.
	kick chan struct{}

	stop     chan struct{}
	stopOnce sync.Once
	started  bool
	pumpDone chan struct{}

	// Widgets below are touched only on the Fyne goroutine.
	phase   *widget.Label
	message *widget.Label
	detail  *widget.Label
	bar     *widget.ProgressBar
	banner  *widget.Label
	list    *widget.List
	content *fyne.Container
}

// NewPanel builds a Panel parented to win and prepares its widgets. win may be
// nil when the caller only wants the widget tree; Confirm then fails closed,
// because a confirmation nobody can see is not a confirmation.
//
// Events are recorded from this moment, but nothing is drawn until [Panel.Start].
// [NewModal] is the same panel with that ceremony, and the dialog around it,
// already taken care of.
func NewPanel(win fyne.Window) *Panel {
	w := &Panel{
		win:      win,
		do:       fyne.Do,
		after:    time.After,
		now:      time.Now,
		kick:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		pumpDone: make(chan struct{}),
	}
	w.build()
	return w
}

// Start begins drawing. Call it from the Fyne goroutine once the window exists.
//
// Events that arrived before Start are not lost — they are in the model, and the
// first render paints the accumulated state. Start is idempotent.
func (w *Panel) Start() {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()

	go w.pump()
	w.nudge()
}

// Widget returns the tree to place in a window or a larger layout. Returning it
// rather than taking the window over lets a host put the progress beside its own
// content instead of in a dialog this package chose for it.
func (w *Panel) Widget() fyne.CanvasObject { return w.content }

// Close stops drawing. It is idempotent and safe to call from any goroutine.
// Events delivered after Close are still recorded — dropping the tail of a
// transaction would lose exactly the part that says how it ended — they simply
// stop being drawn.
func (w *Panel) Close() {
	w.stopOnce.Do(func() {
		close(w.stop)
		w.mu.Lock()
		started := w.started
		w.mu.Unlock()
		if started {
			<-w.pumpDone
		}
	})
}

// Reset clears the event log and the progress, ready for a new transaction.
//
// A host calls it before each CheckForUpdate/Apply run. Without it the panel
// would show the previous transaction's events alongside the new ones, and the
// progress bar would appear to jump backwards as the next run starts at
// PhaseCheck — which is not a bug in the bar but the honest consequence of
// treating two runs as one.
func (w *Panel) Reset() {
	w.mu.Lock()
	w.model = Model{}
	w.ring = w.ring[:0]
	w.mu.Unlock()

	w.nudge()
}

// Events returns a copy of the retained event log, oldest first. It is the model
// the widgets are drawn from, exposed so a host can log the same transaction it
// is showing rather than observing it twice.
func (w *Panel) Events() []hook.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]hook.Event, len(w.ring))
	copy(out, w.ring)
	return out
}

// Snapshot returns the current model. Mainly useful to a host that wants to
// render the same state its own way.
func (w *Panel) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.model.Snapshot()
	s.Log = make([]hook.Event, len(w.ring))
	copy(s.Log, w.ring)
	return s
}

// Run executes work on a new goroutine and delivers its result back on the Fyne
// UI goroutine.
//
// Every call into core/updater belongs here. Calling updater.Apply from a widget
// callback runs it on the Fyne goroutine, where the confirmation dialog can
// never be drawn — see the deadlock note on [Panel.Confirm]. Run exists
// so that the correct thing is also the shorter thing to type.
func Run(work func() error, done func(error)) {
	go func() {
		err := work()
		if done == nil {
			return
		}
		fyne.Do(func() { done(err) })
	}()
}

func (w *Panel) build() {
	w.phase = widget.NewLabel("")
	w.phase.TextStyle = fyne.TextStyle{Bold: true}

	w.message = widget.NewLabel("Idle.")
	w.message.Wrapping = fyne.TextWrapWord
	w.message.TextStyle = fyne.TextStyle{Bold: true}

	w.detail = widget.NewLabel("")
	w.detail.Wrapping = fyne.TextWrapWord

	w.bar = widget.NewProgressBar()
	// The label on the bar is the step, never a percentage. The bar's position
	// mixes a byte fraction of staging with a step count over the rest of the
	// transaction (see [Fraction]), so a "%" on it would be a number no single
	// thing in the update actually reports.
	w.bar.TextFormatter = func() string {
		return StepLabel(w.Snapshot().Phase)
	}

	w.banner = widget.NewLabel("")
	w.banner.Wrapping = fyne.TextWrapWord
	w.banner.Hide()

	w.list = widget.NewList(
		func() int {
			w.mu.Lock()
			defer w.mu.Unlock()
			return len(w.ring)
		},
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			label, ok := o.(*widget.Label)
			if !ok {
				return
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			if i < 0 || i >= len(w.ring) {
				return
			}
			label.SetText(LogLine(w.ring[i]))
		},
	)

	head := container.NewVBox(w.phase, w.message, w.bar, w.detail, w.banner)
	w.content = container.NewBorder(head, nil, nil, nil, w.list)
}
