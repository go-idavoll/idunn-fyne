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

// Command demo shows the idunn Fyne sidecar driving a real update.
//
// By default it installs into a throwaway directory from an in-memory fixture:
// no TUF repository, no signing keys, no network. Everything below the resolver
// is genuine — staging, the transaction journal, the atomic swap, garbage
// collection — because updater.Resolver is an exported interface and the fixture
// stands exactly where the trust client stands.
//
// With the --tuf-* flags it resolves against a real repository instead. Not one
// line of the screen changes between the two, which is the point being made.
//
// Usage:
//
//	demo [--root DIR] [--channel NAME] [--busy] [--keep-root]
//	demo --tuf-root FILE --tuf-metadata-url URL --tuf-targets-url URL [...]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn-fyne/internal/demoui"
	"github.com/go-idavoll/idunn-fyne/internal/fixture"
	"github.com/go-idavoll/idunn/core/fetch"
	"github.com/go-idavoll/idunn/core/release"
	"github.com/go-idavoll/idunn/core/trust"
	"github.com/go-idavoll/idunn/core/updater"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const clientVersion = "1.0.0"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type config struct {
	root     string
	channel  string
	busy     bool
	keepRoot bool

	tufRoot     string
	metadataURL string
	targetsURL  string
	localDir    string
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c config
	fs.StringVar(&c.root, "root", "", "installation root (default: a fresh temporary directory)")
	fs.StringVar(&c.channel, "channel", fixture.Channel, "update channel to follow")
	fs.BoolVar(&c.busy, "busy", false,
		"pretend an instance of the application is running, so the update defers to the next start")
	fs.BoolVar(&c.keepRoot, "keep-root", false, "do not remove a temporary root on exit")
	fs.StringVar(&c.tufRoot, "tuf-root", "", "path to the trust anchor root.json; selects the real-repository mode")
	fs.StringVar(&c.metadataURL, "tuf-metadata-url", "", "TUF metadata base URL")
	fs.StringVar(&c.targetsURL, "tuf-targets-url", "", "TUF targets base URL")
	fs.StringVar(&c.localDir, "tuf-local-dir", "", "local TUF metadata and target cache (default: a temporary directory)")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "idunn demo: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}

	root, cleanup, err := installRoot(c, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "idunn demo: %v\n", err)
		return exitError
	}
	defer cleanup()

	res, advance, err := resolver(c, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "idunn demo: %v\n", err)
		return exitError
	}

	a := app.New()
	win := a.NewWindow("idunn — update demo")

	panel := fyneui.New(win)
	defer panel.Close()

	now := time.Now
	screen := demoui.New(panel, demoui.Options{
		Root:          root,
		Channel:       c.channel,
		Resolver:      res,
		ClientVersion: clientVersion,
		// A build time in the past: it is the first floor under the system
		// clock, and a floor in the future would refuse every run.
		BuildTime:      now().Add(-24 * time.Hour),
		Now:            now,
		Busy:           c.busy,
		QuiesceTimeout: 2 * time.Second,
		AfterInstall:   advance,
	})

	win.SetContent(screen.Content())
	win.Resize(fyne.NewSize(760, 720))

	// From the Fyne goroutine, with the window built: from here the panel draws.
	panel.Start()
	win.ShowAndRun()
	return exitOK
}

func installRoot(c config, stdout io.Writer) (string, func(), error) {
	if c.root != "" {
		//nolint:gosec // G301: an installation root is readable by the users who
		// run the application; 0750 would be the wrong shape for an install tree.
		if err := os.MkdirAll(c.root, 0o755); err != nil {
			return "", func() {}, fmt.Errorf("creating the installation root: %w", err)
		}
		return c.root, func() {}, nil
	}

	dir, err := os.MkdirTemp("", "idunn-demo-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating a temporary installation root: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "installing into %s\n", dir)

	if c.keepRoot {
		return dir, func() { _, _ = fmt.Fprintf(stdout, "kept %s\n", dir) }, nil
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// resolver picks between the fixture and a real trust client. Both satisfy
// updater.Resolver, so the screen never learns which one it got.
// The second return value advances the fixture channel once a version is
// installed. It is nil in the real-repository mode, where the channel moves
// because a publisher moved it.
func resolver(c config, stdout io.Writer) (updater.Resolver, func(string), error) {
	if c.tufRoot == "" {
		if c.metadataURL != "" || c.targetsURL != "" {
			return nil, nil, fmt.Errorf("--tuf-metadata-url and --tuf-targets-url need --tuf-root, " +
				"the trust anchor; without it there is nothing to verify against")
		}
		res, advance := fixtureResolver(c.channel, stdout)
		return res, advance, nil
	}

	rootJSON, err := os.ReadFile(c.tufRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the trust anchor: %w", err)
	}
	if c.metadataURL == "" {
		return nil, nil, fmt.Errorf("--tuf-root needs --tuf-metadata-url")
	}

	local := c.localDir
	if local == "" {
		local, err = os.MkdirTemp("", "idunn-demo-tuf-*")
		if err != nil {
			return nil, nil, fmt.Errorf("creating the TUF cache: %w", err)
		}
	}

	fetcher, err := fetch.New(fetch.Options{UserAgent: "idunn-fyne-demo/" + clientVersion})
	if err != nil {
		return nil, nil, fmt.Errorf("building the transport: %w", err)
	}
	client, err := trust.New(trust.Options{
		Root:        rootJSON,
		MetadataURL: c.metadataURL,
		TargetsURL:  c.targetsURL,
		LocalDir:    local,
		Fetcher:     fetcher,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("building the trust client: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "resolving against %s (cache %s)\n", c.metadataURL, filepath.Clean(local))
	return client, nil, nil
}

// fixtureResolver offers 1.0.0, and advances the channel to 1.1.0 once 1.0.0 is
// installed.
//
// Publishing both up front makes for a weaker demonstration: the first install
// would take 1.1.0 and there would never be an upgrade to watch. Staging them
// means the second run has something to swap away from, a FromVersion to show,
// and an old version directory to collect -- which is the interesting half of
// what idunn does.
func fixtureResolver(channel string, stdout io.Writer) (*fixture.Resolver, func(string)) {
	r := fixture.NewResolver()

	first, blobs := fixture.Build("1.0.0", runtime.GOOS, runtime.GOARCH, release.Requirements{})
	first.Channel = channel
	r.Publish(first, blobs)

	_, _ = fmt.Fprintf(stdout, "fixture mode: channel %q offers 1.0.0; 1.1.0 follows once it is installed\n",
		channel)

	return r, func(installed string) {
		if installed != "1.0.0" {
			return
		}
		next, blobs := fixture.Build("1.1.0", runtime.GOOS, runtime.GOARCH,
			release.Requirements{MinFromVersion: "1.0.0", MinClientVersion: clientVersion})
		next.Channel = channel
		r.Publish(next, blobs)
	}
}
