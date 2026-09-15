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
	"fmt"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn/core/hook"
	"github.com/go-idavoll/idunn/core/updater"
)

// renderNow builds a started Panel whose widgets are laid out in a real
// (test-driver) window, so the list callbacks and the bar formatter actually run.
func renderNow(t *testing.T) *Panel {
	t.Helper()
	test.NewTempApp(t)
	w := NewPanel(nil)
	w.do = func(fn func()) { fn() }
	win := test.NewTempWindow(t, w.Widget())
	win.Resize(fyne.NewSize(600, 400))
	t.Cleanup(w.Close)
	return w
}

// layOut forces the widget tree through a layout pass, which is what makes the
// list's length and update callbacks run.
func layOut(w *Panel) {
	test.LaidOutObjects(w.Widget())
	w.list.Refresh()
}

// TestWidgetIsUsableBeforeAnythingHappens: a host builds the tree at startup,
// long before an update runs, and an empty panel must still lay out.
func TestWidgetIsUsableBeforeAnythingHappens(t *testing.T) {
	w := renderNow(t)
	if w.Widget() == nil {
		t.Fatal("Widget() = nil")
	}
	layOut(w)
	if w.message.Text != "Idle." {
		t.Errorf("initial message = %q, want the idle text", w.message.Text)
	}
	if !w.banner.Hidden {
		t.Error("the result banner is visible before anything has happened")
	}
}

// TestRenderTracksASuccessfulTransaction walks the phases idunn really emits and
// checks the panel ends up where a finished update should leave it.
func TestRenderTracksASuccessfulTransaction(t *testing.T) {
	// The pump is deliberately not started: this test drives render itself, and
	// two goroutines rendering into one widget tree is exactly what the pump
	// exists to prevent.
	w := renderNow(t)

	for _, e := range []hook.Event{
		{Phase: hook.PhaseCheck, Message: "checking for updates", Progress: -1},
		{Phase: hook.PhaseCheck, Message: "update available: 1.1.0", Progress: -1},
		{Phase: hook.PhaseDownload, Message: "staging 1.1.0", Progress: -1},
		{Phase: hook.PhaseApply, Message: "installing 1.1.0", Progress: -1},
		{Phase: hook.PhaseCommit, Message: "installed 1.1.0", Progress: -1},
	} {
		w.render(snapshotOf(w, e))
	}
	layOut(w)

	if w.phase.Text != "Finishing" {
		t.Errorf("phase label = %q, want %q", w.phase.Text, "Finishing")
	}
	// The headline is core's own wording, capitalised: outside staging there is
	// no byte count to put there instead.
	if w.message.Text != "Installed 1.1.0" {
		t.Errorf("message = %q, want the last one", w.message.Text)
	}
	// A committed update fills the bar outright rather than stopping at the
	// commit step's own position: the phases after staging report no fraction,
	// and a bar left at the commit step is how a finished install looks unfinished.
	if got := w.bar.Value; got != 1 {
		t.Errorf("bar = %v, want a full bar after a commit (the commit step sits at %v)",
			got, steps[hook.PhaseCommit].from)
	}
	if !w.banner.Hidden {
		t.Error("the failure banner is showing after a clean run")
	}
}

// TestRenderShowsTheByteProgressOfStaging. Staging is the one part of an update
// that has a real number attached, and it is the part people wait through: the
// size, the file, where its bytes come from, and what the throughput says is
// left all belong on screen while it runs.
func TestRenderShowsTheByteProgressOfStaging(t *testing.T) {
	w := renderNow(t)

	// Two events a second apart, so the throughput estimate has something to
	// work from without any sleeping.
	w.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	w.render(snapshotOf(w, hook.Event{
		Phase: hook.PhaseDownload, Message: "staging 1.3.0", Progress: 0,
		File: "lib/libcef.so", FileIndex: 3, FileCount: 9, Source: hook.SourceDownload,
		BytesDone: 0, BytesTotal: 1 << 30,
	}))
	w.now = func() time.Time { return time.Unix(1_700_000_001, 0) }
	w.render(snapshotOf(w, hook.Event{
		Phase: hook.PhaseDownload, Message: "staging 1.3.0", Progress: 0.5,
		File: "lib/libcef.so", FileIndex: 3, FileCount: 9, Source: hook.SourceDownload,
		BytesDone: 512 << 20, BytesTotal: 1 << 30,
	}))
	layOut(w)

	if got, want := w.message.Text, "512.0 MiB of 1.0 GiB"; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}
	if got := w.detail.Text; !strings.Contains(got, "Downloading lib/libcef.so (3 of 9)") {
		t.Errorf("detail = %q, want it to name the file and where its bytes come from", got)
	}
	if got := w.detail.Text; !strings.Contains(got, "/s") || !strings.Contains(got, "left") {
		t.Errorf("detail = %q, want the throughput and what is left of it", got)
	}
	// The bar is a position in the update, not in the download: half the bytes
	// staged is nowhere near half an install.
	if got := w.bar.Value; got >= 0.5 {
		t.Errorf("bar = %v for half-staged bytes; that is a fraction of staging, "+
			"not of the transaction", got)
	}
}

// TestRenderLeavesTheBarWhereAFailureStoppedIt. A full bar over an error message
// is the worst of both: it says the update finished and that it did not.
func TestRenderLeavesTheBarWhereAFailureStoppedIt(t *testing.T) {
	w := renderNow(t)
	boom := errors.New("an installed file does not match its verified target")

	w.render(snapshotOf(w, hook.Event{
		Phase: hook.PhaseDownload, Progress: 0.4, BytesDone: 40, BytesTotal: 100,
	}))
	stopped := w.bar.Value

	w.render(snapshotOf(w, hook.Event{Phase: hook.PhaseVerify, Message: "verifying", Progress: -1, Err: boom}))
	w.render(snapshotOf(w, hook.Event{Phase: hook.PhaseRollback, Message: "rolled back", Progress: -1}))
	layOut(w)

	if got := w.bar.Value; got == 1 {
		t.Error("a failed update filled the bar")
	} else if got < stopped {
		t.Errorf("bar = %v, below the %v it had reached; a rollback is not negative progress", got, stopped)
	}
	if w.banner.Hidden {
		t.Error("the failure banner is hidden after a failure")
	}
}

// TestRenderShowsAFailureBannerWithTheExplainedWording ties the two halves of
// this package together: a raw error from core becomes a sentence a person can
// act on.
func TestRenderShowsAFailureBannerWithTheExplainedWording(t *testing.T) {
	w := renderNow(t)
	err := fmt.Errorf("%w: the application would not stop writing", updater.ErrBusy)
	w.render(snapshotOf(w, hook.Event{
		Phase: hook.PhaseQuiesce, Message: "waiting for the application", Progress: -1, Err: err,
	}))
	layOut(w)

	if w.banner.Hidden {
		t.Fatal("no banner shown for a failed transaction")
	}
	want := Explain(err)
	if !strings.Contains(w.banner.Text, want.Title) {
		t.Errorf("banner = %q, want it to contain %q", w.banner.Text, want.Title)
	}
	if w.banner.Importance != widget.WarningImportance {
		t.Errorf("banner importance = %v, want warning for a busy application",
			w.banner.Importance)
	}
}

// TestRenderHoldsTheBarDuringARollback. The bar must not run backwards while the
// update is being undone -- that reads as a second attempt.
func TestRenderHoldsTheBarDuringARollback(t *testing.T) {
	w := renderNow(t)
	w.render(snapshotOf(w, hook.Event{Phase: hook.PhaseApply, Message: "installing 1.1.0", Progress: -1}))
	at := w.bar.Value

	w.render(snapshotOf(w, hook.Event{
		Phase: hook.PhaseRollback, Message: "rolled back", Progress: -1,
		Err: fmt.Errorf("%w: migration refused", updater.ErrMigrate),
	}))

	if w.bar.Value != at {
		t.Errorf("bar moved from %v to %v during a rollback; it must hold", at, w.bar.Value)
	}
	if w.phase.Text != "Undoing the update" {
		t.Errorf("phase label = %q, want the rollback wording", w.phase.Text)
	}
}

// TestLogListRendersEveryEvent exercises the list's own callbacks, which are the
// part of the widget wiring most likely to index out of bounds.
func TestLogListRendersEveryEvent(t *testing.T) {
	w := renderNow(t)
	w.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "checking for updates", Progress: -1})
	w.OnEvent(hook.Event{Phase: hook.PhaseGC, Message: "gc failed", Progress: -1, Err: errors.New("busy")})
	layOut(w)

	if got := w.list.Length(); got != 2 {
		t.Fatalf("list length = %d, want 2", got)
	}
	// Drive the update callback directly, including the out-of-range indices a
	// refresh racing a truncation would hand it.
	item := widget.NewLabel("")
	w.list.UpdateItem(0, item)
	if !strings.Contains(item.Text, "checking for updates") {
		t.Errorf("row 0 = %q, want the first event", item.Text)
	}
	w.list.UpdateItem(1, item)
	if !strings.Contains(item.Text, "busy") {
		t.Errorf("row 1 = %q, want the error text", item.Text)
	}
	w.list.UpdateItem(-1, item)
	w.list.UpdateItem(99, item)
	w.list.UpdateItem(0, widget.NewSlider(0, 1)) // wrong type: must be ignored
}

// TestBarFormatterIsAStepLabelNotAPercentage. core reports no byte progress at
// all, so a "%" in the bar would be a number this package invented.
func TestBarFormatterIsAStepLabelNotAPercentage(t *testing.T) {
	w := renderNow(t)
	w.OnEvent(hook.Event{Phase: hook.PhaseApply, Message: "installing 1.1.0", Progress: -1})
	got := w.bar.TextFormatter()
	if got != "Installing" {
		t.Errorf("bar text = %q, want the step label %q", got, "Installing")
	}
	if strings.Contains(got, "%") {
		t.Errorf("bar text %q presents a percentage; core emits no byte progress", got)
	}
}

func TestImportanceForEverySeverity(t *testing.T) {
	for _, tc := range []struct {
		sev  Severity
		want widget.Importance
	}{
		{SeverityNone, widget.MediumImportance},
		{SeverityInfo, widget.MediumImportance},
		{SeverityWarning, widget.WarningImportance},
		{SeverityError, widget.DangerImportance},
		{Severity(99), widget.MediumImportance},
	} {
		if got := importanceFor(tc.sev); got != tc.want {
			t.Errorf("importanceFor(%v) = %v, want %v", tc.sev, got, tc.want)
		}
	}
}

// TestRunReportsBackOnTheUIGoroutine covers the helper that exists so hosts do
// not call Apply from a widget callback.
func TestRunReportsBackOnTheUIGoroutine(t *testing.T) {
	test.NewTempApp(t)

	want := errors.New("work failed")
	got := make(chan error, 1)
	Run(func() error { return want }, func(err error) { got <- err })

	select {
	case err := <-got:
		if !errors.Is(err, want) {
			t.Errorf("Run delivered %v, want %v", err, want)
		}
	case <-timeoutAfter():
		t.Fatal("Run never delivered its result")
	}

	// A nil callback is allowed: a host may only want the work done.
	Run(func() error { return nil }, nil)
}

// snapshotOf records e and returns the model that results, so a render test
// drives the same path OnEvent does.
func snapshotOf(w *Panel, e hook.Event) Snapshot {
	w.OnEvent(e)
	return w.Snapshot()
}

// TestResetReturnsThePanelToIdle. Two updates in one session are two
// transactions, and the second must not be shown on top of the first.
func TestResetReturnsThePanelToIdle(t *testing.T) {
	w := renderNow(t)
	w.render(snapshotOf(w, hook.Event{
		Phase: hook.PhaseCommit, Message: "installed 1.1.0", Progress: -1,
		Err: errors.New("gc failed"),
	}))
	if len(w.Events()) == 0 {
		t.Fatal("no events recorded")
	}

	w.Reset()
	w.render(w.Snapshot())
	layOut(w)

	if got := len(w.Events()); got != 0 {
		t.Errorf("log holds %d events after Reset, want 0", got)
	}
	if w.Snapshot().Seq != 0 {
		t.Errorf("seq = %d after Reset, want 0", w.Snapshot().Seq)
	}
	if w.message.Text != "Idle." {
		t.Errorf("message = %q after Reset, want the idle text", w.message.Text)
	}
	if w.bar.Value != 0 {
		t.Errorf("bar = %v after Reset, want 0", w.bar.Value)
	}
	if !w.banner.Hidden {
		t.Error("the previous transaction's banner is still showing after Reset")
	}
}
