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

package packrun_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-idavoll/idunn-fyne/internal/packrun"
)

// recordExec is an ExecFunc that remembers what it was asked to run.
type recordExec struct {
	called bool
	argv   []string
	env    []string

	code   int
	stdout string
	stderr string
	err    error
}

func (r *recordExec) run(_ context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	r.called = true
	r.argv = argv
	r.env = env
	_, _ = io.WriteString(stdout, r.stdout)
	_, _ = io.WriteString(stderr, r.stderr)
	return r.code, r.err
}

// keysOK is a set of role-key statuses that pass the pre-flight.
func keysOK() []packrun.KeyStatus {
	var out []packrun.KeyStatus
	for _, n := range packrun.RequiredKeys() {
		out = append(out, packrun.KeyStatus{Name: n, Required: true, Set: true,
			Ref: "/keys/" + n + ".pem", Exists: true})
	}
	return out
}

func fakeBin(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "packer")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

const sampleReport = `published demo 1.2.0 on channel stable
  role snapshot     -> version 2
  role stable       -> version 2
  role targets      -> version 2
  role timestamp    -> version 2
  role v1           -> version 2
  delegation stable holds 1 targets
  delegation v1     holds 3 targets
  4 new targets
`

// TestPublishBuildsTheExactCommandLine pins the argv. The packer's flags are its
// contract, and a silently wrong one here is a publish that does something other
// than what the form showed.
func TestPublishBuildsTheExactCommandLine(t *testing.T) {
	bin := fakeBin(t)
	ex := &recordExec{stdout: sampleReport}
	r := &packrun.Runner{Bin: bin, Exec: ex.run, Environ: func() []string { return nil }}

	at := time.Date(2026, 9, 1, 12, 30, 45, 999, time.UTC)
	_, err := r.Publish(context.Background(), packrun.Request{
		ConfigPath:      "pack.yaml",
		RepoDir:         "./tuf-repo",
		Now:             at,
		TargetsExpiry:   48 * time.Hour,
		SnapshotExpiry:  2 * time.Hour,
		TimestampExpiry: time.Hour,
	}, keysOK())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	want := []string{
		bin, "publish",
		"--config", "pack.yaml",
		"--repo", "./tuf-repo",
		"--now", "2026-09-01T12:30:45Z", // truncated to the second
		"--targets-expiry", "48h0m0s",
		"--snapshot-expiry", "2h0m0s",
		"--timestamp-expiry", "1h0m0s",
	}
	if strings.Join(ex.argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv =\n  %v\nwant\n  %v", ex.argv, want)
	}
}

// TestPublishOmitsNowWhenZero: without --now the packer falls back to
// SOURCE_DATE_EPOCH, and passing a zero time would pin every expiry to year one.
func TestPublishOmitsNowWhenZero(t *testing.T) {
	ex := &recordExec{stdout: sampleReport}
	r := &packrun.Runner{Bin: fakeBin(t), Exec: ex.run, Environ: func() []string { return nil }}

	if _, err := r.Publish(context.Background(), packrun.Request{
		ConfigPath: "pack.yaml", RepoDir: "repo",
	}, keysOK()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for _, a := range ex.argv {
		if a == "--now" {
			t.Fatalf("argv carries --now for a zero reference time: %v", ex.argv)
		}
	}
}

// TestPublishUsesTheDefaultExpiries keeps the form's starting point identical to
// the packer's own.
func TestPublishUsesTheDefaultExpiries(t *testing.T) {
	ex := &recordExec{stdout: sampleReport}
	r := &packrun.Runner{Bin: fakeBin(t), Exec: ex.run, Environ: func() []string { return nil }}

	if _, err := r.Publish(context.Background(), packrun.Request{
		ConfigPath: "pack.yaml", RepoDir: "repo",
	}, keysOK()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	joined := strings.Join(ex.argv, " ")
	for _, want := range []string{"--targets-expiry 2160h0m0s", "--snapshot-expiry 168h0m0s",
		"--timestamp-expiry 24h0m0s"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %v", want, ex.argv)
		}
	}
}

// TestPublishMapsTheExitCodes. The packer documents 0/1/2 as a contract, and a
// usage error means this program built the command wrongly -- which the
// interface must not blame on the operator.
func TestPublishMapsTheExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		stderr string
		want   error
	}{
		{"published", 0, "", nil},
		{"refused", 1, "idunn packer: packer: key: TUF_TARGETS_KEY is not set\n", packrun.ErrPublish},
		{"bad command line", 2, "idunn packer: --repo is required\n", packrun.ErrUsage},
		{"killed", 137, "", packrun.ErrPublish},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &recordExec{code: tc.code, stderr: tc.stderr, stdout: sampleReport}
			r := &packrun.Runner{Bin: fakeBin(t), Exec: ex.run, Environ: func() []string { return nil }}

			res, err := r.Publish(context.Background(), packrun.Request{
				ConfigPath: "pack.yaml", RepoDir: "repo",
			}, keysOK())
			if res == nil {
				t.Fatal("no Result returned; the log would have nothing to show")
			}
			if res.ExitCode != tc.code {
				t.Errorf("ExitCode = %d, want %d", res.ExitCode, tc.code)
			}
			switch {
			case tc.want == nil && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			// The banner must not repeat the packer's own prefix.
			if err != nil && strings.Contains(err.Error(), "idunn packer: idunn packer:") {
				t.Errorf("the error doubles the packer prefix: %v", err)
			}
		})
	}
}

// TestPublishRefusesBeforeSpawningWhenAKeyIsMissing is the fail-closed rule. The
// packer would refuse anyway; refusing here means nothing was attempted, and the
// operator is told which variable rather than being handed a subprocess error.
func TestPublishRefusesBeforeSpawningWhenAKeyIsMissing(t *testing.T) {
	ex := &recordExec{}
	r := &packrun.Runner{Bin: fakeBin(t), Exec: ex.run, Environ: func() []string { return nil }}

	keys := keysOK()
	keys[0].Set = false
	keys[0].Ref = ""

	res, err := r.Publish(context.Background(), packrun.Request{
		ConfigPath: "pack.yaml", RepoDir: "repo",
	}, keys)
	if !errors.Is(err, packrun.ErrKeyEnv) {
		t.Fatalf("err = %v, want ErrKeyEnv", err)
	}
	if res != nil {
		t.Error("a Result was returned for a publish that never ran")
	}
	if ex.called {
		t.Error("the packer was spawned despite a missing role key")
	}
	if !strings.Contains(err.Error(), packrun.EnvTargetsKey) {
		t.Errorf("the error does not name the variable: %v", err)
	}
}

// TestPublishNeverWritesDownAKeyReference is the key-hygiene test. Result.Argv
// is what the log renders, and a key path in it would end up in screenshots,
// bug reports and pasted terminal output.
func TestPublishNeverWritesDownAKeyReference(t *testing.T) {
	const secretPath = "/very/private/sentinel-targets-key.pem"

	ex := &recordExec{stdout: sampleReport}
	r := &packrun.Runner{
		Bin:     fakeBin(t),
		Exec:    ex.run,
		Environ: func() []string { return []string{packrun.EnvTargetsKey + "=" + secretPath} },
	}
	keys := keysOK()
	keys[0].Ref = secretPath

	res, err := r.Publish(context.Background(), packrun.Request{
		ConfigPath: "pack.yaml", RepoDir: "repo",
	}, keys)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if strings.Contains(strings.Join(res.Argv, " "), secretPath) {
		t.Errorf("Result.Argv carries the key reference: %v", res.Argv)
	}
	if strings.Contains(res.Stdout+res.Stderr, secretPath) {
		t.Error("the captured output carries the key reference")
	}
	// It must still reach the child, or the publish could not sign anything.
	if !strings.Contains(strings.Join(ex.env, " "), secretPath) {
		t.Error("the key reference was not passed to the packer at all")
	}
}

func TestPublishRefusesAnIncompleteRequest(t *testing.T) {
	r := &packrun.Runner{Bin: fakeBin(t), Exec: (&recordExec{}).run}
	for _, tc := range []struct {
		name string
		req  packrun.Request
	}{
		{"no config", packrun.Request{RepoDir: "repo"}},
		{"no repo", packrun.Request{ConfigPath: "pack.yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Publish(context.Background(), tc.req, keysOK()); !errors.Is(err, packrun.ErrPublish) {
				t.Errorf("err = %v, want ErrPublish", err)
			}
		})
	}
}

// TestLocateRefusesRatherThanGuessing. `go run` fetches and builds code, which
// AGENTS.md §1.3 is hostile to, so it must never be what happens by default.
func TestLocateRefusesRatherThanGuessing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing named "packer" anywhere

	r := &packrun.Runner{Exec: (&recordExec{}).run}
	_, err := r.Command(packrun.Request{ConfigPath: "pack.yaml", RepoDir: "repo"})
	if !errors.Is(err, packrun.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "--allow-go-run") {
		t.Errorf("the error does not say how to proceed: %v", err)
	}

	r.AllowGoRun = true
	argv, err := r.Command(packrun.Request{ConfigPath: "pack.yaml", RepoDir: "repo"})
	if err != nil {
		t.Fatalf("Command with --allow-go-run: %v", err)
	}
	if argv[0] != "go" || argv[1] != "run" {
		t.Errorf("argv = %v, want a go run invocation", argv)
	}
	// No @version: the packer must resolve from this module's own pinned,
	// checksum-verified idunn, not from whatever is newest.
	if strings.Contains(argv[2], "@") {
		t.Errorf("argv names a version to fetch (%q); it must resolve from go.mod", argv[2])
	}
}

func TestLocateRejectsABadBinPath(t *testing.T) {
	for _, tc := range []struct{ name, bin string }{
		{"missing", filepath.Join(os.TempDir(), "definitely-not-here-packer")},
		{"a directory", os.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &packrun.Runner{Bin: tc.bin}
			if _, err := r.Command(packrun.Request{ConfigPath: "p", RepoDir: "r"}); !errors.Is(err, packrun.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

// --- key environment ---------------------------------------------------------

// refusingStat fails the test if anything tries to open a key. It is how the
// promise "this package never reads key material" is kept honest: the only
// filesystem access in keys.go goes through this seam.
func refusingStat(t *testing.T, present map[string]fs.FileMode) packrun.StatFunc {
	t.Helper()
	return func(name string) (fs.FileInfo, error) {
		mode, ok := present[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return fakeInfo{name: name, mode: mode}, nil
	}
}

type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

func TestInspectKeyEnv(t *testing.T) {
	env := map[string]string{
		packrun.EnvTargetsKey:   "/keys/targets.pem",
		packrun.EnvSnapshotKey:  "file:///keys/snapshot.pem",
		packrun.EnvTimestampKey: "/keys/gone.pem",
	}
	lookup := func(name string) (string, bool) { v, ok := env[name]; return v, ok }
	stat := refusingStat(t, map[string]fs.FileMode{
		"/keys/targets.pem":  0o600,
		"/keys/snapshot.pem": 0o644, // group- and world-readable
	})

	got := packrun.InspectKeyEnv(lookup, stat, nil)
	if len(got) != 3 {
		t.Fatalf("got %d statuses, want the three required keys", len(got))
	}
	by := map[string]packrun.KeyStatus{}
	for _, k := range got {
		by[k.Name] = k
	}

	if k := by[packrun.EnvTargetsKey]; !k.Exists || k.Loose || k.Problem() != "" {
		t.Errorf("targets key: %+v, want a clean 0600 file", k)
	}
	// A file: URI must resolve to the same path the packer would read.
	if k := by[packrun.EnvSnapshotKey]; !k.Exists {
		t.Errorf("a file: URI was not resolved to a path: %+v", k)
	} else if !k.Loose {
		t.Error("a 0644 key file was not flagged as readable by others")
	}
	if k := by[packrun.EnvTimestampKey]; k.Exists || !strings.Contains(k.Problem(), "not there") {
		t.Errorf("a missing key file was not reported: %+v", k)
	}
}

// TestInspectKeyEnvRefusesKeyMaterial is the important one. A PEM block in an
// environment variable is a leaked key, and this program must neither use it nor
// copy it anywhere it could be displayed.
func TestInspectKeyEnvRefusesKeyMaterial(t *testing.T) {
	const pem = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIB\n-----END PRIVATE KEY-----"
	lookup := func(name string) (string, bool) {
		if name == packrun.EnvTargetsKey {
			return pem, true
		}
		return "", false
	}

	got := packrun.InspectKeyEnv(lookup, refusingStat(t, nil), nil)
	for _, k := range got {
		if k.Name != packrun.EnvTargetsKey {
			continue
		}
		if !k.Material {
			t.Fatal("a PEM block was not recognised as key material")
		}
		if k.Ref != "" {
			t.Errorf("the key material was copied into Ref: %q", k.Ref)
		}
		if strings.Contains(k.Problem(), "BEGIN") {
			t.Errorf("the message echoes the key: %q", k.Problem())
		}
	}
	if err := packrun.CheckKeyEnv(got); !errors.Is(err, packrun.ErrKeyEnv) {
		t.Errorf("CheckKeyEnv = %v, want ErrKeyEnv", err)
	}
}

func TestInspectKeyEnvIncludesDelegations(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	got := packrun.InspectKeyEnv(lookup, refusingStat(t, nil), []string{"stable", "v1", "long term"})

	names := map[string]bool{}
	for _, k := range got {
		names[k.Name] = true
	}
	for _, want := range []string{
		"TUF_DELEGATION_KEY_STABLE", "TUF_DELEGATION_KEY_V1", "TUF_DELEGATION_KEY_LONG_TERM",
	} {
		if !names[want] {
			t.Errorf("no status for %q; got %v", want, names)
		}
	}

	// An unset delegation override is fine: it falls back to the targets key.
	// Only the three required ones can fail the pre-flight.
	if err := packrun.CheckKeyEnv(got); err == nil {
		t.Error("CheckKeyEnv passed with no required keys set at all")
	} else if strings.Contains(err.Error(), "DELEGATION") {
		t.Errorf("an optional delegation override was treated as required: %v", err)
	}
}

func TestDelegationKeyEnv(t *testing.T) {
	for _, tc := range []struct{ role, want string }{
		{"stable", "TUF_DELEGATION_KEY_STABLE"},
		{"v1", "TUF_DELEGATION_KEY_V1"},
		{"long-term", "TUF_DELEGATION_KEY_LONG_TERM"},
		{"a.b", "TUF_DELEGATION_KEY_A_B"},
	} {
		if got := packrun.DelegationKeyEnv(tc.role); got != tc.want {
			t.Errorf("DelegationKeyEnv(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}

func TestCheckKeyEnvPassesWhenEverythingIsThere(t *testing.T) {
	if err := packrun.CheckKeyEnv(keysOK()); err != nil {
		t.Errorf("CheckKeyEnv = %v, want nil", err)
	}
}
