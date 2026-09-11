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

// Package packui is the screen of cmd/packassist.
//
// It lives here rather than in cmd/ so it can be tested: nothing in this package
// imports fyne.io/fyne/v2/app, so it builds and tests with no OpenGL and no
// display. cmd/packassist is flag parsing and one call into this package.
//
// The assistant writes a pack.yaml and runs the idunn packer over it. It signs
// nothing, generates nothing and reads no key. Where it validates, it is
// advisory: internal/packmodel says so, and so does the screen.
package packui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
	"github.com/go-idavoll/idunn-fyne/internal/packrun"
)

// Options configures the assistant.
type Options struct {
	// Config is the release being described. Nil starts from a blank one for
	// the running platform.
	Config *packmodel.Config

	// Runner publishes. Nil means the assistant can validate and save a
	// pack.yaml but not publish, which is a legitimate way to run it.
	Runner *packrun.Runner

	// ConfigPath is where the pack.yaml is saved and published from.
	ConfigPath string

	// RepoDir is the TUF repository to publish into.
	RepoDir string

	// LookupEnv reads the role-key variables; nil selects the process
	// environment. It is injected so a test never has to set real ones.
	LookupEnv func(string) (string, bool)

	// SourceDateEpoch is the value of SOURCE_DATE_EPOCH, if the environment has
	// one. The packer falls back to it when --now is absent.
	SourceDateEpoch string

	// Now is the clock, injected so the "this repository would already be
	// expired" warning can be tested without waiting a day. Nil selects
	// time.Now.
	Now func() time.Time

	// Run schedules work off the UI goroutine and delivers its result back on
	// it. Nil selects fyneui.Run, which is what a real assistant wants: a
	// publish must never run on the Fyne goroutine.
	//
	// It is injectable so a test can make a publish complete before the
	// assertion instead of alongside it. That matters because Fyne's test
	// driver has no separate UI goroutine at all -- it runs the callback inline
	// on whichever goroutine scheduled it -- so a test that polled the widgets
	// while a publish was in flight would be racing the harness, not the code.
	Run func(work func() error, done func(error))
}

// Wizard is the assistant's screen.
type Wizard struct {
	win  fyne.Window
	opts Options
	cfg  *packmodel.Config

	// problems is the last validation result, indexed by field, so each widget
	// can show its own.
	problems map[string]string

	tabs *container.AppTabs

	// Release step.
	nameEntry, versionEntry, channelEntry *widget.Entry
	minFromEntry, minClientEntry          *widget.Entry
	rolloutSlider                         *widget.Slider
	rolloutLabel                          *widget.Label
	releaseHints                          map[string]*widget.Label

	// Targets step.
	targetsBox *fyne.Container

	// Preview step.
	preview *widget.Entry

	// Publish step.
	publishUI *publishUI

	// summary is the persistent validity line at the bottom.
	summary *widget.Label
}

// New builds the assistant.
func New(win fyne.Window, o Options) *Wizard {
	cfg := o.Config
	if cfg == nil {
		cfg = packmodel.New("linux", "amd64")
	}
	w := &Wizard{
		win:          win,
		opts:         o,
		cfg:          cfg,
		problems:     map[string]string{},
		releaseHints: map[string]*widget.Label{},
	}
	if w.opts.Run == nil {
		w.opts.Run = fyneui.Run
	}
	if w.opts.Now == nil {
		w.opts.Now = time.Now
	}
	w.build()
	w.revalidate()
	return w
}

// Content returns the widget tree.
func (w *Wizard) Content() fyne.CanvasObject {
	return container.NewBorder(nil, w.summary, nil, nil, w.tabs)
}

// Config returns the configuration being edited.
func (w *Wizard) Config() *packmodel.Config { return w.cfg }

// Problems returns the current validation findings, for a test or a caller that
// wants the same answer the screen is showing.
func (w *Wizard) Problems() []packmodel.Problem { return w.cfg.Validate() }

func (w *Wizard) build() {
	w.summary = widget.NewLabel("")
	w.summary.Wrapping = fyne.TextWrapWord

	w.tabs = container.NewAppTabs(
		container.NewTabItem("Release", w.buildRelease()),
		container.NewTabItem("Targets", w.buildTargets()),
		container.NewTabItem("Preview", w.buildPreview()),
		container.NewTabItem("Publish", w.buildPublish()),
	)
	w.tabs.SetTabLocation(container.TabLocationTop)
}

// revalidate re-runs the validator and pushes the result into every widget that
// shows part of it. It is called on every change, which is cheap: validation
// judges the text only and never opens a file.
func (w *Wizard) revalidate() {
	found := w.cfg.Validate()

	w.problems = make(map[string]string, len(found))
	for _, p := range found {
		// The first problem on a field is the one shown; the later ones are
		// usually consequences of it.
		if _, seen := w.problems[p.Field]; !seen {
			w.problems[p.Field] = p.Message
		}
	}

	for field, hint := range w.releaseHints {
		w.setHint(hint, field)
	}
	w.refreshPreview()
	w.refreshSummary(found)
	if w.publishUI != nil {
		w.publishUI.setBlocked(len(found) != 0)
	}
}

func (w *Wizard) setHint(hint *widget.Label, field string) {
	if msg, bad := w.problems[field]; bad {
		hint.SetText(msg)
		hint.Importance = widget.DangerImportance
	} else {
		hint.SetText(fieldHelp[field])
		hint.Importance = widget.LowImportance
	}
	hint.Refresh()
}

func (w *Wizard) refreshSummary(found []packmodel.Problem) {
	if len(found) == 0 {
		// Deliberately not "valid". packmodel judges the text only; whether src
		// exists, and whether two files share content, only the packer knows.
		w.summary.SetText("Nothing left to fix here. The packer has the final say: " +
			"it also checks that every src exists and that no two files hold identical bytes.")
		w.summary.Importance = widget.SuccessImportance
		w.summary.Refresh()
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d thing", len(found))
	if len(found) != 1 {
		b.WriteString("s")
	}
	b.WriteString(" to fix: ")
	for i, p := range found {
		if i == 3 {
			fmt.Fprintf(&b, "and %d more", len(found)-i)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p.Field)
	}
	w.summary.SetText(b.String())
	w.summary.Importance = widget.WarningImportance
	w.summary.Refresh()
}

// fieldHelp is the text a field shows when it is not complaining: the rule,
// stated once, so an operator does not have to provoke an error to learn it.
var fieldHelp = map[string]string{
	"name":                            "Letters, digits, dot, underscore or hyphen. This is the application name.",
	"version":                         "SemVer, e.g. 1.2.0. A leading zero would split the release line.",
	"channel":                         "Lower-case letters, digits and hyphens. Not v1, v2, ... — those name release lines.",
	"requirements.min_from_version":   "Optional. The oldest installed version this update applies over.",
	"requirements.min_client_version": "Optional. The oldest idunn client that can take this release.",
	"rollout":                         "0 offers the release to everyone. A fraction offers it to that share of installations.",
}
