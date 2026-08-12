# Contributing

Thank you for looking. This is a small package with a narrow job. So the bar for
"in scope" is high on purpose, and the bar for "help wanted" is low.

## What Viper permits

Three facts shape this package. All three come from reading Viper's source, not
its documentation:

- **Viper holds one remote factory**, in `viper.RemoteConfig`. So installing must
  delegate to whatever is already there, never replace it.
- **`viper.RemoteProvider` carries four strings and no context.** So per-provider
  configuration has nowhere to live. Anything richer is an install-time option.
- **`Get` returns a document, not a map.** So callers must call
  `SetConfigType("json")`, and the README has to say so loudly.

Those are constraints, not preferences. A change that works around one is probably
solving a different problem, and the review will say so.

## Getting set up

You need Go 1.24 or newer, and [golangci-lint](https://golangci-lint.run) v2.

```sh
git clone https://github.com/matelang/viper-ssm
cd viper-ssm
make check     # tidy, vet, lint, tests with -race
```

`make` on its own lists the targets. Unit tests need no network and no AWS
account. The SSM client is an interface with one method, and the tests pass a
fake.

A second tier reads and writes real parameters against
[Floci](https://hub.docker.com/r/floci/floci), an AWS-compatible emulator. It sits
behind the `integration` build tag, and it skips when the emulator is down:

```sh
docker compose up -d floci     # or any Floci on :4566
make test-integration
```

CI runs that tier as a service container with `FLOCI_REQUIRED=1`. An emulator that
does not answer then fails the build. Keep it that way. A green job that tested
nothing is worse than a red one.

The emulator cannot check three things, so the fake still does. It ignores
`MaxResults`, so it cannot prove pagination. It returns `SecureString` values in
plaintext. It has no access control to deny. A test that needs one of those
belongs in the unit tier.

## What a good change looks like

- **Tests come with it.** `make test` runs with `-race`. CI runs on the oldest
  supported Go and on the current one.
- **No new dependency.** This package depends on `spf13/viper` and
  `aws-sdk-go-v2`. Nothing else, including for tests and for logging. A pull
  request that adds one has to argue for it in the description.
- **No parameter value in a log line or an error message.** Names, types and
  versions are fine. Values are not, anywhere.
  `TestValuesNeverReachLogsOrErrors` proves it against a sentinel. Add a case
  when you add a code path that handles a value.
- **Silence is a bug.** A missing parameter path is an empty document on purpose.
  Anything else this package cannot represent is a named error that identifies
  both parameters. Never add a code path that drops configuration quietly.
- **Prose in the same voice.** [`CLAUDE.md`](CLAUDE.md) has the rules and the word
  list. Short sentences, active voice, the point first.

## Commit messages and pull requests

Commits follow [Conventional Commits](https://www.conventionalcommits.org):
`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`. The body says why. The
diff already says what.

Keep a pull request to one idea. Say what you changed, why, and what you tested.
If it changes the public API, say what it breaks and how a caller migrates.

## Reporting a bug

Open an issue with the Go version, the module version, and the smallest parameter
layout that reproduces it.

**Never paste a parameter value.** Not even a redacted one. Parameter names, types
and versions diagnose almost everything here.

For anything with a security impact, read [`SECURITY.md`](SECURITY.md) instead of
opening an issue.

## Out of scope

- **Writing parameters.** This is a read-only configuration source.
- **Secrets Manager and AppConfig.** Plausible later, behind the same factory. Not
  now.
- **Our own retry.** `GetParametersByPath` is rate-limited, and the AWS SDK's
  standard retryer already handles it.
- **Our own credential handling or region resolution.** The AWS SDK does both
  better.
- **A fork of Viper.** If the extension point cannot carry a requirement, the
  requirement changes.
