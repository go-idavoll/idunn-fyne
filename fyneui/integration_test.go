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

package fyneui_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn-fyne/internal/fixture"
	"github.com/go-idavoll/idunn/core/fsx"
	"github.com/go-idavoll/idunn/core/hook"
	"github.com/go-idavoll/idunn/core/release"
	"github.com/go-idavoll/idunn/core/updater"
)

// refTime is a fixed clock. idunn checks a known-good time floor before every
// refresh and apply, so the clock is an input, not ambient state.
var refTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// alwaysYes answers the Prompter without a UI, for the tests that are about the
// update rather than about the dialog.
type alwaysYes struct{ *fyneui.ProgressWindow }

func (alwaysYes) Confirm(context.Context, string) (bool, error) { return true, nil }

// failingMigrator fails on the way in and succeeds on the way back, which is
// what a real Migrator contract requires of its inverse.
type failingMigrator struct{ rolledBack bool }

func (m *failingMigrator) Migrate(hook.Context) error  { return errors.New("schema 7 is not supported") }
func (m *failingMigrator) Rollback(hook.Context) error { m.rolledBack = true; return nil }

func newUpdater(t *testing.T, root string, res *fixture.Resolver, o updater.Options) *updater.Updater {
	t.Helper()

	o.Trust = res
	o.FS = fsx.OS()
	o.Root = root
	o.Channel = fixture.Channel
	o.OS, o.Arch = runtime.GOOS, runtime.GOARCH
	o.ClientVersion = "1.0.0"
	o.BuildTime = refTime.Add(-24 * time.Hour)
	o.Now = func() time.Time { return refTime }

	u, err := updater.New(o)
	if err != nil {
		t.Fatalf("updater.New: %v", err)
	}
	return u
}

func publish(res *fixture.Resolver, version string, req release.Requirements) {
	d, blobs := fixture.Build(version, runtime.GOOS, runtime.GOARCH, req)
	res.Publish(d, blobs)
}

// TestAdapterDrivesARealUpdate is the evidence for IDN-19: hook.Observer and
// hook.Prompter, and nothing else, are enough to drive a complete update.
//
// Everything below the resolver is genuine — staging, the transaction journal,
// the atomic swap, garbage collection — and the events the panel renders are the
// ones core really emits. There is no TUF repository, no signing key and no
// network, because updater.Resolver is an exported interface and updater.New
// hands the same object to its stager.
func TestAdapterDrivesARealUpdate(t *testing.T) {
	test.NewTempApp(t)

	root := t.TempDir()
	res := fixture.NewResolver()
	publish(res, "1.0.0", release.Requirements{})

	ui := fyneui.New(test.NewTempWindow(t, widget.NewLabel("host")))
	defer ui.Close()

	u := newUpdater(t, root, res, updater.Options{
		Observe: ui,
		Prompt:  alwaysYes{ui},
		Policy: updater.Policy{
			RetainVersions:   2,
			VerifyAfterApply: true,
		},
	})

	// A first install: an empty root has no installed version, so this is the
	// fresh-install path through the very same Apply.
	rel, err := u.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if rel == nil {
		t.Fatal("CheckForUpdate found nothing to install into an empty root")
	}
	if err := u.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply 1.0.0: %v (%s)", err, fyneui.Explain(err).Title)
	}

	// Now the upgrade, which is the interesting one: it has something to swap
	// away from and something to garbage-collect. Reset first: these are two
	// transactions, and the bar legitimately starts over for the second.
	ui.Reset()
	publish(res, "1.1.0", release.Requirements{MinFromVersion: "1.0.0"})

	rel, err = u.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if rel == nil {
		t.Fatal("1.1.0 was published but CheckForUpdate reported nothing")
	}
	if rel.Descriptor.Version != "1.1.0" {
		t.Fatalf("offered version = %q, want 1.1.0", rel.Descriptor.Version)
	}
	if rel.FromVersion != "1.0.0" {
		t.Errorf("FromVersion = %q, want 1.0.0", rel.FromVersion)
	}
	if err := u.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply 1.1.0: %v (%s)", err, fyneui.Explain(err).Title)
	}

	// The bytes on disk are the proof the swap really happened.
	got, err := os.ReadFile(filepath.Join(root, "current", "share", "notes.txt"))
	if err != nil {
		t.Fatalf("reading the installed file: %v", err)
	}
	if !strings.Contains(string(got), "demo 1.1.0") {
		t.Errorf("installed notes.txt = %q, want the 1.1.0 content", got)
	}

	// And the adapter saw the real transaction, in the real order.
	assertPhaseOrder(t, ui.Events())

	if x := fyneui.Explain(nil); !x.Benign() {
		t.Error("a successful update is not presented as benign")
	}
}

// assertPhaseOrder checks the observed events against the sequence idunn emits,
// and — the point of the exercise — that Fraction never sends the bar backwards
// over the events that actually arrived.
func assertPhaseOrder(t *testing.T, events []hook.Event) {
	t.Helper()

	if len(events) == 0 {
		t.Fatal("the Observer received no events at all")
	}

	var (
		seen = map[hook.Phase]bool{}
		bar  float64
	)
	for _, e := range events {
		seen[e.Phase] = true
		f := fyneui.Fraction(fyneui.Snapshot{Phase: e.Phase, Progress: e.Progress})
		if f < 0 {
			continue // a hold: rollback, or a phase added upstream.
		}
		if f < bar {
			t.Errorf("the progress bar would move backwards at phase %q (%v after %v); "+
				"the phase order does not match what idunn emits", e.Phase, f, bar)
		}
		bar = f
	}

	for _, want := range []hook.Phase{
		hook.PhaseCheck, hook.PhaseDownload, hook.PhaseApply,
		hook.PhaseVerify, hook.PhaseCommit,
	} {
		if !seen[want] {
			t.Errorf("phase %q was never observed; events were %v", want, phases(events))
		}
	}
}

func phases(events []hook.Event) []hook.Phase {
	out := make([]hook.Phase, 0, len(events))
	for _, e := range events {
		out = append(out, e.Phase)
	}
	return out
}

// TestAdapterShowsARealRollback is the negative twin. A failing Migrator makes
// core undo a transaction that was already partly applied, and the adapter has
// to render that as a migration failure rather than as "something went wrong".
func TestAdapterShowsARealRollback(t *testing.T) {
	test.NewTempApp(t)

	root := t.TempDir()
	res := fixture.NewResolver()
	publish(res, "1.0.0", release.Requirements{})

	ui := fyneui.New(test.NewTempWindow(t, widget.NewLabel("host")))
	defer ui.Close()

	// The first install must succeed, so there is something to roll back to.
	u := newUpdater(t, root, res, updater.Options{
		Observe: ui,
		Prompt:  alwaysYes{ui},
		Policy:  updater.Policy{RetainVersions: 2},
	})
	rel, err := u.CheckForUpdate(context.Background())
	if err != nil || rel == nil {
		t.Fatalf("CheckForUpdate: %v (rel=%v)", err, rel)
	}
	if err := u.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply 1.0.0: %v", err)
	}

	publish(res, "1.1.0", release.Requirements{})
	mig := &failingMigrator{}
	u = newUpdater(t, root, res, updater.Options{
		Observe: ui,
		Prompt:  alwaysYes{ui},
		Migrate: mig,
		Policy:  updater.Policy{RetainVersions: 2},
	})
	rel, err = u.CheckForUpdate(context.Background())
	if err != nil || rel == nil {
		t.Fatalf("CheckForUpdate: %v (rel=%v)", err, rel)
	}

	err = u.Apply(context.Background(), rel)
	if err == nil {
		t.Fatal("Apply succeeded despite a Migrator that refuses")
	}
	if !errors.Is(err, updater.ErrMigrate) {
		t.Fatalf("err = %v, want updater.ErrMigrate", err)
	}
	if !mig.rolledBack {
		t.Error("core never called Rollback")
	}

	x := fyneui.Explain(err)
	if x.Class != fyneui.ClassMigrate {
		t.Errorf("Explain(...).Class = %q, want %q", x.Class, fyneui.ClassMigrate)
	}
	if x.Retry {
		t.Error("a failed migration should not invite a retry")
	}

	// The installation still works, and it is still the old one.
	got, err := os.ReadFile(filepath.Join(root, "current", "share", "notes.txt"))
	if err != nil {
		t.Fatalf("reading the installed file after a rollback: %v", err)
	}
	if !strings.Contains(string(got), "demo 1.0.0") {
		t.Errorf("after a rollback the install reads %q, want the 1.0.0 content", got)
	}

	var sawRollback bool
	for _, e := range ui.Events() {
		if e.Phase == hook.PhaseRollback {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Errorf("no rollback phase reached the Observer; events were %v", phases(ui.Events()))
	}
}

// TestAdapterReportsADeclinedUpdateAsAnOutcome covers the path a UI most easily
// gets wrong: the user said no, and that is not a failure of anything.
func TestAdapterReportsADeclinedUpdateAsAnOutcome(t *testing.T) {
	test.NewTempApp(t)

	root := t.TempDir()
	res := fixture.NewResolver()
	publish(res, "1.0.0", release.Requirements{})

	ui := fyneui.New(test.NewTempWindow(t, widget.NewLabel("host")))
	defer ui.Close()

	u := newUpdater(t, root, res, updater.Options{
		Observe: ui,
		Prompt:  refuser{},
		Policy:  updater.Policy{RetainVersions: 2},
	})
	rel, err := u.CheckForUpdate(context.Background())
	if err != nil || rel == nil {
		t.Fatalf("CheckForUpdate: %v (rel=%v)", err, rel)
	}

	err = u.Apply(context.Background(), rel)
	if !errors.Is(err, updater.ErrDeclined) {
		t.Fatalf("err = %v, want updater.ErrDeclined", err)
	}
	if x := fyneui.Explain(err); !x.Benign() {
		t.Errorf("a declined update is shown at severity %v; it is an outcome, not a failure",
			x.Severity)
	}
	if _, err := os.Stat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Error("a declined update left something installed")
	}
}

type refuser struct{}

func (refuser) Confirm(context.Context, string) (bool, error) { return false, nil }
