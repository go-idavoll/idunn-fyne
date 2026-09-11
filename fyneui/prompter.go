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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Confirm puts a question to the user and blocks until they answer, the context
// is done, or the interface fails to draw the dialog. It implements
// hook.Prompter.
//
// idunn calls this once, before the journal is opened, so a refusal here costs
// nothing on disk: false becomes updater.ErrDeclined and an error aborts, both
// with the installation untouched. The question arrives as a bare string
// ("Install <name> <version> now?"); a host that wants a richer dialog keeps the
// *updater.Release it got from CheckForUpdate and renders that itself.
//
// # It must not be called from the Fyne goroutine
//
// Confirm blocks by design, and the dialog it raises needs the Fyne goroutine in
// order to be drawn. A host that calls updater.Apply from a widget callback
// therefore blocks the event loop inside Confirm, the dialog is queued behind
// the very goroutine waiting for it, and the application freezes. Use [Run].
//
// Go exposes no way to ask "am I the UI goroutine", and Fyne's own check is
// under internal/, so this cannot be detected and refused. What it can do is
// stop being permanent: if the interface has not drawn the dialog within
// ConfirmTimeout, Confirm gives up with ErrPrompt. The update is refused — the
// fail-closed answer — and the event loop unwinds instead of hanging forever,
// so the mistake shows up as a named error rather than a dead window.
func (w *ProgressWindow) Confirm(ctx context.Context, question string) (bool, error) {
	if w == nil || w.win == nil {
		return false, fmt.Errorf("%w: no window to ask in", ErrPrompt)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	// Buffered, and guarded by a Once: Fyne fires a confirm dialog's callback on
	// every Hide, including a Hide this function itself triggers after the
	// context is done. An unbuffered channel would leave the second fire
	// blocking the UI goroutine forever.
	answer := make(chan bool, 1)
	var once sync.Once
	reply := func(v bool) { once.Do(func() { answer <- v }) }

	var (
		shown     atomic.Bool
		cancelled atomic.Bool
		dlg       atomic.Pointer[dialog.ConfirmDialog]
	)

	w.do(func() {
		// The context can already be done by the time the UI goroutine reaches
		// this. Showing a dialog nobody is waiting for would strand it on screen.
		if cancelled.Load() {
			return
		}
		d := dialog.NewConfirm("Update available", question, reply, w.win)
		d.SetConfirmText("Install now")
		d.SetDismissText("Not now")
		d.SetConfirmImportance(widget.HighImportance)
		dlg.Store(d)
		shown.Store(true)
		d.Show()
	})

	watchdog := w.after(w.confirmTimeout())
	for {
		select {
		case ok := <-answer:
			return ok, nil

		case <-ctx.Done():
			cancelled.Store(true)
			w.do(func() {
				if d := dlg.Load(); d != nil {
					d.Hide() // fires reply(false); the Once absorbs it.
				}
			})
			// A cancelled prompt is a refusal, not a silent yes.
			return false, ctx.Err()

		case <-watchdog:
			if shown.Load() {
				// The dialog is up and a person is reading it. Waiting on a
				// human is not a stall, so stop watching and let them answer.
				watchdog = nil
				continue
			}
			cancelled.Store(true)
			return false, fmt.Errorf(
				"%w: the interface did not draw the confirmation within %s; "+
					"is Apply running on the Fyne goroutine?",
				ErrPrompt, w.confirmTimeout())
		}
	}
}

func (w *ProgressWindow) confirmTimeout() time.Duration {
	if w.ConfirmTimeout > 0 {
		return w.ConfirmTimeout
	}
	return DefaultConfirmTimeout
}
