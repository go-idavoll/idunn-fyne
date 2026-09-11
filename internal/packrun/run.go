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

package packrun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultExpiry values mirror internal/packer's, so the form starts where the
// packer would have started anyway.
const (
	DefaultTargetsExpiry   = 90 * 24 * time.Hour
	DefaultSnapshotExpiry  = 7 * 24 * time.Hour
	DefaultTimestampExpiry = 24 * time.Hour
)

// Request is one publish.
type Request struct {
	ConfigPath string
	RepoDir    string

	// Now is the reference time every expiry is measured from. The packer has
	// no wall-clock fallback at all: two runs over the same inputs must produce
	// a byte-identical repository, and output that embeds when it ran cannot be
	// rebuilt and compared. A zero Now omits --now, which only works when
	// SOURCE_DATE_EPOCH is set in the environment instead.
	Now time.Time

	TargetsExpiry   time.Duration
	SnapshotExpiry  time.Duration
	TimestampExpiry time.Duration
}

// Result is what a publish did.
type Result struct {
	// Argv is the command as it was run, for the log. It never contains an
	// environment value: the key references are passed to the child, not
	// written down here.
	Argv []string

	ExitCode int
	Stdout   string
	Stderr   string
	Report   Report
}

// ExecFunc runs argv with env and returns the process exit code. It is the seam
// that lets this package be tested without a packer binary.
type ExecFunc func(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error)

// Runner publishes by running the idunn packer.
type Runner struct {
	// Bin is an explicit path to the packer. This is the documented production
	// path: a pinned, reviewed binary. Empty falls back to PATH.
	Bin string

	// AllowGoRun permits `go run github.com/go-idavoll/idunn/cmd/packer` when no
	// binary is found. It is opt-in and never the default: AGENTS.md §1.3 is
	// hostile to mechanisms that fetch and run code, and "it is only a
	// maintainer tool" is not a reason to make one the default.
	AllowGoRun bool

	// ModuleDir is where `go run` resolves the packer version from. Pointed at
	// this module, it resolves the same pinned, checksum-verified version this
	// program was built against, rather than whatever is newest.
	ModuleDir string

	// Exec is the process seam; nil selects the real one.
	Exec ExecFunc

	// Environ supplies the base environment; nil selects os.Environ.
	Environ func() []string
}

// Publish runs one publish and returns what the packer said.
//
// The key environment is checked first, before anything is spawned. An error
// here means nothing was attempted; an error with a non-nil Result means the
// packer ran and refused.
func (r *Runner) Publish(ctx context.Context, req Request, keys []KeyStatus) (*Result, error) {
	if err := CheckKeyEnv(keys); err != nil {
		return nil, err
	}
	argv, err := r.argv(req)
	if err != nil {
		return nil, err
	}

	environ := os.Environ
	if r.Environ != nil {
		environ = r.Environ
	}
	run := r.Exec
	if run == nil {
		run = execCommand
	}

	var stdout, stderr bytes.Buffer
	code, err := run(ctx, argv, environ(), &stdout, &stderr)

	res := &Result{
		Argv:     argv,
		ExitCode: code,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Report:   ParseReport(stdout.String()),
	}
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrPublish, err)
	}

	// The packer documents its exit codes as a contract: 0 published, 1 refused
	// or failed, 2 the command line was wrong.
	switch code {
	case 0:
		return res, nil
	case 2:
		return res, fmt.Errorf("%w: %s", ErrUsage, firstLine(res.Stderr))
	default:
		return res, fmt.Errorf("%w: %s", ErrPublish, firstLine(res.Stderr))
	}
}

// Command renders the command line a publish would run, without running it. The
// assistant shows it so an operator can see exactly what is about to happen, and
// can reproduce it on a terminal.
func (r *Runner) Command(req Request) ([]string, error) { return r.argv(req) }

func (r *Runner) argv(req Request) ([]string, error) {
	if req.ConfigPath == "" {
		return nil, fmt.Errorf("%w: no pack.yaml to publish", ErrPublish)
	}
	if req.RepoDir == "" {
		return nil, fmt.Errorf("%w: no repository directory; the packer requires --repo", ErrPublish)
	}

	head, err := r.locate()
	if err != nil {
		return nil, err
	}

	argv := append(head, "publish", "--config", req.ConfigPath, "--repo", req.RepoDir)
	if !req.Now.IsZero() {
		argv = append(argv, "--now", req.Now.UTC().Truncate(time.Second).Format(time.RFC3339))
	}
	argv = append(argv,
		"--targets-expiry", orDefault(req.TargetsExpiry, DefaultTargetsExpiry).String(),
		"--snapshot-expiry", orDefault(req.SnapshotExpiry, DefaultSnapshotExpiry).String(),
		"--timestamp-expiry", orDefault(req.TimestampExpiry, DefaultTimestampExpiry).String(),
	)
	return argv, nil
}

// locate decides which packer to run. The resolved path goes into Result.Argv,
// so the log says which binary actually ran rather than which one was asked for.
func (r *Runner) locate() ([]string, error) {
	if r.Bin != "" {
		info, err := os.Stat(r.Bin)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrNotFound, r.Bin, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%w: %s is a directory", ErrNotFound, r.Bin)
		}
		return []string{r.Bin}, nil
	}

	if path, err := exec.LookPath("packer"); err == nil {
		return []string{path}, nil
	}

	if r.AllowGoRun {
		// No @version: run inside this module, the packer resolves to the same
		// pinned, checksum-verified idunn version this program was built
		// against, rather than to whatever is newest.
		return []string{"go", "run", "github.com/go-idavoll/idunn/cmd/packer"}, nil
	}

	return nil, fmt.Errorf("%w: set --packer-bin to a packer binary, put one named "+
		"\"packer\" on PATH, or pass --allow-go-run to build it from the pinned "+
		"idunn version", ErrNotFound)
}

func execCommand(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, fmt.Errorf("%w: empty command", ErrPublish)
	}

	//nolint:gosec // G204: running the packer is the whole purpose of this
	// package, and the command is built here rather than taken from input.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exitErr); ok {
			// The packer ran and said no. That is an answer, not a failure to
			// run, so it is reported through the exit code rather than as err.
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// The packer prefixes its errors; the banner is repeated by the caller's UI.
	return strings.TrimPrefix(s, "idunn packer: ")
}
