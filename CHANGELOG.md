# Changelog

This file records every notable change.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

First implementation. Nothing is released yet, so nothing here changes a previous
version.

### Added

- `Install` registers `"ssm"` as a Viper remote provider. So
  `viper.AddRemoteProvider("ssm", region, path)` works. Installing is additive:
  Viper holds exactly one remote factory, so `Install` keeps the factory already
  there and hands it every provider string this one does not own.
- `Get` reads every parameter under a path through `GetParametersByPath`. It
  follows pagination to the end. It returns a JSON document whose nesting mirrors
  the parameter names below the path.
- `Watch` re-reads on demand, for `viper.WatchRemoteConfig`.
- `WatchChannel` polls, and emits only when something changed. Parameter version
  decides change, never a comparison of values.
- `Versions` returns the SSM version of every parameter under a path. A process
  can record what it booted with, and later prove staleness in one call per page.
  It never asks SSM to decrypt, so it needs no `kms:Decrypt` permission and holds
  no plaintext.
- `New` builds a `*Provider` without touching Viper's globals, for callers who
  would rather avoid package-level state.
- Options: `WithRegion`, `WithEndpoint`, `WithClient`, `WithDecryption`,
  `WithRecursive`, `WithPollInterval`, `WithTimeout`, `WithRequireNonEmpty`,
  `WithLogger`.
- Named errors that identify both parameters involved. `CollisionError` for a key
  that is both a value and a path. `DuplicateKeyError` for two parameters that map
  to one Viper key. Also `InvalidNameError`, `EmptyPrefixError`,
  `UnhandledProviderError`, `ErrEmptyPath` and `ErrNotInstalled`.
- An integration tier behind the `integration` build tag. It reads and writes real
  parameters against Floci, an AWS-compatible emulator. CI runs it as a service
  container with `FLOCI_REQUIRED=1`, so a broken emulator fails the build rather
  than skipping quietly.

### Notes

- Callers must call `SetConfigType("json")`. Viper parses a remote document with
  the configured config type. Without that line the result is an empty
  configuration and no error.
- A parameter path that holds nothing is an empty document and no error. A
  permissions failure is an error. `WithRequireNonEmpty` turns the first case into
  an error for callers who know the path must exist.
- Parameter names, types and versions appear in logs and errors. Values do not,
  anywhere.
- An endpoint that contains `://` is the SSM endpoint URL. Anything else is a
  region name. `WithEndpoint` sets one for `Versions`, which takes a parameter path
  and no endpoint of its own.
- The go directive is `1.24.0`, not Viper's own floor of `1.23.0`.
  `aws-sdk-go-v2/service/ssm` has declared `go 1.24` since v1.70.0. Nobody can read
  Parameter Store on Go 1.23, whatever this module says.

[Unreleased]: https://github.com/matelang/viper-ssm/commits/main
