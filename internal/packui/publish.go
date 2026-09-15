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
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn-fyne/internal/packrun"
)

// publishUI is the last step: where to publish, with which keys, at which
// reference time -- and then the log of what happened.
type publishUI struct {
	w *Wizard

	repoEntry   *widget.Entry
	configEntry *widget.Entry
	nowEntry    *widget.Entry

	keysBox  *fyne.Container
	command  *widget.Label
	timeWarn *widget.Label

	publish *widget.Button
	verify  *widget.Button
	log     *widget.Entry
	status  *widget.Label

	blocked bool
	running bool
	// published records that this repository has been published to in this
	// session, which is what makes verification worth offering.
	published bool
}

func (w *Wizard) buildPublish() fyne.CanvasObject {
	p := &publishUI{w: w}
	w.publishUI = p

	p.repoEntry = widget.NewEntry()
	p.repoEntry.SetPlaceHolder("./tuf-repo")
	p.repoEntry.SetText(w.opts.RepoDir)
	p.repoEntry.OnChanged = func(s string) { w.opts.RepoDir = s; p.refresh() }

	p.configEntry = widget.NewEntry()
	p.configEntry.SetPlaceHolder("pack.yaml")
	p.configEntry.SetText(w.opts.ConfigPath)
	p.configEntry.OnChanged = func(s string) { w.opts.ConfigPath = s; p.refresh() }

	p.nowEntry = widget.NewEntry()
	p.nowEntry.SetPlaceHolder(time.RFC3339)
	p.nowEntry.OnChanged = func(string) { p.refresh() }

	useEpoch := widget.NewButton("Use SOURCE_DATE_EPOCH", func() {
		t, err := epochTime(w.opts.SourceDateEpoch)
		if err != nil {
			p.setStatus("SOURCE_DATE_EPOCH is not set to a Unix timestamp.", widget.WarningImportance)
			return
		}
		p.nowEntry.SetText(t.Format(time.RFC3339))
	})
	useNow := widget.NewButton("Use the current time", func() {
		p.nowEntry.SetText(time.Now().UTC().Truncate(time.Second).Format(time.RFC3339))
	})

	nowNote := widget.NewLabel("The packer has no wall-clock fallback: without a reference " +
		"time it refuses to publish, because output that embeds when it ran cannot be " +
		"rebuilt and compared. Leave this empty only if SOURCE_DATE_EPOCH is set in the " +
		"environment the packer will run in.")
	nowNote.Wrapping = fyne.TextWrapWord
	nowNote.Importance = widget.LowImportance

	p.timeWarn = widget.NewLabel("")
	p.timeWarn.Wrapping = fyne.TextWrapWord
	p.timeWarn.Importance = widget.WarningImportance
	p.timeWarn.Hide()

	p.keysBox = container.NewVBox()
	keysNote := widget.NewLabel("Signing keys are read by the packer, from the paths these " +
		"variables name. This assistant never opens them, never stores them and cannot " +
		"create them — a root ceremony does that, offline.")
	keysNote.Wrapping = fyne.TextWrapWord
	keysNote.Importance = widget.LowImportance

	p.command = widget.NewLabel("")
	p.command.Wrapping = fyne.TextWrapBreak
	p.command.TextStyle = fyne.TextStyle{Monospace: true}

	p.status = widget.NewLabel("")
	p.status.Wrapping = fyne.TextWrapWord

	p.log = widget.NewMultiLineEntry()
	p.log.TextStyle = fyne.TextStyle{Monospace: true}
	p.log.Disable()

	p.publish = widget.NewButton("Publish", p.onPublish)
	p.publish.Importance = widget.HighImportance
	p.verify = widget.NewButton("Resolve it as a client would", p.onVerify)
	p.verify.Disable()

	head := container.NewVBox(
		widget.NewLabelWithStyle("Repository", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.repoEntry,
		widget.NewLabelWithStyle("pack.yaml", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.configEntry,
		widget.NewLabelWithStyle("Reference time", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.nowEntry,
		container.NewHBox(useEpoch, useNow),
		nowNote,
		p.timeWarn,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Signing keys", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		keysNote,
		p.keysBox,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Command", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.command,
		container.NewHBox(p.publish, p.verify),
		p.status,
		widget.NewSeparator(),
	)

	p.refresh()
	return container.NewBorder(head, nil, nil, nil, container.NewVScroll(p.log))
}

// refresh re-reads the key environment and re-renders the command line, so what
// the operator sees is what would run right now.
func (p *publishUI) refresh() {
	statuses := p.keyStatuses()

	p.keysBox.RemoveAll()
	for _, k := range statuses {
		p.keysBox.Add(keyRow(k))
	}
	p.keysBox.Refresh()

	p.renderCommand()
	p.checkReferenceTime()
	p.setBlocked(p.blocked)
}

// checkReferenceTime warns about a repository that would be published already
// expired.
//
// The packer takes any reference time and is right to: a reproducible rebuild
// of an old release has to be able to reproduce its timestamps. But an operator
// publishing today from a month-old SOURCE_DATE_EPOCH gets a timestamp.json
// that is past its 24-hour expiry the moment it is written, and every client
// refuses the repository -- correctly, and confusingly. Saying so here is much
// cheaper than discovering it from a fleet that has stopped updating.
func (p *publishUI) checkReferenceTime() {
	if p.timeWarn == nil {
		return
	}
	ref, ok := p.referenceTime()
	if !ok {
		p.timeWarn.Hide()
		return
	}

	expiry := orDefault(p.requestExpiry(), packrun.DefaultTimestampExpiry)
	if p.w.opts.Now().Before(ref.Add(expiry)) {
		p.timeWarn.Hide()
		return
	}

	p.timeWarn.SetText(fmt.Sprintf(
		"This reference time is %s ago, and the timestamp role is valid for %s from it. "+
			"The repository would be published already expired, and clients would refuse "+
			"it. That is what you want for reproducing an old release, and not what you "+
			"want for publishing a new one.",
		humanAge(p.w.opts.Now().Sub(ref)), humanAge(expiry)))
	p.timeWarn.Show()
	p.timeWarn.Refresh()
}

// referenceTime is the time the publish would actually use: the field if it
// holds one, otherwise SOURCE_DATE_EPOCH, which is where the packer looks next.
func (p *publishUI) referenceTime() (time.Time, bool) {
	if raw := strings.TrimSpace(p.nowEntry.Text); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		return t, err == nil
	}
	t, err := epochTime(p.w.opts.SourceDateEpoch)
	return t, err == nil
}

// requestExpiry is the timestamp expiry a publish would use. The form does not
// offer it yet, so it is always the default -- but reading it through one
// function keeps the warning honest if it ever does.
func (p *publishUI) requestExpiry() time.Duration { return 0 }

// humanAge renders how long ago something was. Go prints a ten-day duration as
// "261h0m0s", which is exact and unreadable.
func humanAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours())/24)
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return d.Round(time.Minute).String()
	}
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}

func (p *publishUI) keyStatuses() []packrun.KeyStatus {
	var delegations []string
	if c := p.w.cfg; c != nil {
		delegations = append(delegations, c.Channel)
		if major := majorOf(c.Version); major != "" {
			delegations = append(delegations, "v"+major)
		}
	}
	return packrun.InspectKeyEnv(p.w.opts.LookupEnv, nil, delegations)
}

// keyRow renders one variable. It shows the path, because an operator has to be
// able to see which key is about to sign — and never anything read from it.
func keyRow(k packrun.KeyStatus) fyne.CanvasObject {
	name := widget.NewLabel(k.Name)
	name.TextStyle = fyne.TextStyle{Monospace: true}

	detail := widget.NewLabel("")
	detail.Wrapping = fyne.TextWrapBreak

	switch {
	case k.Problem() != "":
		detail.SetText(k.Problem())
		detail.Importance = widget.DangerImportance
	case !k.Set:
		detail.SetText("not set; this delegation falls back to the targets key")
		detail.Importance = widget.LowImportance
	case k.Loose:
		detail.SetText(k.Ref + " — readable by other users on this machine")
		detail.Importance = widget.WarningImportance
	default:
		detail.SetText(k.Ref)
		detail.Importance = widget.SuccessImportance
	}
	return container.NewBorder(nil, nil, name, nil, detail)
}

func (p *publishUI) renderCommand() {
	if p.w.opts.Runner == nil {
		p.command.SetText("No packer configured; this assistant can validate and save a " +
			"pack.yaml but not publish. Pass --packer-bin to enable publishing.")
		return
	}
	req, err := p.request()
	if err != nil {
		p.command.SetText(err.Error())
		return
	}
	argv, err := p.w.opts.Runner.Command(req)
	if err != nil {
		p.command.SetText(err.Error())
		return
	}
	// The environment is described, never printed: the key variables carry
	// paths, and a path in a screenshot is one more thing an operator has to
	// think about before sharing it.
	p.command.SetText(strings.Join(argv, " ") + "\n(plus the role-key variables above)")
}

func (p *publishUI) request() (packrun.Request, error) {
	req := packrun.Request{
		ConfigPath: p.w.opts.ConfigPath,
		RepoDir:    p.w.opts.RepoDir,
	}
	if raw := strings.TrimSpace(p.nowEntry.Text); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return req, fmt.Errorf("the reference time is not RFC3339: %w", err)
		}
		req.Now = t
	}
	return req, nil
}

func (p *publishUI) setConfigPath(path string) {
	p.configEntry.SetText(path)
}

// setBlocked disables publishing while the form still has problems. The packer
// would refuse anyway; refusing here means an operator learns it while they can
// still see which field is wrong.
func (p *publishUI) setBlocked(blocked bool) {
	p.blocked = blocked
	switch {
	case p.running, blocked, p.w.opts.Runner == nil:
		p.publish.Disable()
	default:
		p.publish.Enable()
	}
	if p.published && !p.running {
		p.verify.Enable()
	} else {
		p.verify.Disable()
	}
}

func (p *publishUI) onPublish() {
	req, err := p.request()
	if err != nil {
		p.setStatus(err.Error(), widget.DangerImportance)
		return
	}
	keys := p.keyStatuses()
	if err := packrun.CheckKeyEnv(keys); err != nil {
		p.setStatus(err.Error(), widget.DangerImportance)
		return
	}

	p.running = true
	p.setBlocked(p.blocked)
	p.setStatus("Publishing...", widget.MediumImportance)

	var res *packrun.Result
	p.w.opts.Run(func() error {
		var err error
		res, err = p.w.opts.Runner.Publish(context.Background(), req, keys)
		return err
	}, func(err error) {
		p.running = false
		if res != nil {
			p.appendLog(res)
		}
		switch {
		case err != nil:
			p.setStatus(err.Error(), widget.DangerImportance)
		default:
			p.published = true
			p.setStatus(res.Report.Summary(), widget.SuccessImportance)
		}
		p.setBlocked(p.blocked)
	})
}

func (p *publishUI) onVerify() {
	repo := p.w.opts.RepoDir
	cfg := p.w.cfg

	p.running = true
	p.setBlocked(p.blocked)
	p.setStatus("Resolving the repository the way an installation would...", widget.MediumImportance)

	var out *packrun.VerifyResult
	p.w.opts.Run(func() error {
		var err error
		out, err = packrun.Verify(context.Background(), packrun.VerifyRequest{
			RepoDir: repo,
			Channel: cfg.Channel,
			OS:      cfg.Targets[0].OS,
			Arch:    cfg.Targets[0].Arch,
		})
		return err
	}, func(err error) {
		p.running = false
		p.setBlocked(p.blocked)
		if err != nil {
			p.appendLine("verify: " + err.Error())
			p.setStatus(err.Error(), widget.DangerImportance)
			return
		}

		var b strings.Builder
		fmt.Fprintf(&b, "verify: resolved %s %s on channel %s for %s-%s\n",
			out.Descriptor.Name, out.Descriptor.Version, out.Descriptor.Channel,
			out.Descriptor.OS, out.Descriptor.Arch)
		for dst, n := range out.Bytes {
			fmt.Fprintf(&b, "verify:   %s (%d bytes verified)\n", dst, n)
		}
		if out.SelfAnchored {
			b.WriteString("verify: " + packrun.SelfAnchoredWarning + "\n")
		}
		p.appendLine(b.String())

		msg := "Resolved end to end with the same client an installation runs."
		if out.SelfAnchored {
			msg += " " + packrun.SelfAnchoredWarning
		}
		p.setStatus(msg, widget.SuccessImportance)
	})
}

func (p *publishUI) appendLog(res *packrun.Result) {
	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\n", strings.Join(res.Argv, " "))
	if res.Stdout != "" {
		b.WriteString(res.Stdout)
	}
	if res.Stderr != "" {
		b.WriteString(res.Stderr)
	}
	fmt.Fprintf(&b, "exit %d\n", res.ExitCode)
	p.appendLine(b.String())
}

func (p *publishUI) appendLine(s string) {
	p.log.SetText(p.log.Text + s)
}

func (p *publishUI) setStatus(s string, importance widget.Importance) {
	p.status.SetText(s)
	p.status.Importance = importance
	p.status.Refresh()
}

func majorOf(version string) string {
	if i := strings.IndexByte(version, '.'); i > 0 {
		return version[:i]
	}
	return ""
}

func epochTime(raw string) (time.Time, error) {
	secs, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(secs, 0).UTC(), nil
}
