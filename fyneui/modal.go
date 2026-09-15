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
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/go-idavoll/idunn/core/hook"
)

// ModalSize is the size a Modal opens at. Wide enough for a file path and a
// throughput line without wrapping, tall enough for the event log to be worth
// showing.
var ModalSize = fyne.NewSize(520, 360)

// Modal is the sidecar with its window management already written: a [Panel]
// inside a modal dialog over the host's own window.
//
// It exists so that wiring an update into a UI is two lines and no decisions.
// A host that wants the panel somewhere of its own — a tab, a preferences pane,
// a status area beside its own content — uses [NewPanel] directly and places the
// widget itself; everything below is the part such a host would otherwise have
// to write again.
//
//	ui := fyneui.NewModal(win)
//	defer ui.Close()
//
//	u, err := updater.New(updater.Options{
//		// ...
//		Observe: ui, // hook.Observer
//		Prompt:  ui, // hook.Prompter
//	})
//
// Drive the updater with [Run], never from a widget callback — see the deadlock
// note on [Panel.Confirm], which is the one threading rule this helper cannot
// take away.
//
// # When it appears
//
// The modal raises itself, once, when the update becomes something a person
// should see: the first event from a phase past the check, or a confirmation.
// That distinction is the whole reason it is not simply shown on the first
// event — a background check that finds nothing emits check events and nothing
// else, and a window that appeared for it would interrupt someone to say that
// nothing happened.
//
// It does not close itself. The last thing an update says is how it ended, and a
// dialog that vanished on the commit would take that away; the dismiss button is
// the user's, and [Modal.Hide] is the host's.
//
// Closing it means closing it. A release reports progress hundreds of times, and
// a modal that reappeared on the next one would be impossible to get rid of, so
// a dialog that was dismissed stays dismissed: the update goes on, the panel
// keeps up with it, and nothing puts it back on screen until [Modal.Show] or
// [Modal.Reset] says so.
type Modal struct {
	panel  *Panel
	parent fyne.Window

	mu    sync.Mutex
	dlg   *dialog.CustomDialog
	shown bool

	// dismissed records that this modal has been taken away on purpose --
	// by the user through the dialog's own button, or by the host through
	// Hide. It is what stops the next event putting it straight back.
	dismissed bool
}

var (
	_ hook.Observer = (*Modal)(nil)
	_ hook.Prompter = (*Modal)(nil)
)

// NewModal builds a Modal over parent and starts drawing.
//
// Unlike [NewPanel] there is no Start to call. A fyne.Window can only come from
// a fyne.App, so by the time a caller has a parent to pass, the app exists and
// the render pump has somewhere to marshal to — which is the only thing Start
// was ever waiting for.
//
// parent must not be nil: a modal over nothing has nowhere to draw and no
// window to ask a question in.
func NewModal(parent fyne.Window) *Modal { return newModal(parent, fyne.Do) }

// newModal is NewModal with the UI-thread seam injected, so the tests can drive
// the whole helper -- the raise decision included -- without a main loop.
func newModal(parent fyne.Window, do func(func())) *Modal {
	m := &Modal{
		panel:  NewPanel(parent),
		parent: parent,
	}
	m.panel.do = do
	m.panel.afterRender = m.afterRender
	m.panel.Start()
	return m
}

// Panel is the panel inside the modal, for a host that wants to read its
// [Panel.Snapshot] or [Panel.Events] — to log the transaction it is showing, or
// to render the same numbers in a tray tooltip.
func (m *Modal) Panel() *Panel { return m.panel }

// OnEvent implements hook.Observer by handing the event to the panel.
//
// It adds nothing of its own on this path deliberately. OnEvent runs on the
// updater's goroutine inside an open transaction, and the decision to raise a
// window is a Fyne call; it is made in [Modal.afterRender] instead, which the
// panel already runs on the Fyne goroutine.
func (m *Modal) OnEvent(e hook.Event) {
	if m == nil {
		return
	}
	m.panel.OnEvent(e)
}

// Confirm implements hook.Prompter. It makes sure the modal is up — a question
// arriving over an empty screen has no context to be answered in — and then
// puts the question to the user through the panel.
//
// It blocks until the user answers, the context is done, or the interface fails
// to draw the dialog, exactly as [Panel.Confirm] does, and it must not be called
// from the Fyne goroutine for the same reason.
func (m *Modal) Confirm(ctx context.Context, question string) (bool, error) {
	if m == nil {
		return false, ErrPrompt
	}
	m.Show()
	return m.panel.Confirm(ctx, question)
}

// Show raises the modal, and overrides an earlier dismissal: a host asking for
// it explicitly outranks the rule that keeps a closed dialog closed.
//
// It is safe to call from any goroutine and from any number of them: showing a
// dialog that is already up is a no-op.
func (m *Modal) Show() {
	m.panel.do(func() {
		m.mu.Lock()
		m.dismissed = false
		m.mu.Unlock()
		m.showHere()
	})
}

// Hide takes the modal away without ending the transaction behind it. Events
// keep arriving and keep being folded into the panel, so a host that shows it
// again sees where the update has got to and not where it was — and nothing
// raises it again on its own in the meantime.
func (m *Modal) Hide() {
	m.panel.do(func() {
		m.mu.Lock()
		d, shown := m.dlg, m.shown
		m.mu.Unlock()

		m.onClosed()
		if shown && d != nil {
			d.Hide() // fires onClosed again; it is idempotent.
		}
	})
}

// Reset clears the panel for a new transaction and takes the modal away, so the
// next update raises it again on its own. A host calls it before each
// CheckForUpdate/Apply run.
//
// It also forgets that the last transaction's modal was dismissed. A person who
// closed the window on one update did not thereby opt out of seeing the next.
func (m *Modal) Reset() {
	m.panel.Reset()
	m.Hide()
	m.panel.do(func() {
		m.mu.Lock()
		m.dismissed = false
		m.mu.Unlock()
	})
}

// Close stops drawing. It is idempotent and safe from any goroutine. The modal
// is not hidden: a host closing down has a window going away anyway, and hiding
// through a main loop that may already have stopped is how a shutdown hangs.
func (m *Modal) Close() { m.panel.Close() }

// Snapshot is what the modal is currently showing.
func (m *Modal) Snapshot() Snapshot { return m.panel.Snapshot() }

// afterRender runs at the end of every render, on the Fyne goroutine, and is
// where the modal decides to raise itself. See the note on [Modal] for why a
// check on its own is not enough to earn a window.
func (m *Modal) afterRender(s Snapshot) {
	if s.Seq == 0 || s.Phase == "" || s.Phase == hook.PhaseCheck {
		return
	}
	m.mu.Lock()
	dismissed := m.dismissed
	m.mu.Unlock()

	if dismissed {
		return
	}
	m.showHere()
}

// onClosed records that the dialog has gone away on purpose. Fyne calls it when
// the user taps the dismiss button, and Hide calls it directly; it is idempotent
// so that the two paths can overlap.
func (m *Modal) onClosed() {
	m.mu.Lock()
	m.shown = false
	m.dismissed = true
	m.mu.Unlock()
}

// showHere raises the modal. It touches Fyne, so it runs only on the Fyne
// goroutine: every caller reaches it through do or through a render.
func (m *Modal) showHere() {
	m.mu.Lock()
	if m.shown || m.parent == nil {
		m.mu.Unlock()
		return
	}
	if m.dlg == nil {
		m.dlg = dialog.NewCustom("Update", "Close", m.panel.Widget(), m.parent)
		m.dlg.Resize(ModalSize)
		m.dlg.SetOnClosed(m.onClosed)
	}
	d := m.dlg
	m.shown = true
	m.mu.Unlock()

	d.Show()
}
