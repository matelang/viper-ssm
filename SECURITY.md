# Security Policy

## Supported versions

The latest minor release gets fixes. This package is pre-1.0, so please upgrade
before you report.

## Reporting a vulnerability

Report privately, not in a public issue. Use
[GitHub's private vulnerability reporting](https://github.com/matelang/viper-ssm/security/advisories/new).
It reaches the maintainers, and nobody else can read it.

Include the module version, the Go version, and what an attacker gains. Expect a
first reply within seven days.

**Never include a parameter value.** Not in a report, not anywhere. Parameter
names, types and versions describe any problem in this package.

## What counts here

This package reads AWS SSM Parameter Store on Viper's behalf. It holds no
credentials. It decrypts nothing itself. It writes nothing. So the security
surface is narrow:

- **A parameter value in a log line or an error message.** Values must not appear
  in either, ever. This is the one most worth reporting. A test asserts it, so a
  way past that test is a bug.
- **A configuration outage that looks like an empty configuration.** A missing
  parameter path is an empty document on purpose. A *permissions* failure that
  came back as an empty document would be different. An attacker who removes an
  IAM permission could then drop a security setting quietly.
- **A dropped or overwritten key.** A parameter that is both a value and a path is
  a loud error. So are two parameters that map to one key. A document that reaches
  Viper having lost a key is a bug for the same reason.
- **Anything that widens the AWS surface.** Reading parameters outside the
  configured path, for example.

## What does not count

- Missing or over-broad IAM permissions in your own account. Scope the role to the
  parameter path. This package cannot do it for you.
- Credentials in your environment or on disk. The AWS SDK handles credentials, on
  purpose.
- The configuration document holding parameter values. That is what it is for.
