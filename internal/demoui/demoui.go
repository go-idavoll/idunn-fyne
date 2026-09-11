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

// Package demoui is the screen of cmd/demo.
//
// It lives here rather than in cmd/ so it can be tested: nothing in this package
// imports fyne.io/fyne/v2/app, so it builds and tests with no OpenGL and no
// display. cmd/demo is flag parsing and one call into this package.
//
// The demo exists to show that a host needs nothing from idunn beyond
// hook.Observer and hook.Prompter in order to present an update, and to be the
// worked example of how to wire them up — including the parts that are easy to
// get wrong: running Apply off the UI goroutine, and treating a deferred or
// declined update as an outcome rather than a failure.
package demoui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn/core/fsx"
	"github.com/go-idavoll/idunn/core/installer"
	"github.com/go-idavoll/idunn/core/launch"
	"github.com/go-idavoll/idunn/core/release"
	"github.com/go-idavoll/idunn/core/updater"
)

// Options configures the demo.
type Options struct {
	// Root is the installation root. It is a real directory on the real
	// filesystem, because installer.InstalledVersion reads through fsx.OS()
	// unconditionally and an in-memory filesystem would be invisible to it.
	Root string

	// Channel to follow.
	Channel string

	// Resolver stands in for the trust client. In the default mode it is an
	// internal/fixture.Resolver; with the --tuf-* flags it is a real
	// *trust.Client, and not one line of this package changes between the two.
	// That is the demonstration.
	Resolver updater.Resolver

	// ClientVersion and BuildTime are this client's own identity, checked
	// against a descriptor's MinClientVersion and used as the first floor under
	// the system clock.
	ClientVersion string
	BuildTime     time.Time

	// Now is the injected clock.
	Now func() time.Time

	// Busy makes a fake application lock report that an instance is running, so
	// the deferred-to-restart path can be shown on demand. It is the one
	// terminal state a UI most often renders wrongly.
	Busy bool

	// QuiesceTimeout keeps the busy demonstration short.
	QuiesceTimeout time.Duration

	// AfterInstall, if set, is called after a successful install with the
	// version that landed. The demo uses it to advance its fixture channel, so
	// that installing 1.0.0 is followed by 1.1.0 being offered -- an upgrade,
	// with something to swap away from and an old version to collect, rather
	// than a first install and then nothing.
	AfterInstall func(installed string)
}

// Window is the demo screen.
type Window struct {
	opts  Options
	panel *fyneui.ProgressWindow

	installed *widget.Label
	waiting   *widget.Label
	rootLabel *widget.Label
	card      *widget.Label
	status    *widget.Label

	check   *widget.Button
	install *widget.Button
	restart *widget.Button

	content *fyne.Container

	// offered is the release the last check found, held so the demo can show
	// more about it than the Prompter's bare question string carries.
	offered *updater.Release
}

// New builds the demo screen. panel is the sidecar under demonstration; it is
// wired as both the Observer and the Prompter.
func New(panel *fyneui.ProgressWindow, o Options) *Window {
	w := &Window{opts: o, panel: panel}
	w.build()
	w.refreshState()
	return w
}

// Content returns the widget tree.
func (w *Window) Content() fyne.CanvasObject { return w.content }

func (w *Window) build() {
	w.rootLabel = widget.NewLabel(w.opts.Root)
	w.rootLabel.Wrapping = fyne.TextWrapBreak
	w.installed = widget.NewLabel("")
	w.waiting = widget.NewLabel("")
	w.status = widget.NewLabel("Ready.")
	w.status.Wrapping = fyne.TextWrapWord

	w.card = widget.NewLabel(noRelease)
	w.card.Wrapping = fyne.TextWrapWord
	w.card.TextStyle = fyne.TextStyle{Monospace: true}

	w.check = widget.NewButton("Check for updates", w.onCheck)
	w.install = widget.NewButton("Install", w.onInstall)
	w.install.Disable()
	w.restart = widget.NewButton("Finish the pending update (simulated restart)", w.onRestart)
	w.restart.Disable()

	head := container.NewVBox(
		widget.NewLabelWithStyle("Installation", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		w.rootLabel, w.installed, w.waiting,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Release", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		w.card,
		widget.NewSeparator(),
	)
	foot := container.NewVBox(
		widget.NewSeparator(),
		w.status,
		container.NewHBox(w.check, w.install),
		w.restart,
	)
	w.content = container.NewBorder(head, foot, nil, nil, w.panel.Widget())
}

const noRelease = "No release has been offered yet. Press \"Check for updates\"."

// Describe renders everything a release descriptor carries.
//
// The closing note is not padding. A descriptor has no release notes, no
// download size, no publication date and no URL — core/release/descriptor.go
// keeps only app attributes, and the hash and length live in TUF metadata. A UI
// designed against fields that do not exist ends up inventing a second,
// unsigned metadata channel to fill the gap, which is precisely the thing the
// trust model is there to prevent.
func Describe(d *release.Descriptor, from string) string {
	if d == nil {
		return noRelease
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  (%s, %s-%s)\n", d.Name, d.Version, d.Channel, d.OS, d.Arch)
	if from == "" {
		fmt.Fprintf(&b, "This would be a first install.\n")
	} else {
		fmt.Fprintf(&b, "Upgrading from %s.\n", from)
	}
	if d.Rollout > 0 && d.Rollout < 1 {
		fmt.Fprintf(&b, "Staged rollout: %.0f%% of installations.\n", d.Rollout*100)
	}
	if d.Requirements.MinFromVersion != "" {
		fmt.Fprintf(&b, "Requires an installed version of at least %s.\n", d.Requirements.MinFromVersion)
	}
	if d.Requirements.MinClientVersion != "" {
		fmt.Fprintf(&b, "Requires a client of at least %s.\n", d.Requirements.MinClientVersion)
	}
	fmt.Fprintf(&b, "Layout schema %d.\n\nFiles (%d):\n", d.LayoutSchema, len(d.Files))
	for _, f := range d.Files {
		fmt.Fprintf(&b, "  %-20s %-5s %04o  %s\n", f.Dst, f.Kind, f.Mode, f.Target)
	}
	b.WriteString("\nA release descriptor carries no release notes, no download size, no\n" +
		"publication date and no URL. The above is everything there is.")
	return b.String()
}

// updaterOptions builds the Options for one transaction. Hooks are wired here
// and nowhere else, so there is exactly one place to read to see what the demo
// asks of idunn.
func (w *Window) updaterOptions() updater.Options {
	o := updater.Options{
		Trust:         w.opts.Resolver,
		FS:            fsx.OS(),
		Root:          w.opts.Root,
		Channel:       w.opts.Channel,
		ClientVersion: w.opts.ClientVersion,
		BuildTime:     w.opts.BuildTime,
		Now:           w.opts.Now,
		Observe:       w.panel,
		Prompt:        w.panel,
		Policy: updater.Policy{
			RetainVersions: 2,
			// Cheap here, and it is the check that catches what happened to the
			// bytes between staging and the swap.
			VerifyAfterApply: true,
			QuiesceTimeout:   w.opts.QuiesceTimeout,
		},
	}
	if w.opts.Busy {
		// The zero BusyPolicy is BusyAbort, which fails. Deferring is the
		// interesting one: it is a success that looks like an error to a UI
		// that only checks err != nil.
		o.Lock = busyLock{}
		o.Policy.OnBusy = updater.BusyDeferToRestart
	}
	return o
}

// busyLock is an application lock that is always held by somebody else. A false
// with a nil error is an answer, not a failure — that is the AppLock contract.
type busyLock struct{}

func (busyLock) TryLock(context.Context) (bool, error) { return false, nil }
func (busyLock) Unlock() error                         { return nil }

func (w *Window) onCheck() {
	w.busy(true)
	w.panel.Reset()
	w.setStatus("Checking...")

	// Never on the Fyne goroutine: Confirm blocks and needs that goroutine to
	// draw its dialog. This is the pattern the demo exists to show.
	var rel *updater.Release
	fyneui.Run(func() error {
		u, err := updater.New(w.updaterOptions())
		if err != nil {
			return err
		}
		rel, err = u.CheckForUpdate(context.Background())
		return err
	}, func(err error) {
		w.busy(false)
		if err != nil {
			w.report(err)
			return
		}
		w.offered = rel
		if rel == nil {
			w.card.SetText(noRelease)
			w.setStatus("Already up to date.")
			return
		}
		w.card.SetText(Describe(rel.Descriptor, rel.FromVersion))
		w.setStatus("Update available: " + rel.Descriptor.Version)
		w.install.Enable()
	})
}

func (w *Window) onInstall() {
	if w.offered == nil {
		return
	}
	rel := w.offered
	w.busy(true)
	w.panel.Reset()
	w.setStatus("Installing " + rel.Descriptor.Version + "...")

	fyneui.Run(func() error {
		u, err := updater.New(w.updaterOptions())
		if err != nil {
			return err
		}
		return u.Apply(context.Background(), rel)
	}, func(err error) {
		w.busy(false)
		w.offered = nil
		w.card.SetText(noRelease)
		w.report(err)
		if err == nil && w.opts.AfterInstall != nil {
			w.opts.AfterInstall(rel.Descriptor.Version)
		}
		w.refreshState()
	})
}

// onRestart is the launcher's job, done by hand: settle the install root and
// finish whatever a previous run deferred.
func (w *Window) onRestart() {
	w.busy(true)
	w.panel.Reset()
	w.setStatus("Finishing the pending update...")

	var res launch.Result
	fyneui.Run(func() error {
		var err error
		res, err = launch.Start(context.Background(), launch.Options{
			FS:             fsx.OS(),
			Root:           w.opts.Root,
			Observe:        w.panel,
			RetainVersions: 2,
		})
		return err
	}, func(err error) {
		w.busy(false)
		if err != nil {
			w.report(err)
		} else {
			switch {
			case res.Applied:
				w.setStatus("Applied the deferred update: " + res.ToVersion + ".")
			case res.Skipped:
				w.setStatus("An instance still holds the lock; the update stays deferred.")
			default:
				w.setStatus("Nothing was pending.")
			}
		}
		w.refreshState()
	})
}

// report turns whatever core returned into a sentence. It is the whole reason
// fyneui.Explain exists: a deferred or declined update is not a failure, and
// showing it as one teaches people that a working updater is broken.
func (w *Window) report(err error) {
	x := fyneui.Explain(err)
	if err == nil {
		w.setStatus("Done. " + x.Detail)
		return
	}
	w.setStatus(x.Title + " — " + x.Detail)
}

func (w *Window) setStatus(s string) { w.status.SetText(s) }

func (w *Window) busy(b bool) {
	for _, btn := range []*widget.Button{w.check, w.install, w.restart} {
		if b {
			btn.Disable()
		}
	}
	if !b {
		w.check.Enable()
		w.refreshState()
	}
}

// refreshState re-reads the installation from disk. The demo never caches what
// it could ask idunn, so what it shows is what is really there.
func (w *Window) refreshState() {
	version, err := installer.InstalledVersion(w.opts.Root)
	switch {
	case err != nil:
		w.installed.SetText("Installed: unreadable (" + err.Error() + ")")
	case version == "":
		w.installed.SetText("Installed: nothing yet")
	default:
		w.installed.SetText("Installed: " + version)
	}

	pending, err := launch.Waiting(fsx.OS(), w.opts.Root)
	switch {
	case err != nil:
		w.waiting.SetText("Pending: unreadable (" + err.Error() + ")")
		w.restart.Disable()
	case pending == nil:
		w.waiting.SetText("Pending: nothing")
		w.restart.Disable()
	default:
		w.waiting.SetText("Pending: " + pending.ToVersion + ", waiting for a restart")
		w.restart.Enable()
	}
}
