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

package packui_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
	"github.com/go-idavoll/idunn-fyne/internal/packrun"
	"github.com/go-idavoll/idunn-fyne/internal/packui"
)

func validConfig() *packmodel.Config {
	return &packmodel.Config{
		Name: "demo", Version: "1.2.0", Channel: "stable",
		Targets: []packmodel.Platform{{
			OS: "linux", Arch: "amd64",
			Files: []packmodel.File{{Src: "build/app", Dst: "bin/app", Kind: "exe"}},
		}},
	}
}

// envWith is a LookupEnv over a fixed map, so a test never touches the real
// role-key variables.
func envWith(m map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := m[name]; return v, ok }
}

func keysPresent(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	out := map[string]string{}
	for _, name := range packrun.RequiredKeys() {
		path := filepath.Join(dir, name+".pem")
		// Not a key: nothing in this program ever opens it, which is the point.
		if err := os.WriteFile(path, []byte("not a key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out[name] = path
	}
	return out
}

// stubBin is a file that exists and is not a directory, which is all
// packrun.Runner.locate asks of --packer-bin. It must be created rather than
// named: "/bin/sh" is not there on Windows, so locate would refuse and the
// publish would never reach the exec seam these tests replace.
func stubBin(t *testing.T) string {
	t.Helper()
	name := "packer"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newWizard(t *testing.T, o packui.Options) *packui.Wizard {
	t.Helper()
	test.NewTempApp(t)
	if o.LookupEnv == nil {
		o.LookupEnv = envWith(nil)
	}
	if o.Run == nil {
		// Synchronous: a publish finishes before the assertion rather than
		// alongside it. See the doc comment on packui.Options.Run.
		o.Run = func(work func() error, done func(error)) { done(work()) }
	}
	w := packui.New(test.NewTempWindow(t, widget.NewLabel("host")), o)
	test.NewTempWindow(t, w.Content())
	test.LaidOutObjects(w.Content())
	return w
}

func TestWizardStartsFromSomethingIncomplete(t *testing.T) {
	w := newWizard(t, packui.Options{})
	// A blank release must not look publishable: it has no name, no version and
	// no files.
	if problems := w.Problems(); len(problems) == 0 {
		t.Error("a brand new configuration reported nothing to fix")
	}
}

func TestWizardAcceptsAValidConfiguration(t *testing.T) {
	w := newWizard(t, packui.Options{Config: validConfig()})
	if problems := w.Problems(); len(problems) != 0 {
		t.Errorf("a valid configuration reported problems: %v", problems)
	}
}

// TestWizardRevalidatesAsTheFormIsEdited is the behaviour the whole first step
// exists for: a publisher learns a field is wrong while they are still looking
// at it, not when the packer refuses ten minutes later.
func TestWizardRevalidatesAsTheFormIsEdited(t *testing.T) {
	w := newWizard(t, packui.Options{Config: validConfig()})

	entry := findEntryWithText(t, w, "1.2.0")
	test.Type(entry, "x") // typed at the cursor: "x1.2.0", no longer SemVer

	var found bool
	for _, p := range w.Problems() {
		if p.Field == "version" {
			found = true
		}
	}
	if !found {
		t.Errorf("typing an invalid version produced no problem on that field: %v", w.Problems())
	}
	if w.Config().Version != "x1.2.0" {
		t.Errorf("the model did not follow the form: version = %q", w.Config().Version)
	}
}

// TestWizardRejectsADangerousDestination: dst goes through the same sanitizer
// the client runs on ingest, so a traversal is refused in the form.
func TestWizardRejectsADangerousDestination(t *testing.T) {
	cfg := validConfig()
	cfg.Targets[0].Files[0].Dst = "../../etc/passwd"

	w := newWizard(t, packui.Options{Config: cfg})

	var msg string
	for _, p := range w.Problems() {
		if strings.HasSuffix(p.Field, ".dst") {
			msg = p.Message
		}
	}
	if msg == "" {
		t.Fatalf("a traversal destination was accepted: %v", w.Problems())
	}
	if !strings.Contains(msg, "escapes the install root") {
		t.Errorf("the message does not say what is wrong: %q", msg)
	}
}

// TestWizardPreviewIsWhatWouldBePublished pins the promise the preview pane
// makes: these are the bytes, not an approximation of them.
func TestWizardPreviewIsWhatWouldBePublished(t *testing.T) {
	cfg := validConfig()
	w := newWizard(t, packui.Options{Config: cfg})

	want, err := packmodel.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := findPreview(t, w); got != string(want) {
		t.Errorf("preview differs from what Marshal would write:\ngot\n%s\nwant\n%s", got, want)
	}
}

// TestWizardPublishIsBlockedWhileTheFormIsWrong. The packer would refuse anyway;
// refusing here means the operator is still looking at the field that is wrong.
func TestWizardPublishIsBlockedWhileTheFormIsWrong(t *testing.T) {
	cfg := validConfig()
	cfg.Version = "not a version"

	w := newWizard(t, packui.Options{
		Config:    cfg,
		Runner:    &packrun.Runner{Bin: stubBin(t)},
		RepoDir:   t.TempDir(),
		LookupEnv: envWith(keysPresent(t)),
	})

	btn := findButton(t, w, "Publish")
	if !btn.Disabled() {
		t.Error("Publish is enabled while the configuration is invalid")
	}
}

// TestWizardPublishRunsThePackerAndLogsIt drives the whole last step through a
// fake process, so the wiring is checked without a packer and without keys.
func TestWizardPublishRunsThePackerAndLogsIt(t *testing.T) {
	const report = `published demo 1.2.0 on channel stable
  role targets      -> version 2
  1 new targets
`
	var gotArgv []string
	runner := &packrun.Runner{
		Bin:     stubBin(t), // the exec seam below means it is never actually run
		Environ: func() []string { return nil },
		Exec: func(_ context.Context, argv, _ []string, stdout, _ io.Writer) (int, error) {
			gotArgv = argv
			_, _ = io.WriteString(stdout, report)
			return 0, nil
		},
	}

	w := newWizard(t, packui.Options{
		Config:     validConfig(),
		Runner:     runner,
		ConfigPath: "pack.yaml",
		RepoDir:    t.TempDir(),
		LookupEnv:  envWith(keysPresent(t)),
	})

	btn := findButton(t, w, "Publish")
	if btn.Disabled() {
		t.Fatal("Publish is disabled for a valid configuration with every key present")
	}
	test.Tap(btn)

	if len(gotArgv) == 0 {
		t.Fatal("the packer was never run")
	}
	joined := strings.Join(gotArgv, " ")
	for _, want := range []string{"publish", "--config", "pack.yaml", "--repo"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %v", want, gotArgv)
		}
	}
	if log := findLog(w); !strings.Contains(log, "published demo 1.2.0") {
		t.Errorf("the packer's output never reached the log:\n%s", log)
	}
}

// TestWizardPublishRefusesWithoutKeysAndDoesNotRunAnything is the fail-closed
// path at the UI level.
func TestWizardPublishRefusesWithoutKeysAndDoesNotRunAnything(t *testing.T) {
	ran := false
	runner := &packrun.Runner{
		Bin:     stubBin(t),
		Environ: func() []string { return nil },
		Exec: func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
			ran = true
			return 0, nil
		},
	}

	w := newWizard(t, packui.Options{
		Config:     validConfig(),
		Runner:     runner,
		ConfigPath: "pack.yaml",
		RepoDir:    t.TempDir(),
		LookupEnv:  envWith(nil), // no role keys at all
	})

	test.Tap(findButton(t, w, "Publish"))
	if ran {
		t.Error("the packer was run with no signing key configured")
	}
}

// TestWizardNeverShowsKeyMaterial. A PEM block in a role-key variable is an
// already-leaked key; copying it onto the screen, into a log or into a
// screenshot would leak it further. The assistant must say what is wrong
// without quoting it.
func TestWizardNeverShowsKeyMaterial(t *testing.T) {
	const secret = "MC4CAQAwBQYDK2VwBCIEILLLSECRETLLL"
	const pem = "-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----"

	w := newWizard(t, packui.Options{
		Config:    validConfig(),
		Runner:    &packrun.Runner{Bin: stubBin(t)},
		RepoDir:   t.TempDir(),
		LookupEnv: envWith(map[string]string{packrun.EnvTargetsKey: pem}),
	})

	var said bool
	walk(w.Content(), func(o any) {
		for _, text := range textOf(o) {
			if strings.Contains(text, secret) {
				t.Errorf("key material reached the screen: %q", text)
			}
			if strings.Contains(text, "holds key material") {
				said = true
			}
		}
	})
	if !said {
		t.Error("the assistant did not say that the variable holds key material")
	}
}

// --- helpers -----------------------------------------------------------------

// walk visits every object in the tree, including the tabs that are not
// currently selected -- a problem shown on a tab nobody is looking at is still
// a problem the assistant is responsible for.
func walk(o fyne.CanvasObject, fn func(any)) {
	if o == nil {
		return
	}
	fn(o)
	switch t := o.(type) {
	case *fyne.Container:
		for _, child := range t.Objects {
			walk(child, fn)
		}
	case *container.AppTabs:
		for _, item := range t.Items {
			walk(item.Content, fn)
		}
	case *container.Scroll:
		walk(t.Content, fn)
	case *widget.Card:
		walk(t.Content, fn)
	}
}

func textOf(o any) []string {
	switch t := o.(type) {
	case *widget.Label:
		return []string{t.Text}
	case *widget.Entry:
		return []string{t.Text, t.PlaceHolder}
	case *widget.Button:
		return []string{t.Text}
	case *widget.Select:
		return []string{t.Selected}
	default:
		return nil
	}
}

func findButton(t *testing.T, w *packui.Wizard, label string) *widget.Button {
	t.Helper()
	var found *widget.Button
	walk(w.Content(), func(o any) {
		if b, ok := o.(*widget.Button); ok && b.Text == label && found == nil {
			found = b
		}
	})
	if found == nil {
		t.Fatalf("no button labelled %q", label)
	}
	return found
}

func findEntryWithText(t *testing.T, w *packui.Wizard, text string) *widget.Entry {
	t.Helper()
	var found *widget.Entry
	walk(w.Content(), func(o any) {
		if e, ok := o.(*widget.Entry); ok && e.Text == text && found == nil {
			found = e
		}
	})
	if found == nil {
		t.Fatalf("no entry holding %q", text)
	}
	return found
}

func findPreview(t *testing.T, w *packui.Wizard) string {
	t.Helper()
	var found string
	walk(w.Content(), func(o any) {
		if e, ok := o.(*widget.Entry); ok && e.MultiLine && strings.HasPrefix(e.Text, "name:") {
			found = e.Text
		}
	})
	if found == "" {
		t.Fatal("no preview pane holding a pack.yaml")
	}
	return found
}

func findLog(w *packui.Wizard) string {
	var found string
	walk(w.Content(), func(o any) {
		if e, ok := o.(*widget.Entry); ok && e.MultiLine && strings.Contains(e.Text, "exit ") {
			found = e.Text
		}
	})
	return found
}

// TestWizardWarnsAboutAReferenceTimeThatWouldPublishAnExpiredRepository is the
// lesson from running the assistant for real: publishing today from a month-old
// SOURCE_DATE_EPOCH produces a timestamp.json that is past its 24-hour expiry
// before it is even written, and every client then refuses the repository --
// correctly, and very confusingly.
func TestWizardWarnsAboutAReferenceTimeThatWouldPublishAnExpiredRepository(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name  string
		epoch time.Time
		warn  bool
	}{
		{"a month ago", now.AddDate(0, -1, 0), true},
		{"two days ago", now.Add(-48 * time.Hour), true},
		{"an hour ago", now.Add(-time.Hour), false},
		{"now", now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWizard(t, packui.Options{
				Config:          validConfig(),
				Runner:          &packrun.Runner{Bin: stubBin(t)},
				RepoDir:         t.TempDir(),
				LookupEnv:       envWith(keysPresent(t)),
				SourceDateEpoch: strconv.FormatInt(tc.epoch.Unix(), 10),
				Now:             func() time.Time { return now },
			})

			warned := false
			walk(w.Content(), func(o any) {
				label, ok := o.(*widget.Label)
				if !ok || label.Hidden {
					return
				}
				if strings.Contains(label.Text, "already expired") {
					warned = true
				}
			})
			if warned != tc.warn {
				t.Errorf("warned = %v, want %v for a reference time of %s", warned, tc.warn, tc.name)
			}
		})
	}
}

// TestWizardDoesNotWarnWithoutAReferenceTime: with neither a field nor
// SOURCE_DATE_EPOCH there is nothing to judge, and the packer will refuse the
// publish on its own terms.
func TestWizardDoesNotWarnWithoutAReferenceTime(t *testing.T) {
	w := newWizard(t, packui.Options{
		Config:    validConfig(),
		Runner:    &packrun.Runner{Bin: stubBin(t)},
		RepoDir:   t.TempDir(),
		LookupEnv: envWith(keysPresent(t)),
		Now:       func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) },
	})
	walk(w.Content(), func(o any) {
		if label, ok := o.(*widget.Label); ok && !label.Hidden &&
			strings.Contains(label.Text, "already expired") {
			t.Error("warned about a reference time that was never given")
		}
	})
}

// TestWizardWarningReadsAsProse. Go renders a ten-day duration as "261h0m0s",
// which is exact and unhelpful in a sentence aimed at a person.
func TestWizardWarningReadsAsProse(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	w := newWizard(t, packui.Options{
		Config:          validConfig(),
		Runner:          &packrun.Runner{Bin: stubBin(t)},
		RepoDir:         t.TempDir(),
		LookupEnv:       envWith(keysPresent(t)),
		SourceDateEpoch: strconv.FormatInt(now.AddDate(0, 0, -10).Unix(), 10),
		Now:             func() time.Time { return now },
	})

	var text string
	walk(w.Content(), func(o any) {
		if label, ok := o.(*widget.Label); ok && !label.Hidden &&
			strings.Contains(label.Text, "already expired") {
			text = label.Text
		}
	})
	if text == "" {
		t.Fatal("no warning shown for a ten-day-old reference time")
	}
	if !strings.Contains(text, "10 days") {
		t.Errorf("the warning does not say how long ago in readable terms: %q", text)
	}
	if strings.Contains(text, "h0m0s") {
		t.Errorf("the warning prints a raw Go duration: %q", text)
	}
}
