## What

<!-- One or two sentences on the change and why. Link the issue if there is one. -->

## How it was verified

- [ ] `go test ./... -count=1` passes
- [ ] `golangci-lint run ./...` and `gofmt -l .` are clean
- [ ] For extraction or resolution changes: the relevant `bench/accuracy` gate or a new fixture covers it
- [ ] For user-facing changes: README / docs / CHANGELOG `Unreleased` updated

## Notes for the reviewer

<!-- Anything non-obvious: behavior changes, migration concerns, follow-ups. -->
