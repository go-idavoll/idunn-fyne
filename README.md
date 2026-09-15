# idunn-fyne

**The Fyne UI sidecar for [idunn](https://github.com/go-idavoll/idunn).**

idunn is headless by default and `core` carries no UI dependency. A sidecar is the
opt-in half: a separate module that implements the two host hooks a user-facing
update needs — `hook.Observer` to render the lifecycle, `hook.Prompter` to ask the
one question — and nothing else. Fyne appears in a dependency graph only because a
host chose to import this (idunn `docs/design.md` §8).

```go
a := app.New()
ui := fyneui.New(a, "Acme")

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

Dropping the two lines gives back a headless update. Nothing else changes.

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

Run `go run ./cmd/idunn-fyne-demo` to watch a synthetic release go past. It installs
nothing, touches no install root and reaches no network.

## How it is built

The split is the point:

- **`Model`** (`progress.go`) turns the event stream into a `State`: the headline, the
  detail line, the fraction, the throughput estimate and what is left of it. It has no
  Fyne in it, so what a release's progress *means* is decided in one place and tested
  without a display.
- **`ProgressWindow`** (`window.go`) copies a `State` into widgets, and owns the
  threading. The updater calls `OnEvent` from its own goroutine; Fyne widgets may only
  be touched on the main one. `OnEvent` therefore does nothing but fold the event into
  the model under a mutex and hand a repaint to `fyne.Do` — and it **coalesces**, so a
  release reporting every megabyte produces one repaint per main-loop turn rather than
  a backlog of stale ones.

`Confirm` blocks the updater until the user answers, which is correct — there is
nothing for it to do until it has a decision — but honours the context, so an update
being torn down does not wait on a dialog nobody is looking at.

## Status

Early, and honest about it: this renders an update and asks one question. It does not
schedule checks, live in a tray, or show release notes — a host owns all three, and
each would be a policy decision this module has no business making.

## Building

Fyne needs a C toolchain and the platform's GL and windowing headers. On Debian or
Ubuntu:

```sh
sudo apt-get install gcc libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev
```

macOS needs Xcode command line tools; Windows needs a MinGW toolchain. See
[Fyne's own prerequisites](https://docs.fyne.io/started/) for the current list.

## License

MIT — see [LICENSE](LICENSE). idunn itself is Apache-2.0.
