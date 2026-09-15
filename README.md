<div align="center">

# idunn-fyne

**The Fyne UI sidecar for [idunn](https://github.com/go-idavoll/idunn).**
_Renders an update; decides nothing about it._

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-early--implementation-orange.svg)](#status)

</div>

---

## What this is

idunn is a cryptographically secure installer and updater for Go applications. It
runs headless by default and keeps its `core` free of any UI dependency; UI lives
in separate sidecar modules that implement two optional interfaces and nothing
else — `hook.Observer` to render the lifecycle, `hook.Prompter` to ask the one
question. This is the Fyne one, and the reference implementation for
[IDN-19](https://github.com/go-idavoll/idunn/blob/main/docs/backlog.md). Fyne
appears in a dependency graph only because a host chose to import this (idunn
`docs/design.md` §8).

It contains:

| | |
|---|---|
| [`.`](window.go) | `ProgressWindow`: the sidecar as a whole window, with byte-level staging progress. A host adds two lines and gets a visible update |
| [`fyneui`](fyneui) | the sidecar as a panel to drop into a window the host already owns, plus `Explain` for error wording and `Run` for the threading rule below |
| [`cmd/idunn-fyne-demo`](cmd/idunn-fyne-demo) | a synthetic release going past: installs nothing, touches no root, reaches no network |
| [`cmd/demo`](cmd/demo) | a host that runs a real update, so the interfaces are shown to be sufficient |
| [`cmd/packassist`](cmd/packassist) | a packing assistant: writes a `pack.yaml`, runs the idunn packer, resolves the result |

> **Two adapters, for now.** The root package and `fyneui` arrived from two
> branches and overlap: one owns a window, the other is embedded in one. They
> compile and are tested independently, and consolidating them is the next
> design decision this module owes, not a merge artefact to be settled quietly.

**What it is not.** It makes no trust decision. It verifies nothing, signs
nothing, and holds no key. Every verdict about what may be installed is
`core/trust` and go-tuf's, exactly as it is for a headless build.

## Using it

Dropping the two hook lines gives back a headless update. Nothing else changes.

```go
import fyneui "github.com/go-idavoll/idunn-fyne"

a := app.New()
ui := fyneui.New(a, "Acme")   // the root package: the sidecar owns the window

u, _ := updater.New(updater.Options{
    Trust: client, FS: fsx.OS(), Root: "/opt/acme", Channel: "stable",

    Observe: ui, // byte-level progress in the window
    Prompt:  ui, // "Install Acme 1.3.0 now?"
})

go func() {
    r, err := u.CheckForUpdate(ctx)
    if err != nil || r == nil {
        return // nothing to install; stay hidden
    }
    _ = u.Apply(ctx, r)
}()
ui.Window().ShowAndRun()
```

A host that already owns its window uses the `fyneui` package instead and places
the panel itself:

```go
import "github.com/go-idavoll/idunn-fyne/fyneui"

ui := fyneui.New(win)          // the fyneui package: a widget, not a window
defer ui.Close()
win.SetContent(ui.Widget())
ui.Start()                      // from the Fyne goroutine, once the window exists

// Never from a widget callback -- see the threading contract below.
fyneui.Run(func() error {
    rel, err := u.CheckForUpdate(ctx)
    if err != nil || rel == nil {
        return err
    }
    return u.Apply(ctx, rel)
}, func(err error) {
    x := fyneui.Explain(err)
    // x.Title, x.Detail, x.Benign()
})
```

## What it shows

idunn reports staging in **bytes**: how much of the release has been written against a
total taken from the signed lengths before the first byte moves, plus the file being
written and where its bytes come from.

That last part is the one worth having. A release is assembled three ways — reused
from a version already installed, reconstructed from a delta patch, or downloaded —
and they differ by orders of magnitude in what they cost. A window that says
*downloading* while a gigabyte is copied off the local disk is telling the user the
wrong thing about how long this will take, so the source is named:

```
820.0 MiB of 964.5 MiB
Reusing lib/libcef.so (1 of 4) · 412.6 MiB/s, 1s left
```

Outside staging there is no byte count, and none is invented: a quiesce or a
migration says what it is doing and leaves the bar where it was. A failure keeps the
bar where it stopped rather than filling it over an error message.

Run `go run ./cmd/idunn-fyne-demo` to watch a synthetic release go past.

## A byte count is not a position in the update

`hook.Event.Progress` carries a real fraction while staging and `-1` everywhere
else — and the fraction is of **staging**, not of the transaction. A release whose
bytes are all in place is nowhere near installed: the swap, the verify and the
commit are still to come.

So `fyneui.Fraction` gives each phase a span of the bar and places the byte
fraction inside the span staging owns. Handing the raw value to the bar would fill
it at the end of the download and then have to move it backwards to tell the truth,
which reads as "it is doing the update again". Over the rest of a transaction the
bar is still a step count, and it is labelled `Installing`, never `63%`.

The phase order those spans use is the order idunn **emits** them, which is not the
order `hook.Phase` declares them: `PhaseVerify` is emitted *after* `PhaseApply`,
`PhaseStage` is never emitted on success at all, and `PhaseQuiesce` follows
`PhaseDownload`. Driving a bar from the declaration order sends it backwards near
the end of every update that has `VerifyAfterApply` switched on. An integration
test drives a real transaction and fails if the bar would ever step back.

## The threading contract

Three rules. They are not style advice; each one follows from something in
idunn's or Fyne's source, and getting one wrong breaks an update or freezes a
window.

**1. Never call `Apply` from the Fyne goroutine.**
`Prompter.Confirm` blocks by design, and the dialog it raises needs the Fyne
goroutine in order to be drawn. Calling `Apply` from a widget callback therefore
blocks the event loop inside `Confirm`, the dialog is queued behind the very
goroutine waiting for it, and the application freezes for good. Go cannot detect
which goroutine is the UI one, and Fyne's own check is under `internal/`, so this
cannot be refused — only survived. The root package's `Confirm` honours the
context, so an update being torn down does not wait on a dialog nobody is looking
at; `fyneui.Confirm` gives up after `DefaultConfirmTimeout` with `ErrPrompt`,
which turns a permanent freeze into a five-second hitch and a named error.
`fyneui.Run` is the helper that keeps the call off the UI goroutine in the first
place.

**2. `OnEvent` runs on the updater's goroutine, inside an open transaction.**
It takes a mutex, folds the event into the model, and hands a repaint on. It
reaches no Fyne symbol beyond `fyne.Do` after the app exists, because `fyne.Do`
dereferences `fyne.CurrentApp`, which is nil until the host starts its app — and a
panic in an Observer is not recovered by core, so it would take the process down
mid-update. Repaints **coalesce**: a release reporting every megabyte produces one
repaint per main-loop turn rather than a backlog of stale ones. Do not put work in
the render callback.

**3. `Reset` before each transaction.** Two updates in one session are two
transactions. Without it the second is drawn on top of the first and the progress
bar appears to jump backwards as the next run starts at `PhaseCheck`.

## How the root package is built

The split is the point:

- **`Model`** (`progress.go`) turns the event stream into a `State`: the headline, the
  detail line, the fraction, the throughput estimate and what is left of it. It has no
  Fyne in it, so what a release's progress *means* is decided in one place and tested
  without a display.
- **`ProgressWindow`** (`window.go`) copies a `State` into widgets, and owns the
  threading described above.

## Deferred and declined are not failures

`updater.ErrDeferred` and `updater.ErrDeclined` arrive as non-nil errors and are
ordinary outcomes: the update is staged and waiting for a restart, or the user
said no. `fyneui.Explain` reports both as benign. Showing them in red is the
easiest mistake to make at this interface, and it teaches people that a working
updater is broken.

`Explain` mirrors core's unexported `classify()` over the sentinels idunn
exports. It is a mirror, not the original: `layout.ErrLayout` and
`safepath.ErrUnsafe` live under `internal/` and are unreachable from another
module, so they land in `ClassUnknown` here. An exported `updater.Classify` would
close that gap.

## What a release descriptor contains

`Name`, `Version`, `Channel`, `OS`, `Arch`, `Files[]`, `Requirements`, `Rollout`,
`LayoutSchema` — and nothing else. No release notes, no download size, no
publication date, no URL. `cmd/demo` shows every field there is and says so.

Design the UI for that. Filling the gap from a side channel would create a
second, unsigned metadata path next to the signed one, which is the thing the
trust model exists to prevent.

## cmd/demo

```sh
go run ./cmd/demo               # a throwaway root, an in-memory fixture
go run ./cmd/demo --busy        # the deferred-to-restart path
go run ./cmd/demo --root DIR --tuf-root anchor.json \
    --tuf-metadata-url https://updates.example.com/metadata/ \
    --tuf-targets-url  https://updates.example.com/targets/
```

By default it installs from an in-memory fixture: no TUF repository, no signing
key, no network. Everything below the resolver is genuine — staging, the
transaction journal, the atomic swap, garbage collection — because
`updater.Resolver` is an exported interface and the fixture stands exactly where
the trust client stands. With the `--tuf-*` flags it resolves against a real
repository, and **not one line of the screen changes between the two**.

`--busy` hangs a fake application lock off the updater with
`BusyDeferToRestart`, so the deferred outcome can be watched: it arrives as a
non-nil error, is shown calmly, and a button finishes it through `launch.Start`.

## cmd/packassist

```sh
go run ./cmd/packassist --config pack.yaml --repo ./tuf-repo --packer-bin /usr/local/bin/packer
```

Four tabs: the release, the files, the generated `pack.yaml`, and the publish.

**It never touches key material.** It learns that a `TUF_*` variable is set,
stats the file it names so it can say "that file is not there" before a publish
spends anything finding out, and passes the value to the packer — which opens it,
in its own process. A variable holding a PEM block is reported as key material
without being quoted anywhere. It generates no keys and cannot sign `root.json`;
a root ceremony does that, offline.

**It re-implements no part of publishing.** `internal/packer` cannot be imported
from another module, so the assistant runs `cmd/packer` as a subprocess. The
packer owns the repository layout, the signing and the reproducibility rules, and
a second implementation of any of them in a GUI would be a parallel trust path.

Locating the binary is `--packer-bin` first, then `packer` on `PATH`, and `go
run` only behind `--allow-go-run`. Fetching and building code is not something a
publishing tool should do without being asked.

**Its validation is advisory.** It rejects, in the form, what the packer would
reject when it runs — but whether every `src` exists, and whether two files hold
identical bytes, only the packer can answer. The summary says so rather than
calling a clean form "valid".

**Verification is the real done-criterion.** After a publish, "Resolve it as a
client would" serves the repository on a loopback listener and runs the actual
`core/trust` client through `Refresh`, `LatestRelease` and every payload. When no
anchor is supplied it falls back to the repository's own `root.json` and says
what that is worth: internal consistency, not a signature from the keys your
installations trust.

## Mirrored upstream rules

Two files hold copies of values that are unexported in idunn and unreachable from
another module: [`internal/packmodel/mirror.go`](internal/packmodel/mirror.go)
(the name, channel, os, arch and mode patterns) and the metadata directory names
in [`internal/packrun`](internal/packrun). Both carry a header saying so.

Copies rot, so the rejection table in `internal/packmodel/validate_test.go` is
transcribed case for case from idunn's `internal/packer/config_test.go`, and an
integration test builds the real packer and runs every mirrored rule past it.
Drift fails a test instead of puzzling a publisher. An exported
`packer.ValidateConfig([]byte) error` upstream would delete `mirror.go` outright.

## Requirements

Go 1.26 and cgo. Fyne needs a C toolchain and the platform's GL and windowing
headers; only `cmd/...` and the root package's godoc example link the desktop
driver, and only Linux needs the headers spelled out:

```sh
make deps-linux   # libgl1-mesa-dev libxcursor-dev libxrandr-dev libxinerama-dev
                  # libxi-dev libxxf86vm-dev libwayland-dev libxkbcommon-dev
                  # wayland-protocols
```

macOS needs Xcode command line tools; Windows needs a MinGW toolchain. See
[Fyne's own prerequisites](https://docs.fyne.io/started/) for the current list.

`fyneui` and everything under `internal/` link no driver, so they build and test
with **no OpenGL and no display at all**:

```sh
make test-lib
```

`make no-app-import` is what keeps that true: `fyne.io/fyne/v2/app` may be
imported only from `cmd/`, and from the root package's compile-only godoc
`Example`, which exists to show a host calling `app.New()`.

## Status

**Early implementation**, pinned to a pseudo-version of idunn and to Fyne 2.8.1.
idunn's own API is not stable yet, and neither is this. It renders an update and
asks one question; it does not schedule checks, live in a tray, or show release
notes — a host owns all three, and each would be a policy decision this module
has no business making.

Not implemented, deliberately: key generation or handling of any kind, elevation
UI beyond classifying the outcome (idunn has no interactive elevator on POSIX),
release notes or download sizes (a descriptor carries none), internationalisation,
repository management beyond one `publish`.

## Contributing

This repository inherits idunn's [`AGENTS.md`](https://github.com/go-idavoll/idunn/blob/main/AGENTS.md)
and [`CONTRIBUTING.md`](https://github.com/go-idavoll/idunn/blob/main/CONTRIBUTING.md) —
they bind every contributor here too, human or AI. Before pushing:

```sh
make fmt-check && make vet && make lint && make license && make test
```

## License

Apache-2.0 — see [`LICENSE`](LICENSE), matching idunn itself. Every source file
carries the header and `make license` enforces it.
