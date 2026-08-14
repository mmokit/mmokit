## What and why

<!-- What changes, and the reason. The diff shows what; explain why. -->

## Linked issue

<!-- Required for substantial changes. Small obvious fixes can say "none". -->

## Checks run

<!-- Tick what you ran. Say plainly what you did NOT run and why — an honest
     gap is fine, an unverified claim is not. -->

- [ ] `go vet ./...`
- [ ] `gofmt -l $(git ls-files '*.go')` is empty
- [ ] `go test ./... -short -count=1 -p 1`
- [ ] `just lint-no-ark`
- [ ] Frontend suites, if touched (`just ts-core-test`, `just web-test`, `just admin-test`)
- [ ] PostgreSQL suites, if persistence was touched (`just db-up && just test-pg`)
- [ ] Regenerated any affected SDK or golden, and committed the result

Not run, and why:

## Notes for review

<!-- Anything you are unsure about, or a decision you would like challenged. -->
