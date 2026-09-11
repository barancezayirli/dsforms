# Contributing

Thanks for looking. This is a small project with a strong opinion about how
changes get made, and it is easier if that is stated up front rather than
discovered in review.

## Before writing code

**Open an issue first for anything beyond a bug fix.** dsforms is deliberately
narrow — a form backend that stays a single binary with a single SQLite file.
Features that widen that surface are more likely to be declined than a bad
patch is, and it is nobody's idea of fun to hear that after the work is done.

Good first contributions: a bug with a reproduction, a documentation gap you
hit while deploying, a test for a path that has none.

## The working agreement

[`AGENT.md`](AGENT.md) is the real document. It covers the architecture and its
dependency rules, the SQLite and HTTP conventions, and the testing standard.
It is longer than most contributing guides because it is written for people —
and models — doing the work, not as a formality.

The parts that most affect a pull request:

**Tests come first, and they must be watched failing.** Not as ceremony: a test
written after the fix frequently asserts nothing, and the only way to know is to
run it against the broken code and see it fail with the right message. If you
fix a bug, the reproduction goes in first.

**Assert the behaviour, not its absence.** A test checking that a row is missing
from the inbox passes whether the submission was quarantined or destroyed. Ask
of every assertion what regression would still slip past it.

**Close the mechanism, not the reported case.** If a redirect bypass arrives via
one URL, the fix and its test should cover the shape, not that string. Most
findings in this repo have had a second member of the family behind them.

**One concern per commit**, and a message that explains why rather than what.
The diff already says what.

## Running it

```bash
go test -race -count=1 ./... && go vet ./... && gofmt -l .
```

Then run it. A green suite is necessary and not sufficient here — a drawer that
needed five presses of Back to close, a stale WAL figure and an open redirect
all shipped past a green suite and were found by using the thing.

```bash
docker compose up --build     # http://localhost:8080, mail at :8025
```

## Pull requests

Small and focused travels fastest. If a change touches more than a couple of
packages, say why in the description. Expect review comments about test
strength rather than style — `gofmt` settles style, so there is nothing to
argue about.

## Licence

dsforms is licensed under the **GNU AGPL-3.0**. By contributing you agree that
your contribution is licensed under the same terms. If you have a reason that
does not work for you, raise it in an issue before you start.
