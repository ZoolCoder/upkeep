# Changelog

Notable changes, newest first. Versions follow [semver](https://semver.org);
before 1.0 a minor bump may change the config format, and any that does says so
here with what to edit.

## 0.2.0

### Surfaces

**Cloudflare Workers** — for a backend that is a Worker rather than a container.
Manages the two things about one that break quietly: a secret the script expects
and the account does not have, and a route it should answer on and does not. The
worst case is a route that *exists* and points at another script, because then
nothing looks missing; that one names who is answering.

**Fly.io** — the second hosting provider, and the test of whether the provider
seam was a seam. It shares no code with Render, only an interface. Fly never
returns a secret's value, so `upkeep` reports one missing and can set it, but
stays silent about one that exists rather than claiming a comparison it did not
make.

**Cloudflare DNS** — the handful of records a site needs to exist. It never
plans a deletion: a DNS record is the one thing here that can take a site off
the internet by being removed.

**Auth** — the one surface that checks variables against each other rather than
against a provider, because that is where authentication fails. It fetches the
JWKS to confirm it actually serves keys, probes the session endpoint for JSON
rather than a 404, and checks the frontend and backend name the same provider.
It changes nothing: a wrong automatic fix here locks out everybody.

### Commands

**`report`** — a scheduled run's summary, splitting what `upkeep` can still fix
from what waits on a human. A quiet run prints one line.

**`status`** — what is live right now, without diffing against a config. Names
which declared variables are absent, which is the one question a dashboard does
not answer.

### Behaviour

- `valueFrom` gained `aws://` and `gcp://` alongside `op://`.
  `aws://secret#field` pulls one value out of a JSON secret.
- Structural diffs: `+header cache-control`, not the whole desired rule.
- Apps are read concurrently, with output in config order and a deterministic
  first failure.
- `plan -exit-code` now separates what CI can fix (2) from what it cannot (3).
  A gate that fails forever on something no tool can do is a gate people turn
  off.
- Credential paths on Windows.

### Fixed

- **`plan -exit-code` could never go green.** Any non-empty plan exited 2,
  including one whose every line was `MANUAL`. Found by running against real
  infrastructure rather than a fixture — the fixture had both kinds in it and
  looked fine.
- **The env-var rules existed twice** once Fly arrived. They are one validator
  now; the copy that drifted would have been the one that let a credential into
  a committed file.

### Tests

`String()` was at 0% on every API client — the method between an accidental
`%v` and a credential in a log. Covered on all four, with the error paths every
other package depends on. Coverage 49.3% → 77.6%.

## 0.1.0

First release.

Reconciles the cloud footprint of a website or SaaS app from one file: Render
services and deploys, Cloudflare R2 and Pages, Neon branches.

Three deliberate departures from Terraform, each with its cost written down
beside its benefit:

- **No state.** It never deletes by omission, so a mistake in a config file
  cannot take a service down. Removing a key from the config does not remove it
  from the service.
- **A fourth verb.** `MANUAL`, for changes no tool can make — an R2 API token
  cannot be minted from any CLI, and real bank details are nobody's to invent.
- **`apply` verifies.** It re-reads afterwards, because a provider returning 200
  is not evidence a change took.

Credentials are borrowed from `render`, `wrangler` and `neonctl`'s own sessions.
The config is meant to be committed: a literal under a credential-shaped name is
refused at parse time.
