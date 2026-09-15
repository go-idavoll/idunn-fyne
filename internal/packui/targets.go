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

package packui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
)

// buildTargets is the second step: which platforms, and which files for each.
func (w *Wizard) buildTargets() fyne.CanvasObject {
	w.targetsBox = container.NewVBox()
	w.rebuildTargets()

	add := widget.NewButton("Add a platform", func() {
		w.cfg.Targets = append(w.cfg.Targets, packmodel.Platform{OS: "linux", Arch: "amd64"})
		w.rebuildTargets()
		w.revalidate()
	})

	note := widget.NewLabel("Every destination is checked with the same sanitizer the " +
		"client runs when it installs, so a path that would be refused on the way in is " +
		"refused here instead of shipping.")
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance

	return container.NewBorder(
		container.NewVBox(note, add, widget.NewSeparator()), nil, nil, nil,
		container.NewVScroll(w.targetsBox),
	)
}

// rebuildTargets redraws the whole editor.
//
// Rebuilding rather than patching is deliberate: platforms and files are
// inserted and removed, so every row's index -- and therefore the field name its
// problem is reported under -- can change on any edit. Redrawing keeps the
// widget tree and the model in step by construction.
func (w *Wizard) rebuildTargets() {
	w.targetsBox.RemoveAll()
	for i := range w.cfg.Targets {
		w.targetsBox.Add(w.platformCard(i))
	}
	if len(w.cfg.Targets) == 0 {
		w.targetsBox.Add(widget.NewLabel("No platforms yet. A release needs at least one."))
	}
	w.targetsBox.Refresh()
}

func (w *Wizard) platformCard(pi int) fyne.CanvasObject {
	p := &w.cfg.Targets[pi]
	at := fmt.Sprintf("targets[%d]", pi)

	osEntry := widget.NewEntry()
	osEntry.SetText(p.OS)
	osEntry.SetPlaceHolder("linux")
	osEntry.OnChanged = func(s string) { p.OS = s; w.revalidate() }

	archEntry := widget.NewEntry()
	archEntry.SetText(p.Arch)
	archEntry.SetPlaceHolder("amd64")
	archEntry.OnChanged = func(s string) { p.Arch = s; w.revalidate() }

	remove := widget.NewButton("Remove this platform", func() {
		w.cfg.Targets = append(w.cfg.Targets[:pi], w.cfg.Targets[pi+1:]...)
		w.rebuildTargets()
		w.revalidate()
	})

	addFile := widget.NewButton("Add a file", func() {
		p.Files = append(p.Files, packmodel.File{Kind: "data"})
		w.rebuildTargets()
		w.revalidate()
	})

	head := container.NewVBox(
		container.NewGridWithColumns(2,
			container.NewVBox(widget.NewLabel("OS"), osEntry, w.problemLabel(at+".os")),
			container.NewVBox(widget.NewLabel("Arch"), archEntry, w.problemLabel(at+".arch")),
		),
		w.problemLabel(at),
		w.problemLabel(at+".files"),
	)

	files := container.NewVBox()
	for fi := range p.Files {
		files.Add(w.fileRow(pi, fi))
	}

	return widget.NewCard(
		fmt.Sprintf("Platform %d — %s-%s", pi+1, p.OS, p.Arch), "",
		container.NewVBox(head, widget.NewSeparator(), files, addFile, remove, widget.NewSeparator()),
	)
}

func (w *Wizard) fileRow(pi, fi int) fyne.CanvasObject {
	f := &w.cfg.Targets[pi].Files[fi]
	at := fmt.Sprintf("targets[%d].files[%d]", pi, fi)

	src := widget.NewEntry()
	src.SetText(f.Src)
	src.SetPlaceHolder("build/linux-amd64/app")
	src.OnChanged = func(s string) { f.Src = s; w.revalidate() }

	browse := widget.NewButton("Browse...", func() {
		if w.win == nil {
			return
		}
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer func() { _ = r.Close() }()
			src.SetText(r.URI().Path())
		}, w.win)
	})

	dst := widget.NewEntry()
	dst.SetText(f.Dst)
	dst.SetPlaceHolder("bin/app")
	dst.OnChanged = func(s string) { f.Dst = s; w.revalidate() }

	kind := widget.NewSelect(packmodel.Kinds(), func(s string) {
		f.Kind = s
		w.revalidate()
	})
	kind.SetSelected(f.Kind)

	mode := widget.NewEntry()
	mode.SetText(f.Mode)
	mode.OnChanged = func(s string) { f.Mode = s; w.revalidate() }
	// The placeholder shows the default for the chosen kind, so leaving it empty
	// is an informed choice rather than a guess.
	mode.SetPlaceHolder(packmodel.DefaultMode(f.Kind) + " (default for " + orDash(f.Kind) + ")")

	remove := widget.NewButton("Remove", func() {
		fs := w.cfg.Targets[pi].Files
		w.cfg.Targets[pi].Files = append(fs[:fi], fs[fi+1:]...)
		w.rebuildTargets()
		w.revalidate()
	})

	return container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Source"), browse, src),
		w.problemLabel(at+".src"),
		container.NewBorder(nil, nil, widget.NewLabel("Destination"), nil, dst),
		w.problemLabel(at+".dst"),
		container.NewGridWithColumns(3,
			container.NewBorder(nil, nil, widget.NewLabel("Kind"), nil, kind),
			container.NewBorder(nil, nil, widget.NewLabel("Mode"), nil, mode),
			remove,
		),
		w.problemLabel(at+".kind"),
		w.problemLabel(at+".mode"),
	)
}

// problemLabel shows this field's complaint, or nothing at all when there is
// none. The targets editor is rebuilt on every change, so these are read once
// rather than registered for later updates.
func (w *Wizard) problemLabel(field string) fyne.CanvasObject {
	msg, bad := w.problems[field]
	label := widget.NewLabel(msg)
	label.Wrapping = fyne.TextWrapWord
	label.Importance = widget.DangerImportance
	if !bad {
		label.Hide()
	}
	return label
}

func orDash(s string) string {
	if s == "" {
		return "no kind yet"
	}
	return s
}

// buildPreview is the third step: exactly the bytes that will be published.
func (w *Wizard) buildPreview() fyne.CanvasObject {
	w.preview = widget.NewMultiLineEntry()
	w.preview.TextStyle = fyne.TextStyle{Monospace: true}
	// Read-only on purpose. Editing here would put the text and the model out of
	// step, and the point of this pane is that it shows what the form holds.
	w.preview.Disable()

	note := widget.NewLabel("This is the pack.yaml that will be written and published. " +
		"It is generated from the form, so it is what the packer will read.")
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance

	save := widget.NewButton("Save as pack.yaml...", w.onSave)

	w.refreshPreview()
	return container.NewBorder(
		container.NewVBox(note, save, widget.NewSeparator()), nil, nil, nil,
		container.NewVScroll(w.preview),
	)
}

func (w *Wizard) refreshPreview() {
	if w.preview == nil {
		return
	}
	raw, err := packmodel.Marshal(w.cfg)
	if err != nil {
		w.preview.SetText("# this configuration cannot be written as YAML:\n# " + err.Error())
		return
	}
	w.preview.SetText(string(raw))
}

func (w *Wizard) onSave() {
	raw, err := packmodel.Marshal(w.cfg)
	if err != nil {
		w.fail("The pack.yaml could not be generated", err)
		return
	}
	if w.win == nil {
		return
	}

	dialog.ShowFileSave(func(wc fyne.URIWriteCloser, err error) {
		if err != nil {
			w.fail("The file could not be opened for writing", err)
			return
		}
		if wc == nil {
			return
		}
		defer func() { _ = wc.Close() }()

		if _, err := wc.Write(raw); err != nil {
			w.fail("The file could not be written", err)
			return
		}
		w.opts.ConfigPath = wc.URI().Path()
		if w.publishUI != nil {
			w.publishUI.setConfigPath(w.opts.ConfigPath)
		}
	}, w.win)
}

func (w *Wizard) fail(title string, err error) {
	if w.win == nil {
		return
	}
	dialog.ShowError(fmt.Errorf("%s: %w", title, err), w.win)
}
