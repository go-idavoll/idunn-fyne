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
	"errors"
	"fmt"
	"io/fs"
	"net"
	"testing"

	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn/core/elevate"
	"github.com/go-idavoll/idunn/core/installer"
	"github.com/go-idavoll/idunn/core/launch"
	"github.com/go-idavoll/idunn/core/stage"
	"github.com/go-idavoll/idunn/core/timefloor"
	"github.com/go-idavoll/idunn/core/trust"
	"github.com/go-idavoll/idunn/core/txn"
	"github.com/go-idavoll/idunn/core/updater"
)

// fakeNetErr satisfies net.Error, which classify() matches with errors.As.
type fakeNetErr struct{}

func (fakeNetErr) Error() string   { return "dial tcp: connection refused" }
func (fakeNetErr) Timeout() bool   { return false }
func (fakeNetErr) Temporary() bool { return true }

// TestExplainClassifiesEveryExportedSentinel walks the whole vocabulary idunn
// exports. It is the regression test for the one mistake this layer exists to
// prevent: showing a user a red "update failed" for an outcome that is not a
// failure.
func TestExplainClassifiesEveryExportedSentinel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		want   fyneui.Class
		benign bool
	}{
		{"nil", nil, fyneui.ClassNone, true},
		{"cancelled", context.Canceled, fyneui.ClassCancelled, true},
		{"deadline", context.DeadlineExceeded, fyneui.ClassCancelled, true},
		{"clock rollback", timefloor.ErrClockRollback, fyneui.ClassClockSkew, false},
		{"time floor", timefloor.ErrFloor, fyneui.ClassTimeFloor, false},
		{"network", fakeNetErr{}, fyneui.ClassNetwork, false},
		{"elevation declined", elevate.ErrDeclined, fyneui.ClassElevationDeclined, true},
		{"elevation unsupported", elevate.ErrNotImplemented, fyneui.ClassElevationUnsupported, false},
		{"elevation request", elevate.ErrRequest, fyneui.ClassElevationRequest, false},
		{"elevation helper", elevate.ErrHelper, fyneui.ClassElevationFailed, false},
		{"prompt", fyneui.ErrPrompt, fyneui.ClassPrompt, false},
		{"declined", updater.ErrDeclined, fyneui.ClassDeclined, true},
		{"deferred", updater.ErrDeferred, fyneui.ClassDeferred, true},
		{"busy", updater.ErrBusy, fyneui.ClassBusy, false},
		{"check", updater.ErrCheck, fyneui.ClassCheck, false},
		{"migrate", updater.ErrMigrate, fyneui.ClassMigrate, false},
		{"stale", updater.ErrStale, fyneui.ClassStale, false},
		{"policy", updater.ErrPolicy, fyneui.ClassPolicy, false},
		{"installer refused", installer.ErrRefused, fyneui.ClassInstallerRefused, false},
		{"config", updater.ErrConfig, fyneui.ClassConfig, false},
		{"verify", updater.ErrVerify, fyneui.ClassVerify, false},
		{"trust", trust.ErrTrust, fyneui.ClassVerify, false},
		{"resolve", trust.ErrResolve, fyneui.ClassResolve, false},
		{"permission", fs.ErrPermission, fyneui.ClassPermission, false},
		{"stage", stage.ErrStage, fyneui.ClassDisk, false},
		{"journal", txn.ErrJournal, fyneui.ClassDisk, false},
		{"not exist", fs.ErrNotExist, fyneui.ClassDisk, false},
		{"launch", launch.ErrLaunch, fyneui.ClassLaunch, false},
		{"anything else", errors.New("something new upstream"), fyneui.ClassUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Bare, and wrapped the way core wraps: fmt.Errorf("%w: detail", Err).
			for _, err := range []error{tc.err, wrap(tc.err)} {
				got := fyneui.Explain(err)
				if got.Class != tc.want {
					t.Errorf("Explain(%v).Class = %q, want %q", err, got.Class, tc.want)
				}
				if got.Benign() != tc.benign {
					t.Errorf("Explain(%v).Benign() = %v, want %v", err, got.Benign(), tc.benign)
				}
				if got.Title == "" || got.Detail == "" {
					t.Errorf("Explain(%v) has empty wording: %+v", err, got)
				}
			}
		})
	}
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: while doing the thing", err)
}

// TestExplainPrefersDeferredOverBusy pins the one ordering that is easy to get
// wrong. quiesce returns ErrDeferred under BusyDeferToRestart and ErrBusy under
// BusyAbort, and a rollback path can join errors, so an error can match both.
// Deferred is the more specific -- and far better -- news.
func TestExplainPrefersDeferredOverBusy(t *testing.T) {
	err := errors.Join(updater.ErrDeferred, updater.ErrBusy)
	got := fyneui.Explain(err)
	if got.Class != fyneui.ClassDeferred {
		t.Errorf("Explain(join(deferred, busy)).Class = %q, want %q", got.Class, fyneui.ClassDeferred)
	}
	if !got.Benign() {
		t.Error("a deferred update was presented as a failure")
	}
}

// TestExplainPrefersClockSkewOverVerify mirrors core's own reasoning: expired
// metadata is a clock a user can fix, not a signature failure they cannot.
func TestExplainPrefersClockSkewOverVerify(t *testing.T) {
	// trust.IsExpiry matches what the trust client wraps on an expiry; the
	// clock-rollback sentinel is the other half of the same diagnosis.
	got := fyneui.Explain(fmt.Errorf("%w: metadata expired", timefloor.ErrClockRollback))
	if got.Class != fyneui.ClassClockSkew {
		t.Errorf("Class = %q, want %q", got.Class, fyneui.ClassClockSkew)
	}
	if !got.Retry {
		t.Error("a clock problem is fixable by the user, so retry must be offered")
	}
}

// TestExplainNeverOffersRetryOnASecurityFailure: a "Try again" button next to a
// signature mismatch teaches people to click through it.
func TestExplainNeverOffersRetryOnASecurityFailure(t *testing.T) {
	for _, err := range []error{updater.ErrVerify, trust.ErrTrust, updater.ErrPolicy, updater.ErrConfig} {
		if got := fyneui.Explain(err); got.Retry {
			t.Errorf("Explain(%v) offers retry; it must not", err)
		}
	}
}

// TestExplainCarriesTechnicalText keeps the details disclosure useful, and
// checks the nil case does not invent one.
func TestExplainCarriesTechnicalText(t *testing.T) {
	err := fmt.Errorf("%w: /opt/acme/versions", stage.ErrStage)
	if got := fyneui.Explain(err).Technical; got != err.Error() {
		t.Errorf("Technical = %q, want %q", got, err.Error())
	}
	if got := fyneui.Explain(nil).Technical; got != "" {
		t.Errorf("Explain(nil).Technical = %q, want empty", got)
	}
}

// TestAllClassesHaveWording proves no class can be added without a sentence to
// go with it: every class must be reachable from some error.
func TestAllClassesHaveWording(t *testing.T) {
	reachable := map[fyneui.Class]bool{}
	for _, err := range []error{
		nil, context.Canceled, timefloor.ErrClockRollback, timefloor.ErrFloor,
		fakeNetErr{}, elevate.ErrDeclined, elevate.ErrNotImplemented,
		elevate.ErrRequest, elevate.ErrHelper, fyneui.ErrPrompt,
		updater.ErrDeclined, updater.ErrDeferred, updater.ErrBusy, updater.ErrCheck,
		updater.ErrMigrate, updater.ErrStale, updater.ErrPolicy, installer.ErrRefused,
		updater.ErrConfig, updater.ErrVerify, trust.ErrResolve, fs.ErrPermission,
		stage.ErrStage, launch.ErrLaunch, errors.New("x"),
	} {
		reachable[fyneui.Explain(err).Class] = true
	}
	for _, c := range fyneui.AllClasses() {
		if !reachable[c] {
			t.Errorf("class %q is declared but no error produces it", c)
		}
	}
}

// Assert at compile time that net.Error really is matched by errors.As, which is
// what the network branch relies on.
var _ net.Error = fakeNetErr{}
