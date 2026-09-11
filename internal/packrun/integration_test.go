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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
	"github.com/go-idavoll/idunn-fyne/internal/packrun"
)

var (
	realPackerOnce sync.Once
	realPackerPath string
	realPackerErr  error
)

// realPacker builds the packer this module is pinned to, once per test binary.
//
// Testing against the real thing rather than a stand-in is the point: the argv,
// the exit codes and the error text are the packer's contract, and a fake would
// only ever confirm what this package already believes about them.
func realPacker(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: not building the packer")
	}

	realPackerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "idunn-packer-*")
		if err != nil {
			realPackerErr = err
			return
		}
		out := filepath.Join(dir, "packer")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out,
			"github.com/go-idavoll/idunn/cmd/packer")
		if combined, err := cmd.CombinedOutput(); err != nil {
			realPackerErr = errors.New(string(combined))
			return
		}
		realPackerPath = out
	})

	if realPackerErr != nil {
		t.Skipf("could not build the packer: %v", realPackerErr)
	}
	return realPackerPath
}

func writePack(t *testing.T, c *packmodel.Config) string {
	t.Helper()
	raw, err := packmodel.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "pack.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validConfig() *packmodel.Config {
	return &packmodel.Config{
		Name: "demo", Version: "1.2.0", Channel: "stable",
		Targets: []packmodel.Platform{{
			OS: "linux", Arch: "amd64",
			Files: []packmodel.File{{Src: "app", Dst: "bin/app", Kind: "exe"}},
		}},
	}
}

// TestAgainstTheRealPackerAConfigWeAcceptGetsPastValidation is the cross-check
// that keeps packmodel's mirrored rules honest.
//
// The packer validates pack.yaml before it resolves any key, so running it with
// no keys set reaches exactly the question worth asking -- did it like the
// configuration? -- and stops before anything is signed or written. A "packer:
// key" refusal therefore means the configuration was accepted.
func TestAgainstTheRealPackerAConfigWeAcceptGetsPastValidation(t *testing.T) {
	bin := realPacker(t)

	cfg := validConfig()
	if problems := cfg.Validate(); len(problems) != 0 {
		t.Fatalf("our own validator rejected the fixture: %v", problems)
	}

	r := &packrun.Runner{Bin: bin, Environ: func() []string { return nil }}
	res, err := r.Publish(context.Background(), packrun.Request{
		ConfigPath: writePack(t, cfg),
		RepoDir:    t.TempDir(),
		Now:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}, keysOK()) // keysOK passes our pre-flight; the packer finds them unset itself

	if err == nil {
		t.Fatal("the packer published without any signing key")
	}
	if !errors.Is(err, packrun.ErrPublish) {
		t.Fatalf("err = %v, want ErrPublish", err)
	}
	if !strings.Contains(res.Stderr, "packer: key") {
		t.Errorf("the packer stopped somewhere other than the key step, so it did not "+
			"accept our configuration:\n%s", res.Stderr)
	}
	if strings.Contains(res.Stderr, "packer: config") {
		t.Errorf("the real packer rejected a configuration packmodel accepted; "+
			"internal/packmodel/mirror.go has drifted:\n%s", res.Stderr)
	}
}

// TestAgainstTheRealPackerRejectionsAgree walks the rules packmodel mirrors and
// checks the packer refuses each one too, with a config error rather than a key
// error. Drift shows up here as a failure rather than as a puzzled publisher.
func TestAgainstTheRealPackerRejectionsAgree(t *testing.T) {
	bin := realPacker(t)

	for _, tc := range []struct {
		name   string
		mutate func(*packmodel.Config)
	}{
		{"not semver", func(c *packmodel.Config) { c.Version = "1.2" }},
		{"leading zero", func(c *packmodel.Config) { c.Version = "01.2.0" }},
		{"release-line channel", func(c *packmodel.Config) { c.Channel = "v1" }},
		{"channel with a slash", func(c *packmodel.Config) { c.Channel = "a/b" }},
		{"upper-case os", func(c *packmodel.Config) { c.Targets[0].OS = "Linux" }},
		{"traversal dst", func(c *packmodel.Config) { c.Targets[0].Files[0].Dst = "../../etc/passwd" }},
		{"absolute dst", func(c *packmodel.Config) { c.Targets[0].Files[0].Dst = "/etc/passwd" }},
		{"windows device dst", func(c *packmodel.Config) { c.Targets[0].Files[0].Dst = "NUL" }},
		{"unclean dst", func(c *packmodel.Config) { c.Targets[0].Files[0].Dst = "bin//app" }},
		{"unknown kind", func(c *packmodel.Config) { c.Targets[0].Files[0].Kind = "script" }},
		{"setuid mode", func(c *packmodel.Config) { c.Targets[0].Files[0].Mode = "4755" }},
		{"non-octal mode", func(c *packmodel.Config) { c.Targets[0].Files[0].Mode = "999" }},
		{"rollout above one", func(c *packmodel.Config) { c.Rollout = 1.5 }},
		{"bad requirement", func(c *packmodel.Config) { c.Requirements.MinFromVersion = "latest" }},
		{"no files", func(c *packmodel.Config) { c.Targets[0].Files = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)

			if problems := cfg.Validate(); len(problems) == 0 {
				t.Fatalf("packmodel accepted %q; the packer is about to refuse it", tc.name)
			}

			// Marshal refuses nothing that parses, so the file still gets written
			// and the packer gets its own say.
			raw, err := packmodel.Marshal(cfg)
			if err != nil {
				t.Skipf("not representable as YAML: %v", err)
			}
			path := filepath.Join(t.TempDir(), "pack.yaml")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			r := &packrun.Runner{Bin: bin, Environ: func() []string { return nil }}
			res, err := r.Publish(context.Background(), packrun.Request{
				ConfigPath: path,
				RepoDir:    t.TempDir(),
				Now:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			}, keysOK())

			if err == nil {
				t.Fatalf("the packer published %q", tc.name)
			}
			if !strings.Contains(res.Stderr, "packer: config") {
				t.Errorf("the packer refused for some other reason than the configuration; "+
					"packmodel and the packer disagree about %q:\n%s", tc.name, res.Stderr)
			}
		})
	}
}

// TestAgainstTheRealPackerUsageExitCode covers the one exit code that means this
// program built the command wrongly, so the interface can say so instead of
// blaming the operator.
func TestAgainstTheRealPackerUsageExitCode(t *testing.T) {
	bin := realPacker(t)

	// --repo is required; the packer exits 2 when it is missing. Going around
	// Runner.argv here is the point: this is what a bug in it would look like.
	cmd := exec.CommandContext(context.Background(), bin, "publish",
		"--config", writePack(t, validConfig()))
	out, _ := cmd.CombinedOutput()
	code := cmd.ProcessState.ExitCode()

	if code != 2 {
		t.Fatalf("exit = %d, want 2 for a missing --repo\n%s", code, out)
	}
	if !strings.Contains(string(out), "--repo is required") {
		t.Errorf("unexpected message:\n%s", out)
	}
}

// TestVerifyRefusesWhatIsNotARepository covers the error paths of the
// post-publish check. The happy path needs a signed repository, which needs
// keys, which this program will not create -- so it is proven in the packer's
// own tests upstream, not here.
func TestVerifyRefusesWhatIsNotARepository(t *testing.T) {
	empty := t.TempDir()

	for _, tc := range []struct {
		name string
		req  packrun.VerifyRequest
		want string
	}{
		{"no directory", packrun.VerifyRequest{}, "no repository directory"},
		{"not a repository", packrun.VerifyRequest{RepoDir: empty}, "not a published repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := packrun.Verify(context.Background(), tc.req)
			if !errors.Is(err, packrun.ErrVerify) {
				t.Fatalf("err = %v, want ErrVerify", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestVerifyNeedsARootTheCeremonyMade: the packer never writes root.json, so a
// repository without one cannot be verified, and the message has to say why
// rather than leaving an operator looking for a packer flag that will never
// exist.
func TestVerifyNeedsARootTheCeremonyMade(t *testing.T) {
	repo := t.TempDir()
	for _, d := range []string{packrun.MetadataDir, packrun.TargetsDir} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := packrun.Verify(context.Background(), packrun.VerifyRequest{RepoDir: repo})
	if !errors.Is(err, packrun.ErrVerify) {
		t.Fatalf("err = %v, want ErrVerify", err)
	}
	if !strings.Contains(err.Error(), "root ceremony") {
		t.Errorf("err = %v, want it to explain that the packer never creates root.json", err)
	}
}
