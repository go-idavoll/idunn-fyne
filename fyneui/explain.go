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

package fyneui

import (
	"context"
	"errors"
	"io/fs"
	"net"

	"fyne.io/fyne/v2/widget"
	"github.com/go-idavoll/idunn/core/elevate"
	"github.com/go-idavoll/idunn/core/installer"
	"github.com/go-idavoll/idunn/core/launch"
	"github.com/go-idavoll/idunn/core/stage"
	"github.com/go-idavoll/idunn/core/timefloor"
	"github.com/go-idavoll/idunn/core/trust"
	"github.com/go-idavoll/idunn/core/txn"
	"github.com/go-idavoll/idunn/core/updater"
)

// Class is the kind of thing that went wrong. It mirrors the Reporter taxonomy
// in core/updater/errors.go so that what a user is told and what a publisher is
// told describe the same event.
//
// It is a mirror, not the original. core's classify() is unexported and also
// matches two sentinels under internal/ — layout.ErrLayout and
// safepath.ErrUnsafe — which no other module can import. Those land in
// ClassUnknown here. When the two disagree, core is right.
//
// TODO(idunn): ask upstream for an exported updater.Classify(error) string,
// which would let this file shrink to the wording alone.
type Class string

// The error classes. Several are finer-grained than core's, because a person
// being shown a message needs distinctions telemetry does not: "deferred" and
// "busy" are one class to a publisher counting failures and two very different
// sentences to a user.
const (
	ClassNone                 Class = ""
	ClassCancelled            Class = "cancelled"
	ClassClockSkew            Class = "clock_skew"
	ClassTimeFloor            Class = "time_floor"
	ClassNetwork              Class = "network"
	ClassDeclined             Class = "declined"
	ClassDeferred             Class = "deferred"
	ClassBusy                 Class = "busy"
	ClassCheck                Class = "check"
	ClassMigrate              Class = "migrate"
	ClassStale                Class = "stale"
	ClassPolicy               Class = "policy"
	ClassConfig               Class = "config"
	ClassVerify               Class = "verify"
	ClassResolve              Class = "resolve"
	ClassPermission           Class = "permission"
	ClassDisk                 Class = "disk"
	ClassElevationDeclined    Class = "elevation_declined"
	ClassElevationUnsupported Class = "elevation_unsupported"
	ClassElevationRequest     Class = "elevation_request"
	ClassElevationFailed      Class = "elevation_failed"
	ClassInstallerRefused     Class = "installer_refused"
	ClassPrompt               Class = "prompt"
	ClassLaunch               Class = "launch"
	ClassUnknown              Class = "unknown"
)

// Severity says how an outcome should be presented.
type Severity int

// The severities. Info is the one that matters most: idunn has two terminal
// states that are not failures at all, and showing them in red teaches people
// that a working updater is broken.
const (
	SeverityNone Severity = iota
	SeverityInfo
	SeverityWarning
	SeverityError
)

// Explanation is one error as it is put in front of a person.
type Explanation struct {
	Class    Class
	Severity Severity
	Title    string // short, for a banner or a dialog title.
	Detail   string // one sentence: what happened, and what can be done about it.

	// Retry says whether offering "Try again" is honest. It is deliberately
	// false for verification and policy failures: a button that invites someone
	// to retry a signature mismatch teaches them to click through it.
	Retry bool

	// Technical is err.Error(), for a details disclosure. The Reporter rule
	// against raw error strings (design.md §14.5) governs what leaves the
	// machine; it does not govern what the operator sitting at it may read.
	// An Explanation is never handed to a hook.Reporter.
	Technical string
}

// Benign reports whether this outcome means nothing went wrong.
func (x Explanation) Benign() bool { return x.Severity <= SeverityInfo }

// Explain classifies err and renders it for a person.
//
// The order mirrors core's classify(): the most specific diagnosis wins. Expired
// metadata is reported as a clock problem rather than a verification failure,
// because it is the one failure a user can usually fix themselves, and saying
// "the update failed" when the answer is "your clock is wrong" is how a
// fail-closed system becomes an unexplained one.
func Explain(err error) Explanation {
	x := explain(err)
	if err != nil {
		x.Technical = err.Error()
	}
	return x
}

// explain is the classification itself, kept separate so Explain can attach the
// technical text in one place. The flat, ordered chain of errors.Is is
// deliberate: it mirrors core's classify() case for case, so the two can be read
// side by side when either changes.
func explain(err error) Explanation {
	var netErr net.Error

	switch {
	case err == nil:
		return Explanation{Class: ClassNone, Severity: SeverityNone,
			Title:  "Up to date",
			Detail: "The update finished without a problem."}

	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Explanation{Class: ClassCancelled, Severity: SeverityInfo, Retry: true,
			Title:  "Update cancelled",
			Detail: "The update stopped before anything was changed. Nothing was installed."}

	case trust.IsExpiry(err), errors.Is(err, timefloor.ErrClockRollback):
		return Explanation{Class: ClassClockSkew, Severity: SeverityWarning, Retry: true,
			Title: "Check the system clock",
			Detail: "This computer's clock reads a time the update service cannot accept. " +
				"Correct the date and time, then try again."}

	case errors.Is(err, timefloor.ErrFloor):
		return Explanation{Class: ClassTimeFloor, Severity: SeverityError,
			Title: "The update record could not be read",
			Detail: "The file recording when this installation was last known to be current " +
				"is missing or damaged."}

	case errors.As(err, &netErr):
		return Explanation{Class: ClassNetwork, Severity: SeverityWarning, Retry: true,
			Title: "No connection to the update service",
			Detail: "The update service could not be reached. Check the network connection " +
				"and try again."}

	case errors.Is(err, elevate.ErrDeclined):
		return Explanation{Class: ClassElevationDeclined, Severity: SeverityInfo, Retry: true,
			Title: "Administrator approval was not given",
			Detail: "Installing into this location needs administrator rights. " +
				"Nothing was changed."}

	case errors.Is(err, elevate.ErrNotImplemented):
		return Explanation{Class: ClassElevationUnsupported, Severity: SeverityError,
			Title: "Not supported on this system",
			Detail: "Installing into this location needs administrator rights, which this " +
				"build cannot request. Re-run with those rights, or install into a " +
				"per-user location."}

	case errors.Is(err, elevate.ErrRequest):
		return Explanation{Class: ClassElevationRequest, Severity: SeverityError,
			Title: "The privileged request was refused",
			Detail: "The request sent to the privileged helper was rejected before it ran. " +
				"This is a defect in the application; nothing was changed."}

	case errors.Is(err, elevate.ErrHelper):
		return Explanation{Class: ClassElevationFailed, Severity: SeverityError,
			Title: "The privileged helper failed",
			Detail: "The component that installs with administrator rights did not finish. " +
				"Nothing was left half-installed."}

	case errors.Is(err, ErrPrompt):
		return Explanation{Class: ClassPrompt, Severity: SeverityError,
			Title: "The confirmation could not be shown",
			Detail: "The update was not installed because it could not be put to you for " +
				"approval. This is a defect in the application."}

	case errors.Is(err, updater.ErrDeclined):
		return Explanation{Class: ClassDeclined, Severity: SeverityInfo, Retry: true,
			Title:  "Update not installed",
			Detail: "You chose not to install this update. It will be offered again later."}

	// Deferred is matched before Busy: quiesce returns ErrDeferred under
	// BusyDeferToRestart and ErrBusy under BusyAbort, and a rollback path can
	// join errors, so an error can match both. Deferred is the more specific and
	// by far the better news.
	case errors.Is(err, updater.ErrDeferred):
		return Explanation{Class: ClassDeferred, Severity: SeverityInfo,
			Title: "Ready for the next start",
			Detail: "The update is downloaded, verified and waiting. It will be completed " +
				"the next time this application starts."}

	case errors.Is(err, updater.ErrBusy):
		return Explanation{Class: ClassBusy, Severity: SeverityWarning, Retry: true,
			Title: "The application is still running",
			Detail: "The update needs a moment when nothing is writing. Close every window " +
				"of this application and try again."}

	case errors.Is(err, updater.ErrCheck):
		return Explanation{Class: ClassCheck, Severity: SeverityError,
			Title:  "The update was refused before it started",
			Detail: "A pre-flight check rejected this update. Nothing was changed."}

	case errors.Is(err, updater.ErrMigrate):
		return Explanation{Class: ClassMigrate, Severity: SeverityError,
			Title: "The data migration failed",
			Detail: "The update could not convert this installation's data, so everything " +
				"was put back the way it was."}

	case errors.Is(err, updater.ErrStale):
		return Explanation{Class: ClassStale, Severity: SeverityWarning, Retry: true,
			Title: "This installation changed",
			Detail: "Something else changed this installation while the update was being " +
				"prepared. Check for updates again."}

	case errors.Is(err, updater.ErrPolicy):
		return Explanation{Class: ClassPolicy, Severity: SeverityError,
			Title:  "This update does not apply here",
			Detail: "This release cannot be installed over the version that is present."}

	case errors.Is(err, installer.ErrRefused):
		return Explanation{Class: ClassInstallerRefused, Severity: SeverityError,
			Title: "An installation is already present",
			Detail: "There is already an installation here that this installer will not " +
				"write over. Use the application's own updater instead."}

	case errors.Is(err, updater.ErrConfig):
		return Explanation{Class: ClassConfig, Severity: SeverityError,
			Title: "The updater is misconfigured",
			Detail: "The update system was not set up correctly. This is a defect in the " +
				"application, not a problem with the release."}

	case errors.Is(err, updater.ErrVerify), errors.Is(err, trust.ErrTrust):
		return Explanation{Class: ClassVerify, Severity: SeverityError,
			Title: "The release could not be verified",
			Detail: "The downloaded files did not match what the publisher signed. " +
				"Nothing was installed."}

	case errors.Is(err, trust.ErrResolve):
		return Explanation{Class: ClassResolve, Severity: SeverityError,
			Title:  "The release could not be found",
			Detail: "The update service offered no release for this channel and platform."}

	case errors.Is(err, fs.ErrPermission):
		return Explanation{Class: ClassPermission, Severity: SeverityError,
			Title:  "Permission denied",
			Detail: "This application may not write to its own installation folder."}

	case errors.Is(err, stage.ErrStage), errors.Is(err, txn.ErrJournal),
		errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrExist):
		return Explanation{Class: ClassDisk, Severity: SeverityError, Retry: true,
			Title: "The installation folder could not be written",
			Detail: "The update could not be written to disk. Check the free space, and " +
				"that no other program is holding these files open."}

	case errors.Is(err, launch.ErrLaunch):
		return Explanation{Class: ClassLaunch, Severity: SeverityWarning, Retry: true,
			Title: "The pending update could not be finished",
			Detail: "The update that was waiting could not be applied at this start. " +
				"The application is running the version it had before."}

	default:
		return Explanation{Class: ClassUnknown, Severity: SeverityError,
			Title:  "The update failed",
			Detail: "The update did not complete. Nothing was left half-installed."}
	}
}

// AllClasses lists every class this package can produce. A test walks it to
// prove no class can be added without wording to go with it.
func AllClasses() []Class {
	return []Class{
		ClassNone, ClassCancelled, ClassClockSkew, ClassTimeFloor, ClassNetwork,
		ClassDeclined, ClassDeferred, ClassBusy, ClassCheck, ClassMigrate,
		ClassStale, ClassPolicy, ClassConfig, ClassVerify, ClassResolve,
		ClassPermission, ClassDisk, ClassElevationDeclined,
		ClassElevationUnsupported, ClassElevationRequest, ClassElevationFailed,
		ClassInstallerRefused, ClassPrompt, ClassLaunch, ClassUnknown,
	}
}

func importanceFor(s Severity) widget.Importance {
	switch s {
	case SeverityWarning:
		return widget.WarningImportance
	case SeverityError:
		return widget.DangerImportance
	case SeverityNone, SeverityInfo:
		return widget.MediumImportance
	default:
		return widget.MediumImportance
	}
}
