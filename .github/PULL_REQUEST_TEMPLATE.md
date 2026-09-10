## What this changes, and why

<!-- The diff says what. This is for why — the problem it solves, and the
     reasoning behind the approach if there was a choice to make. -->

## How it was verified

<!-- Both halves matter here. A green suite has not been sufficient in this
     repo: an open redirect, a stale database size and a drawer that needed five
     presses of Back all shipped past one. -->

- [ ] `go test -race -count=1 ./... && go vet ./... && gofmt -l .`
- [ ] Ran it (say what you did, and what you saw)

## Tests

<!-- If this fixes a bug, the reproduction should have been written first and
     watched failing. Say what you saw it fail with. -->

- [ ] New tests fail against the unfixed code
- [ ] Assertions cover the shape of the problem, not only the reported case

## Anything a reviewer should push on

<!-- The part you are least sure about. Naming it gets you a better review than
     leaving it to be found. -->
