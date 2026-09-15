// Copyright 2026 The idunn Authors
//
// Licensed under the MIT License. See LICENSE for details.

package fyneui_test

import (
	"context"
	"log"

	"fyne.io/fyne/v2/app"

	fyneui "github.com/go-idavoll/idunn-fyne"
	"github.com/go-idavoll/idunn/core/fsx"
	"github.com/go-idavoll/idunn/core/trust"
	"github.com/go-idavoll/idunn/core/updater"
)

// A host wires the sidecar into the two hooks it implements and changes nothing
// else: the same Updater, the same policy, the same trust client. Dropping the
// two lines gives back a headless update.
func Example() {
	a := app.New()
	ui := fyneui.New(a, "Acme")

	client, err := trust.New(trust.Options{
		Root:        embeddedRootJSON,
		MetadataURL: "https://updates.example.com/metadata/",
		TargetsURL:  "https://updates.example.com/targets/",
		LocalDir:    "/var/lib/acme/tuf",
	})
	if err != nil {
		log.Fatal(err)
	}

	u, err := updater.New(updater.Options{
		Trust:   client,
		FS:      fsx.OS(),
		Root:    "/opt/acme",
		Channel: "stable",

		Observe: ui, // byte-level progress in the window
		Prompt:  ui, // "Install Acme 1.3.0 now?"
	})
	if err != nil {
		log.Fatal(err)
	}

	// The update runs on its own goroutine; the Fyne main loop belongs to
	// ShowAndRun. The sidecar bridges the two — OnEvent is called from here and
	// repaints over there.
	go func() {
		r, err := u.CheckForUpdate(context.Background())
		if err != nil || r == nil {
			return // nothing to install, or a check that failed; stay hidden
		}
		if err := u.Apply(context.Background(), r); err != nil {
			log.Print(err)
		}
	}()

	ui.Window().ShowAndRun()
}

// embeddedRootJSON stands in for the trust anchor a real host compiles in with
// go:embed. It is never downloaded on first use (docs/design.md §4).
var embeddedRootJSON []byte
