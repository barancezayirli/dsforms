# Security

dsforms receives untrusted input from the public internet by design: anyone who
can reach a form endpoint can post to it. Bugs in that path matter more than
their size suggests, and I would rather hear about one early than read about it
later.

## Reporting a vulnerability

**Please do not open a public issue.**

Use GitHub's private vulnerability reporting on this repository:
**[Report a vulnerability](https://github.com/barancezayirli/dsforms/security/advisories/new)**
— or go to the **Security** tab and choose *Report a vulnerability*. That opens
a private thread visible only to the maintainer.

Useful things to include, in rough order of value:

- What an attacker gains — reading other people's submissions, forging a
  session, reaching another origin, taking the instance down.
- The smallest request or page that demonstrates it.
- The version or commit you tested, and whether you ran the Docker image or
  built from source.

You will get a first response within **72 hours**. If a fix is warranted I will
tell you roughly when to expect it and credit you in the release notes unless
you would rather I did not.

## What is in scope

Anything reachable in a default deployment: the public submission endpoints
(`/f/{id}`, `/w/{id}`), the admin UI and its session handling, the spam and
filter-rule paths, backup export and restore, and the CLI.

## What is not

- Findings that need an attacker to already be an authenticated admin. An admin
  can already export the database; that is the product working.
- Missing hardening headers or rate limits on endpoints where their absence has
  no consequence you can demonstrate.
- Anything about a reverse proxy, TLS setup or host that sits in front of
  dsforms rather than in this repository.
- Scanner output with no working request behind it.

One deliberate design decision worth knowing before you report it: `ExtractIP`
trusts `X-Forwarded-For` unconditionally, because dsforms is meant to run behind
a proxy that sets it. That makes IP-based *allow* rules only as trustworthy as
that proxy, and it is recorded as an accepted risk in `SESSION_PROGRESS.md`
rather than an oversight. If you can show it causing harm in a normal
deployment, that is a real report.

## Supported versions

The latest release. This is a young project; there is no long-term support
branch yet.
