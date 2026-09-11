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
else. This is the Fyne one, and the reference implementation for
[IDN-19](https://github.com/go-idavoll/idunn/blob/main/docs/backlog.md).

It contains:

| | |
|---|---|
| [`fyneui`](fyneui) | the sidecar: `hook.Observer` + `hook.Prompter`, a progress panel, and error wording |
| [`cmd/demo`](cmd/demo) | a host that runs a real update, so the interfaces are shown to be sufficient |
| [`cmd/packassist`](cmd/packassist) | a packing assistant: writes a `pack.yaml`, runs the idunn packer, resolves the result |

**What it is not.** It makes no trust decision. It verifies nothing, signs
nothing, and holds no key. Every verdict about what may be installed is
`core/trust` and go-tuf's, exactly as it is for a headless build.

## Requirements

Go 1.25 and cgo. Only `cmd/...` links Fyne's desktop driver, and only Linux needs
the headers spelled out:

```sh
make deps-linux   # libgl1-mesa-dev libxcursor-dev libxrandr-dev libxinerama-dev
                  # libxi-dev libxxf86vm-dev libwayland-dev libxkbcommon-dev
                  # wayland-protocols
```

macOS and Windows need nothing beyond their own toolchain. The library packages
build and test with **no OpenGL and no display at all**:

```sh
make test-lib
```

## Using the sidecar

```go
a := app.New()
win := a.NewWindow("Updater")

ui := fyneui.New(win)
defer ui.Close()
win.SetContent(ui.Widget())
ui.Start()          // from the Fyne goroutine, once the window exists

u, err := updater.New(updater.Options{
    Trust:   trustClient,
    FS:      fsx.OS(),
    Root:    "/opt/acme",
    Channel: "stable",
    Observe: ui,    // hook.Observer
    Prompt:  ui,    // hook.Prompter
})

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

## The threading contract

Three rules. They are not style advice; each one follows from something in
idunn's or Fyne's source, and getting one wrong breaks an update or freezes a
window.

**1. Never call `Apply` from the Fyne goroutine. Use `fyneui.Run`.**
`Prompter.Confirm` blocks by design, and the dialog it raises needs the Fyne
goroutine in order to be drawn. Calling `Apply` from a widget callback therefore
blocks the event loop inside `Confirm`, the dialog is queued behind the very
goroutine waiting for it, and the application freezes for good. Go cannot detect
which goroutine is the UI one, and Fyne's own check is under `internal/`, so this
cannot be refused — only survived. `Confirm` gives up after
`DefaultConfirmTimeout` with `ErrPrompt`, which turns a permanent freeze into a
five-second hitch and a named error.

**2. `OnEvent` runs on the updater's goroutine, inside an open transaction.**
It takes a mutex, copies the event, and makes one non-blocking send. It reaches
no Fyne symbol at all, because `fyne.Do` dereferences `fyne.CurrentApp`, which is
nil until the host starts its app — and a panic in an Observer is not recovered
by core, so it would take the process down mid-update. Do not put work in the
render callback.

**3. `Reset` before each transaction.** Two updates in one session are two
transactions. Without it the second is drawn on top of the first and the progress
bar appears to jump backwards as the next run starts at `PhaseCheck`.

## Progress is a step count, not a percentage

`hook.Event.Progress` is `-1` everywhere today — both emitters in idunn hardcode
it, and there is no byte-level or file-level progress anywhere in `core`. So
`fyneui.Fraction` derives a position from the phase, and the bar is labelled
`Installing`, never `63%`.

The phase order it uses is the order idunn **emits** them, which is not the order
`hook.Phase` declares them: `PhaseVerify` is emitted *after* `PhaseApply`,
`PhaseStage` is never emitted on success at all, and `PhaseQuiesce` follows
`PhaseDownload`. Driving a bar from the declaration order sends it backwards near
the end of every update that has `VerifyAfterApply` switched on.

The `Progress >= 0` branch is written and tested, so nothing here changes on the
day core starts reporting a real one.

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

## Status

**Early implementation**, pinned to a pseudo-version of idunn and to Fyne 2.8.1.
idunn's own API is not stable yet, and neither is this.

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
