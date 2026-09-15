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
	"fyne.io/fyne/v2/widget"
)

// buildRelease is the first step: what is being published, and to which channel.
func (w *Wizard) buildRelease() fyne.CanvasObject {
	w.nameEntry = w.boundEntry("acme-app", func(s string) { w.cfg.Name = s })
	w.versionEntry = w.boundEntry("1.2.0", func(s string) { w.cfg.Version = s })
	w.channelEntry = w.boundEntry("stable", func(s string) { w.cfg.Channel = s })
	w.minFromEntry = w.boundEntry("1.0.0", func(s string) { w.cfg.Requirements.MinFromVersion = s })
	w.minClientEntry = w.boundEntry("1.0.0", func(s string) { w.cfg.Requirements.MinClientVersion = s })

	w.nameEntry.SetText(w.cfg.Name)
	w.versionEntry.SetText(w.cfg.Version)
	w.channelEntry.SetText(w.cfg.Channel)
	w.minFromEntry.SetText(w.cfg.Requirements.MinFromVersion)
	w.minClientEntry.SetText(w.cfg.Requirements.MinClientVersion)

	w.rolloutLabel = widget.NewLabel("")
	w.rolloutSlider = widget.NewSlider(0, 100)
	w.rolloutSlider.Step = 1
	w.rolloutSlider.Value = w.cfg.Rollout * 100
	w.rolloutSlider.OnChanged = func(v float64) {
		// Kept as hundredths so the emitted YAML holds a short decimal rather
		// than a float that reads as noise.
		w.cfg.Rollout = v / 100
		w.setRolloutLabel()
		w.revalidate()
	}
	w.setRolloutLabel()

	form := container.New(newFormLayout(),
		w.field("name", "Name", w.nameEntry),
		w.field("version", "Version", w.versionEntry),
		w.field("channel", "Channel", w.channelEntry),
		w.field("requirements.min_from_version", "Minimum installed version", w.minFromEntry),
		w.field("requirements.min_client_version", "Minimum client version", w.minClientEntry),
	)

	rollout := container.NewVBox(
		widget.NewLabelWithStyle("Rollout", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		w.rolloutLabel,
		w.rolloutSlider,
		w.hintFor("rollout"),
	)

	return container.NewVScroll(container.NewVBox(form, widget.NewSeparator(), rollout))
}

func (w *Wizard) setRolloutLabel() {
	if w.cfg.Rollout <= 0 {
		w.rolloutLabel.SetText("Offered to everyone.")
		return
	}
	w.rolloutLabel.SetText(fmt.Sprintf("Offered to %.0f%% of installations (rollout: %.2f).",
		w.cfg.Rollout*100, w.cfg.Rollout))
}

// boundEntry is an entry that writes straight into the model and revalidates, so
// what the form shows and what would be published cannot drift apart.
func (w *Wizard) boundEntry(placeholder string, set func(string)) *widget.Entry {
	e := widget.NewEntry()
	e.SetPlaceHolder(placeholder)
	e.OnChanged = func(s string) {
		set(s)
		w.revalidate()
	}
	return e
}

// field is a labelled entry with its own hint line, which carries either the
// rule or the complaint about this field.
func (w *Wizard) field(name, label string, entry *widget.Entry) fyne.CanvasObject {
	hint := w.hintFor(name)
	return container.NewVBox(
		widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		entry,
		hint,
	)
}

func (w *Wizard) hintFor(field string) *widget.Label {
	hint := widget.NewLabel(fieldHelp[field])
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.LowImportance
	w.releaseHints[field] = hint
	return hint
}

// formLayout stacks its children and gives each the full width. Fyne's own
// widget.Form puts the hint out of reach, and a field whose rule is invisible
// until it is broken is a field people get wrong twice.
type formLayout struct{}

func newFormLayout() fyne.Layout { return formLayout{} }

func (formLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var size fyne.Size
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		size.Height += m.Height
		if m.Width > size.Width {
			size.Width = m.Width
		}
	}
	return size
}

func (formLayout) Layout(objects []fyne.CanvasObject, containerSize fyne.Size) {
	y := float32(0)
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		h := o.MinSize().Height
		o.Resize(fyne.NewSize(containerSize.Width, h))
		o.Move(fyne.NewPos(0, y))
		y += h
	}
}
