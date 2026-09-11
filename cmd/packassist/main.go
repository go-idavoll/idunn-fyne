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

// Command packassist writes a pack.yaml and runs the idunn packer over it.
//
// It is an assistant, not a publisher. It signs nothing, creates no key and
// reads none: the role keys are referenced by the TUF_* environment variables
// the packer already reads, and the packer opens them, in its own process.
// root.json is not this program's business at all — a root ceremony creates it,
// offline (idunn AGENTS.md §5).
//
// Usage:
//
//	packassist [--config pack.yaml] [--repo ./tuf-repo] [--packer-bin PATH]
//	           [--allow-go-run]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
	"github.com/go-idavoll/idunn-fyne/internal/packrun"
	"github.com/go-idavoll/idunn-fyne/internal/packui"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("packassist", flag.ContinueOnError)
	fs.SetOutput(stderr)

	config := fs.String("config", "", "pack.yaml to open, and to publish from")
	repo := fs.String("repo", "", "TUF repository directory to publish into")
	packerBin := fs.String("packer-bin", "",
		"path to the idunn packer binary; this is the documented way to publish")
	allowGoRun := fs.Bool("allow-go-run", false,
		"if no packer binary is found, build one with `go run` from the idunn version "+
			"this assistant is pinned to. Off by default: fetching and running code is "+
			"not something a publishing tool should do without being asked.")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "idunn packassist: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}

	cfg, err := loadConfig(*config)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "idunn packassist: %v\n", err)
		return exitError
	}

	epoch, _ := os.LookupEnv("SOURCE_DATE_EPOCH")

	a := app.New()
	win := a.NewWindow("idunn — packing assistant")

	wizard := packui.New(win, packui.Options{
		Config: cfg,
		Runner: &packrun.Runner{
			Bin:        *packerBin,
			AllowGoRun: *allowGoRun,
		},
		ConfigPath:      *config,
		RepoDir:         *repo,
		SourceDateEpoch: epoch,
	})

	win.SetContent(wizard.Content())
	win.Resize(fyne.NewSize(860, 820))
	win.ShowAndRun()

	_, _ = fmt.Fprintln(stdout, "packing assistant closed")
	return exitOK
}

// loadConfig opens an existing pack.yaml, or starts a new one for this machine.
//
// An existing file is read with the packer's own strict rules, so a file this
// assistant will not open is one the packer would not have read either.
func loadConfig(path string) (*packmodel.Config, error) {
	if path == "" {
		return packmodel.New(runtime.GOOS, runtime.GOARCH), nil
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own argument
	if os.IsNotExist(err) {
		// Naming a file that is not there yet is how a publisher starts a new
		// release, so it is not an error -- but the path is remembered.
		return packmodel.New(runtime.GOOS, runtime.GOARCH), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg, err := packmodel.Unmarshal(raw)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return cfg, nil
}
