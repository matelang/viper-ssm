# viper-ssm

[![Go Reference](https://pkg.go.dev/badge/github.com/matelang/viper-ssm.svg)](https://pkg.go.dev/github.com/matelang/viper-ssm)
[![CI](https://github.com/matelang/viper-ssm/actions/workflows/ci.yml/badge.svg)](https://github.com/matelang/viper-ssm/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/matelang/viper-ssm)](https://goreportcard.com/report/github.com/matelang/viper-ssm)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

AWS SSM Parameter Store as a first-class [Viper](https://github.com/spf13/viper)
remote provider.

Viper reads remote configuration from etcd, Consul, Firestore and NATS.
Parameter Store is not on that list. See
[spf13/viper#877](https://github.com/spf13/viper/issues/877).

So teams read the parameters themselves and hand Viper a document. That works,
and it costs two things. `viper.WatchRemoteConfig` stops working. SSM's
per-parameter version is thrown away, so a running process cannot prove its
configuration is current.

This package registers `"ssm"` with Viper properly. Watching works, and versions
survive.

## Install

```sh
go get github.com/matelang/viper-ssm
```

## Usage

```go
import (
    "github.com/spf13/viper"
    viperssm "github.com/matelang/viper-ssm"
)

if err := viperssm.Install(); err != nil {
    return err
}

v := viper.New()
v.SetConfigType("json")   // REQUIRED. See below.
if err := v.AddRemoteProvider("ssm", "eu-central-1", "/myapp/prod"); err != nil {
    return err
}
if err := v.ReadRemoteConfig(); err != nil {
    return err
}

v.GetString("database.url")   // from /myapp/prod/database/url
```

### `SetConfigType("json")` is not optional

Viper parses a remote document with the configured config type. This provider
returns JSON. Leave the line out and Viper gives you an empty configuration and
no error. It is the likeliest way to misuse this package.

### The endpoint is the region, and it cannot be empty

`viper.AddRemoteProvider` takes a provider, an endpoint and a path. Viper's
`RemoteProvider` carries four strings and nothing else. So this package reads the
endpoint as the **AWS region**, and the path as the **parameter path**.

An endpoint that contains `://` is a URL. This package then uses it as the SSM
endpoint. A VPC endpoint or a local emulator needs that:

```go
v.AddRemoteProvider("ssm", "https://vpce-0123.ssm.eu-central-1.vpce.amazonaws.com", "/myapp/prod")
v.AddRemoteProvider("ssm", "http://localhost:4566", "/myapp/prod")   // emulator
```

**Do not pass an empty endpoint.** Viper drops a provider whose endpoint is
empty, and still returns `nil`. `ReadRemoteConfig` then reports `No Remote
Providers`. That is Viper's behaviour, not this package's. Pass a region. To use
the AWS SDK's own region resolution instead, skip Viper's registration and call
`Get` on a `*Provider` from `New`.

`Versions` takes a parameter path and no endpoint. Point it at one with
`WithEndpoint`. A URL passed to `WithRegion` is an error that names the right
option, rather than an `invalid input region` from deep inside the AWS SDK:

```go
viperssm.Install(
    viperssm.WithRegion("eu-central-1"),            // what the client signs for
    viperssm.WithEndpoint("http://localhost:4566"), // where the client connects
)
```

Set anything richer once, at install time:

```go
viperssm.Install(
    viperssm.WithRegion("eu-central-1"),
    viperssm.WithPollInterval(30*time.Second),
    viperssm.WithTimeout(10*time.Second),
    viperssm.WithRecursive(true),      // default
    viperssm.WithDecryption(true),     // default
    viperssm.WithRequireNonEmpty(),    // an empty path becomes an error
    viperssm.WithLogger(logger),
)
```

### Keys

Strip the parameter path, then turn `/` into nesting. Under path `/myapp/prod`:

| Parameter | Type | Configuration key | Value |
|---|---|---|---|
| `/myapp/prod/database/url` | `String` | `database.url` | `"postgres://…"` |
| `/myapp/prod/database/password` | `SecureString` | `database.password` | decrypted string |
| `/myapp/prod/http/port` | `String` | `http.port` | `"8080"`, and `GetInt` gives `8080` |
| `/myapp/prod/http/hosts` | `StringList` | `http.hosts` | JSON array, so `GetStringSlice` works |

Numbers and booleans stay strings. Viper's `GetInt` and `GetBool` already convert
them, and guessing here would only add surprises. A `StringList` splits on
commas. It is not trimmed, because that is how SSM defines the type.

Two layouts are errors, not a silent overwrite. Both name the two parameters
involved:

- `CollisionError`. A key is both a value and a path. For example
  `/myapp/prod/db` next to `/myapp/prod/db/host`.
- `DuplicateKeyError`. Two parameters map to one key. SSM parameter names are
  case-sensitive and Viper keys are not, so `/myapp/prod/DB/host` and
  `/myapp/prod/db/host` are two parameters and one key.

## Watching

```go
v.WatchRemoteConfigOnChannel()
```

The default interval is `viperssm.DefaultPollInterval`, one minute. Change it with
`WithPollInterval`. Each poll costs one `GetParametersByPath` call per page of ten
parameters. Choose the cadence on purpose.

**Change is decided by parameter version, never by comparing values.** SSM
versions rise by one per edit. So an edit that produces the same value still
counts, and no secret is ever held to diff it.

Two things to know about `WatchRemoteConfigOnChannel`. Both are Viper's, not this
package's:

- **It drops the stop channel.** A watch started through Viper runs for the life
  of the process. Call `WatchChannel` on a `*Provider` to keep a handle on it.
  Send on, or close, the channel it returns.
- **It writes Viper's key store from its own goroutine, with no
  synchronisation.** Reading configuration from another goroutine during a watch
  is a data race in Viper. Read your configuration into a struct once at boot, or
  use `WatchChannel` and apply the document yourself.

## Proving your configuration is current

```go
loaded, err := viperssm.Versions(ctx, "/myapp/prod")   // record at boot
// ... later ...
live, err := viperssm.Versions(ctx, "/myapp/prod")     // compare
```

SSM returns a version per parameter, and it rises by one per edit. So comparing
versions is an exact staleness verdict. `Versions` never asks SSM to decrypt. It
needs no `kms:Decrypt` permission, and it reads, holds and compares no secret.

## Design notes

- **Installing is additive.** Viper holds exactly one remote factory. `Install`
  keeps the factory already there, and hands it every provider string this one
  does not own. So etcd and SSM work in one program. Blank-import
  `github.com/spf13/viper/remote` **before** `Install`, not after.
- **Values never reach logs.** Parameter names, types and versions do. Values do
  not, in any log line or error message. A test proves it against a sentinel.
- **A missing path is empty, not an error. A permissions failure is an error.**
  Conflate the two and a configuration outage looks like an empty configuration.
  `WithRequireNonEmpty` changes the first case when you know the path must exist.
- **Retry is the AWS SDK's job.** `GetParametersByPath` is rate-limited, and the
  standard retryer applies. This package adds no retry of its own.
- **The SSM client is one interface with one method.** Pass a fake through
  `WithClient` and your tests need no network and no AWS account. This package's
  own unit tests do exactly that.

## Testing

Unit tests need no network and no AWS account:

```sh
make check          # tidy, vet, lint, tests with -race
```

A second tier reads and writes real parameters against
[Floci](https://hub.docker.com/r/floci/floci), an AWS-compatible emulator. It sits
behind the `integration` build tag, and it skips when the emulator is down:

```sh
docker compose up -d floci      # or any Floci on :4566
make test-integration
```

CI runs that tier as a service container with `FLOCI_REQUIRED=1`. A broken
emulator then fails the build. Without it, a green job could have tested nothing.

The emulator cannot check three things, so the fake still does. It ignores
`MaxResults` and answers in one page, so it cannot prove pagination. It returns
`SecureString` values in plaintext whatever `WithDecryption` says. It has no
access control to deny.

## IAM

```json
{
  "Effect": "Allow",
  "Action": "ssm:GetParametersByPath",
  "Resource": "arn:aws:ssm:eu-central-1:111122223333:parameter/myapp/prod/*"
}
```

Add `kms:Decrypt` on the key of any `SecureString`. `Versions` does not need it.

## Requirements

Go 1.24 or newer. Two dependencies: `spf13/viper` and `aws-sdk-go-v2` (`config`
and `service/ssm`). Nothing else.

Go 1.24 rather than Viper's own floor of 1.23, and not by choice.
`aws-sdk-go-v2/service/ssm` has declared `go 1.24` since v1.70.0. Nobody can read
Parameter Store on Go 1.23, whatever this module says.

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md). It records what Viper's extension point
permits, found by reading Viper's source rather than its documentation. Those
constraints are not preferences. A pull request that works around one is probably
solving a different problem.

## License

MIT. Same as Viper.
