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
	"log"

	"fyne.io/fyne/v2"

	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn/core/fsx"
	"github.com/go-idavoll/idunn/core/trust"
	"github.com/go-idavoll/idunn/core/updater"
)

// A host wires the sidecar into the two hooks it implements and changes nothing
// else: the same Updater, the same policy, the same trust client. Dropping the
// two lines gives back a headless update.
//
// The window is the host's own — whatever it already calls a.NewWindow on. It is
// a variable here rather than a call to app.New because fyne.io/fyne/v2/app
// links the desktop driver, and this package is kept buildable on a machine with
// no OpenGL and no display.
func Example() {
	ui := fyneui.NewModal(hostWindow)
	defer ui.Close()

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

		Observe: ui, // byte-level progress in the modal
		Prompt:  ui, // "Install Acme 1.3.0 now?"
	})
	if err != nil {
		log.Fatal(err)
	}

	// Never from a widget callback: Confirm blocks, and the dialog it raises
	// needs the goroutine that would be blocked. Run keeps the update off it.
	fyneui.Run(func() error {
		rel, err := u.CheckForUpdate(context.Background())
		if err != nil || rel == nil {
			return err // nothing to install; the modal stays hidden
		}
		return u.Apply(context.Background(), rel)
	}, func(err error) {
		if err != nil {
			x := fyneui.Explain(err)
			log.Printf("%s: %s (benign=%v)", x.Title, x.Detail, x.Benign())
		}
	})
}

// hostWindow stands in for the window the host already has. An example with no
// output comment is compiled and never run, so this is a declaration the
// compiler checks and nothing dereferences.
var hostWindow fyne.Window

// embeddedRootJSON stands in for the trust anchor a real host compiles in, with
// a go:embed directive. It is never downloaded on first use (docs/design.md §4).
//
// The line above deliberately does not begin with the directive: a comment line
// starting "// go:embed" is a malformed compiler directive, and staticcheck
// fails the build on it (SA9009).
var embeddedRootJSON []byte
