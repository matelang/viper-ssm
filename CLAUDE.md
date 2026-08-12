# CLAUDE.md

## Writing style

Write in ASD-STE100, or Simplified Technical English. Follow Zinsser's four
principles: simplicity, brevity, clarity, humanity.

The rules:

- Use short sentences. One idea per sentence.
- Use the active voice.
- Give each word one meaning. Use the same word for the same thing every time.
- Put the point first. Give the reason after.
- Cut every word that does no work.
- Keep it warm and human. A person wrote it, not a manual.

Avoid long subordinate clauses. Avoid hedging. Avoid three claims in one
sentence. Avoid turning verbs into nouns. Write "review", not "do a review of".

This applies everywhere: code comments, commit messages, docs, PR descriptions,
issue templates, and error messages.

**One caution.** Strict ASD-STE100 was built for aircraft maintenance manuals. It
bans most words outside a controlled vocabulary. Applied literally it makes prose
stilted, which fights the fourth principle. So take the discipline of STE, and
let humanity choose the words. Plain and warm beats correct and cold.

### Words this project fixes

Use the same term every time. Pick from the left, never the right.

| Use | Not |
|---|---|
| parameter path | prefix, path prefix, parameter prefix |
| parameter | param, key-value, entry |
| configuration key | viper key, config key |
| document | payload, blob, body |
| read | fetch, retrieve, pull |
| the AWS SDK | the SDK, aws-sdk-go-v2 (in prose) |

Code keeps its own names. `EmptyPrefixError` and its `Prefix` field stay as they
are; the prose around them says "parameter path".

## Project

A Viper remote provider for AWS SSM Parameter Store. `CONTRIBUTING.md` records
what Viper's extension point permits. Read it before changing the public API.

```sh
make check              # tidy, vet, lint, tests with -race
make test-integration   # the Floci tier; skips when the emulator is down
```

Two rules the tests enforce, so keep them true:

- No parameter value reaches a log line or an error message. Names, types and
  versions may.
- Nothing drops configuration quietly. A layout this package cannot represent is
  a named error.
