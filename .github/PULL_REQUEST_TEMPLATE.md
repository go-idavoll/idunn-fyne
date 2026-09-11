<!-- idunn's AGENTS.md and CONTRIBUTING.md bind this repository too. Describe the
     security impact explicitly, even if it is "none". -->

## What & why

<!-- What changes, and why. Reference the issue you are addressing. -->

Closes #

## Security impact

<!-- Required. "None" is a valid answer — saying it tells reviewers you considered it. -->

- What this changes about how the sidecar handles untrusted input or key references:
- Touches the packer subprocess, its environment, or anything that could read a
  signing key? yes / no
  <!-- If yes: human maintainer sign-off is required and this does not auto-merge. -->

## Checklist

- [ ] Fail closed: an ambiguous or partial result is surfaced, never smoothed over.
- [ ] No key material is read, written, generated, logged, or held in memory.
- [ ] No parallel trust path; verification stays inside `core/trust` and go-tuf.
- [ ] `hook.Observer.OnEvent` still cannot block the updater goroutine and cannot panic.
- [ ] Errors stay classifiable: wrapped with `%w`, matched with `errors.Is`.
- [ ] Tests added; the library packages still build and test with no OpenGL and no display.
- [ ] `make test`, `make fmt-check`, `make vet`, `make lint`, and `make license` are green locally.
- [ ] New third-party dependency justified above (every dependency is attack surface).
