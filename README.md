# upkeep

[![ci](https://github.com/ZoolCoder/upkeep/actions/workflows/ci.yml/badge.svg)](https://github.com/ZoolCoder/upkeep/actions/workflows/ci.yml)
[![go reference](https://pkg.go.dev/badge/github.com/zoolcoder/upkeep.svg)](https://pkg.go.dev/github.com/zoolcoder/upkeep)
[![licence: MIT](https://img.shields.io/badge/licence-MIT-blue.svg)](LICENSE)

One file describes what an app's cloud footprint should be. `upkeep` reads
what it actually is, prints the difference, and — only if you ask — closes it.

```console
$ upkeep plan
MANUAL  render-env  FEE_ACCOUNT_INFO      real bank details, shown to a customer verbatim
MANUAL  render-env  R2_SECRET_ACCESS_KEY  set $R2_SECRET_ACCESS_KEY locally, then re-run
CREATE  render-env  R2_BUCKET             not set on the service → my-bucket
UPDATE  r2-cors     my-bucket             no CORS rule at all, so a browser cannot upload

2 of these cannot be automated and are yours to do.
```

## Why it exists

The failure it was written after: an app shipped with none of its object-storage
variables set. Nothing errored. The service booted, served every page, and
answered `404` to one endpoint — photo upload — for days. Every layer reported
success, because from each layer's point of view nothing had gone wrong.

So `upkeep` has a fourth kind of action, `MANUAL`, for changes no tool can
make: an R2 API token cannot be minted from any CLI, and real bank details are
nobody's to invent. A tool that could only automate would have reported that
same clean success. **Naming the gap is the feature.**

For the same reason `apply` re-reads afterwards. A provider returning `200` is
not evidence a change took.

## What it manages

| Surface | Reconciles | Reports only |
|---|---|---|
| Render | service environment, deploys | |
| Cloudflare R2 | bucket, public access, CORS | |
| Cloudflare Pages | production branch | domains it does not know about |
| Neon | | branch exists, `DATABASE_URL` agreement |

It never deletes by omission: a variable the config does not name is left
exactly as it is. Deleting a database or repointing a live service at another
one are not things a config file should do quietly, so it does not.

## Install

```console
go install github.com/zoolcoder/upkeep/cmd/upkeep@latest
```

## Credentials

Every provider reuses the session its own CLI already stored, so on a machine
where `render`, `wrangler` and `neonctl` work, `upkeep` works too.

| Variable | Falls back to |
|---|---|
| `RENDER_API_KEY` | `~/.render/cli.yaml` after `render login` |
| `CLOUDFLARE_API_TOKEN` | wrangler's config after `wrangler login` |
| `NEON_API_KEY` | `~/.config/neonctl/credentials.json` after `neonctl auth` |

A surface whose credential is missing is *reported*, never skipped silently. An
expired borrowed session says which command to re-run, rather than failing as a
`401` that reads like a permissions problem.

## Getting a config

Do not write one by hand — that is how a config ends up describing an app that
does not exist. Read it off the account:

```console
$ upkeep import -name my-app -render srv-… -bucket my-bucket -pages my-app
```

`import` and `plan` compose, so you can check a config you have not saved:

```console
$ upkeep import -name my-app -bucket my-bucket \
  | { echo "version: 1"; echo "apps:"; cat; } \
  | upkeep plan -config -
```

See [`upkeep.example.yaml`](upkeep.example.yaml) for the full shape.

## Secrets

**The config file is meant to be committed. Nothing secret goes in it.**

A variable either carries a literal value, or names the local environment
variable its value comes from:

```yaml
- key: R2_BUCKET             # not secret
  value: my-bucket
- key: R2_SECRET_ACCESS_KEY  # secret: resolved at apply time, never written
  valueEnv: R2_SECRET_ACCESS_KEY
- key: FEE_ACCOUNT_INFO      # nobody's to invent
  manual: true
  why: real bank details, set by hand on the service
```

Three things enforce that, rather than trusting you to remember:

1. **A literal under a credential-shaped name is refused at parse time.** Write
   `value:` under a key containing `SECRET`, `TOKEN`, `PASSWORD`, `API_KEY`,
   `DATABASE_URL` and friends and the config does not load, with an error naming
   the field to use instead. The check is deliberately eager — a public value
   wrongly refused costs one line of config; a secret wrongly committed costs a
   rotation.
2. **`import` never copies a live secret out.** A variable whose name looks like
   a credential comes back as a `valueEnv` reference with no value, using the
   *same* rule as the validator, so an import always parses back in.
3. **No output ever prints a value.** A plan says
   `differs from the config (value from $R2_SECRET, not shown)`. `-json` follows
   the identical rule — a machine-readable view that leaked what the text view
   hides would be a way around the guarantee, not a second view of it.

### Threat model

`upkeep` holds credentials in memory for the length of one run and writes
none to disk. Every API client redacts its token in `String()`, so no accidental
`%v` can print one.

It does **not** protect against a hostile config file: a config can point at any
service id or account you have access to, and `valueEnv` can name any variable
in your environment. Review a config you did not write, the same as any script.

The heuristic that classifies a name as secret is a heuristic. It will not catch
a credential in a variable called `SETTINGS_BLOB`. Use `valueEnv` for anything
sensitive regardless of what it is called.

## Exit codes

| | |
|---|---|
| `0` | nothing to do, or the apply converged and was verified |
| `1` | something failed, or an applied change is still not in place |
| `2` | `plan -exit-code` only: the live state differs from the config |

So CI can gate on drift:

```console
$ upkeep plan -exit-code -json > plan.json
```

## Developing

```console
make test     # go test ./...
make lint     # go vet + gofmt check
make build    # ./upkeep
```

No network is needed to run the tests. Every provider is behind an interface and
every planner is tested against a fake, including one that accepts every write
and changes nothing — the failure `apply`'s verification exists to catch, and
one no real API will perform on request.

## Where it is going

See [CHANGELOG.md](CHANGELOG.md) for what has landed, and
[ROADMAP.md](ROADMAP.md) — including what has been considered and declined,
which is the more useful half.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Everything runs offline; CI has no
credentials configured on purpose, so a fork can be tested without being
trusted.

## Security

See [SECURITY.md](SECURITY.md) — including what is deliberately *not* in scope,
which is the more useful half.

## Licence

MIT. See [LICENSE](LICENSE).
