# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for a security problem.

Use GitHub's private reporting — [Security → Report a
vulnerability](https://github.com/ZoolCoder/upkeep/security/advisories/new) — or
email the maintainer listed on the repository profile.

You will get an acknowledgement within a few days. This is a small project with
one maintainer, so please be patient; if a fix takes longer than that, you will
be told where it stands rather than left waiting.

## What is in scope

`upkeep` holds cloud credentials in memory and writes changes to production
infrastructure, so the interesting failures are about disclosure and about doing
something the operator did not review.

Especially in scope:

* **A value reaching an output.** A plan, `-json`, a saved plan, an error, or an
  `import` that prints or writes a secret. Every one of these is meant to name
  where a value came from and never the value itself.
* **A reference escaping into a subprocess.** `valueFrom` references are
  untrusted input; commands run without a shell and references are validated
  before anything runs. A way around either is a vulnerability.
* **Applying something that was not planned.** A saved plan that applies after
  the world moved, or an action executing that the plan did not list.
* **A `MANUAL` action executing.** Those must never have a `Do`.
* **Credential handling.** Anything that writes a borrowed CLI session to disk,
  logs a token, or leaks one through an error or a `%v`.

## What is not in scope

* **A hostile config file.** A config can name any service or account you have
  access to, and `valueEnv` can name any variable in your environment. Review a
  config you did not write, as you would any script. This is documented, not a
  bug.
* **A mis-named value.** The classifier that refuses a literal under a
  credential-shaped name reads names, not contents. It will not catch a secret
  in a variable called `SETTINGS_BLOB`. Documented limitation.
* **The access a borrowed session already has.** `upkeep` trusts what
  `render login`, `wrangler login` and `neonctl auth` stored, and has exactly
  their permissions.
* **Anything requiring an attacker to already have your shell.**

## Supported versions

The latest release. This project is young enough that backporting to older tags
is not sensible yet; if that changes, this section will say so.

## What you can expect from a fix

A security fix gets a release of its own, notes saying what was exposed and
under what conditions, and a test that fails without the fix. If the problem was
a disclosure, the notes say plainly what could have been disclosed rather than
describing it as hardening.
