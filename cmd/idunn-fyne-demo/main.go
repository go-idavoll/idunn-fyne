// Copyright 2026 The idunn Authors
//
// Licensed under the MIT License. See LICENSE for details.

// Command idunn-fyne-demo replays a synthetic update into the progress window.
//
// It exists because the thing this repository has to get right is what a person
// sees, and that is the one property a test cannot assert. It installs nothing,
// touches no install root and reaches no network: the events are made up here,
// in the shape core/updater emits them, so the window can be looked at without
// a signed repository to hand.
package main

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	fyneui "github.com/go-idavoll/idunn-fyne"
	"github.com/go-idavoll/idunn/core/hook"
)

// The release being pretended to: a browser runtime that mostly does not
// change, a library that changed a little, and the application itself.
var files = []struct {
	dst    string
	size   int64
	source hook.Source
}{
	{"lib/libcef.so", 820 << 20, hook.SourceReuse},
	{"lib/libacme.so", 96 << 20, hook.SourcePatch},
	{"bin/acme", 48 << 20, hook.SourceDownload},
	{"share/icons/acme.png", 512 << 10, hook.SourceDownload},
}

func main() {
	a := app.New()
	ui := fyneui.New(a, "Acme — demo")
	ui.Window().Resize(fyne.NewSize(480, 170))

	go replay(ui)
	ui.Window().ShowAndRun()
}

// replay emits the events one update produces, at a pace a person can watch.
func replay(ui *fyneui.ProgressWindow) {
	var total int64
	for _, f := range files {
		total += f.size
	}

	ui.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "checking for updates", Progress: -1})
	time.Sleep(700 * time.Millisecond)

	if ok, err := ui.Confirm(context.Background(), "Install Acme 1.3.0 now?"); err != nil || !ok {
		ui.OnEvent(hook.Event{Phase: hook.PhaseCheck, Message: "the update was declined", Progress: -1})
		return
	}

	var done int64
	for i, f := range files {
		// A reused file moves at disk speed, a download at link speed; the
		// chunk sizes below are what make the window show the difference.
		chunk := int64(24 << 20)
		pause := 40 * time.Millisecond
		if f.source == hook.SourceDownload {
			chunk, pause = 4<<20, 90*time.Millisecond
		}
		for written := int64(0); written < f.size; {
			written = min(written+chunk, f.size)
			ui.OnEvent(hook.Event{
				Phase:      hook.PhaseDownload,
				Message:    fmt.Sprintf("staging %s", f.dst),
				Progress:   float64(done+written) / float64(total),
				File:       f.dst,
				FileIndex:  i + 1,
				FileCount:  len(files),
				Source:     f.source,
				BytesDone:  done + written,
				BytesTotal: total,
				FileDone:   written,
				FileSize:   f.size,
			})
			time.Sleep(pause)
		}
		done += f.size
	}

	for _, e := range []hook.Event{
		{Phase: hook.PhaseQuiesce, Message: "waiting for the application to stop writing", Progress: -1},
		{Phase: hook.PhaseMigrate, Message: "migrating state", Progress: -1},
		{Phase: hook.PhaseApply, Message: "installing 1.3.0", Progress: -1},
		{Phase: hook.PhaseCommit, Message: "installed 1.3.0", Progress: -1},
	} {
		ui.OnEvent(e)
		time.Sleep(600 * time.Millisecond)
	}
}
