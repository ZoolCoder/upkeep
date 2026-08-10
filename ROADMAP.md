# Roadmap

What `upkeep` is for: the cloud footprint of a website or a SaaS product — the
service that runs it, the bucket that holds its uploads, the static site that
fronts it, the DNS that points at it, the database behind it. Not a general
infrastructure engine. If you need dependency graphs and VPCs, use Terraform;
the reasons are in [the comparison](docs/modules/ROOT/pages/comparison.adoc).

Everything below is ordered by how much it would have helped the failures that
produced this tool, not by how interesting it is to build.

## 0.1 — shipped

**Surfaces**

- [x] Render: service environment, deploys, waiting for a deploy to reach live
- [x] Cloudflare R2: bucket, public access, CORS
- [x] Cloudflare Pages: production branch, domain reporting
- [x] Cloudflare DNS: the records a site needs, never removing one it did not declare
- [x] Neon: branch exists, `DATABASE_URL` agreement
- [x] Auth: the variables checked against each other, and the keys actually fetched

**Commands**

- [x] `plan`, with `-json`, `-out` to save one, and `-exit-code` separating
      what CI can fix from what it cannot
- [x] `apply`, which re-reads afterwards to check the change took
- [x] `apply <saved-plan>` — apply exactly what you reviewed, or be told the world moved
- [x] `status` — what is live right now, without diffing
- [x] `validate` — offline, no network and no credentials
- [x] `import` — read a config off the account rather than writing one
- [x] `-config -`, so `import` and `plan` compose through a pipe

**Behaviour**

- [x] `MANUAL` actions for what no tool can do, printed every run until done
- [x] Credentials borrowed from `render`, `wrangler` and `neonctl`'s own sessions
- [x] `valueFrom: op://…` for values a team keeps in a vault
- [x] A literal under a credential-shaped name refused at parse time
- [x] Structural diffs — `+header cache-control`, not the whole desired rule
- [x] Concurrent reads, ordered output, deterministic first failure
- [x] macOS, Linux and Windows credential paths
- [x] A fake cloud, so the whole suite runs with no account anywhere

## 0.2 — next

**Shipping Pages assets.**
`upkeep` configures a Pages project but cannot upload a build to it, so a deploy
script still lives beside it. Closing that means implementing Cloudflare's
direct-upload protocol — hashing every asset, negotiating a manifest, uploading
what is missing — which `wrangler` already does well. Worth doing only if the
seam between the two turns out to cause real problems; shelling out to
`wrangler` is not obviously worse than reimplementing it.

**A second hosting provider.**
Render is one opinion about where a backend runs. Fly.io and Railway are the
common alternatives for this shape of product, and adding one is the real test
of whether the provider seam is a seam or just tidy code.

**Workers.**
Routes, bindings and secrets, for products whose backend is a Worker rather than
a container.

**A drift report worth scheduling.**
`plan -json` already fits CI. What is missing is something worth waking up to: a
summary across every app, manual items separated from actionable ones, so a
weekly run is a page you read rather than a log you skim.

**More resolvers.**
`valueFrom` has one scheme, `op://`. AWS Secrets Manager, Google Secret Manager
and Vault are each one small type behind the existing interface.

## Considered and declined

**State, and deleting what you removed.**
Terraform keeps state so that removing a resource from the config removes it
from the world. `upkeep` will not. A production service carries variables its own
platform set, a bucket carries settings nobody wrote down, and a zone carries
records that predate the config; managing only what is named means a mistake in
a config file cannot take a service down. The cost — removing a key from the
config does not remove it from the service — is real, and it is the price of
that guarantee.

**Locking.**
Follows from the above: with no state file there is nowhere natural for a lock.
Two people applying at once would both read, both write, and the second would
win. Worth revisiting if `upkeep` is ever run unattended by more than one thing
at a time.

**A plugin protocol.**
Providers are Go packages behind an interface. A plugin system would mean a
binary protocol, versioning, and a distribution story, to save a fork one
`go build`. See [adding a provider](docs/modules/ROOT/pages/extending.adoc).

**A DSL.**
The config is YAML with a few fields per variable. The moment it grows
conditionals it stops being reviewable, and being reviewable is the point.

**Arbitrary commands as value sources.**
`valueFrom: sh://…` would make every resolver unnecessary and every config a
script. Resolvers name a specific tool with a fixed argument shape instead, and
references are validated before anything runs.

## How to help

Everything runs offline: `make test` needs no cloud account, and CI deliberately
has no credentials configured, so a fork can be tested without being trusted.

The most useful contribution is a provider for a platform you actually deploy
to, with the four rules in
[adding a provider](docs/modules/ROOT/pages/extending.adoc) followed —
especially the last one. A provider that quietly does nothing when it cannot do
something is worse than no provider at all.
