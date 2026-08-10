# Contributing

## Running everything

```console
make test    # go test ./...
make race    # the same, with the race detector
make lint    # gofmt check + go vet
make cover   # coverage across the whole suite
make build   # ./upkeep
```

No test touches a network. That is a rule, not an accident: you should be able
to work on `upkeep` with no cloud account, and a pull request from a fork should
be testable without being handed access to anything. CI runs with no credentials
configured, deliberately.

`internal/testfake` is a whole cloud on localhost — Render, Cloudflare and Neon
in the envelope shapes those APIs really use. Point the three base-URL variables
at it and `upkeep` cannot tell the difference:

```go
cloud := testfake.New()
defer cloud.Close()
cloud.Env["srv-000000000000000000"] = map[string]string{"APP_ENV": "production"}
```

## What a good change looks like

**Say what breaks, not what differs.** `no CORS rule at all, so a browser cannot
upload` sends someone to the right place; `cors mismatch` does not. Error and
plan text is most of this tool's value.

**Never print a value.** Not in a plan, not in `-json`, not in a saved plan, not
in an error. Say where the value comes from instead. There are tests that fail
if you do.

**Prefer reporting to guessing.** If `upkeep` cannot do something — a credential
only a dashboard can mint, a value only a human knows — that is `OpManual` with
a `why`, printed every run. A tool that silently skips what it cannot do is how
a half-built deployment passes for a finished one.

**Test the failure, not the success.** The interesting tests here are a provider
that accepts every write and changes nothing, a saved plan applied after the
world moved, an import that writes a file the validator refuses. Successes tend
to hold on their own.

## Adding a provider

See [Adding a provider](docs/modules/ROOT/pages/extending.adoc). Three edits and
a package, with four rules — the last of which is the one that matters.

## Docs

The manual is Antora:

```console
npx antora antora-playbook.yml && open build/site/index.html
```

It must build with no warnings. `${VAR}` in prose is an attribute reference —
wrap it in `+…+` or it renders as nothing.

## Commits

One change per commit, with a message saying what was wrong and why the fix is
the right one. If a test found the bug, say so — that is the useful part for
whoever reads it next.
