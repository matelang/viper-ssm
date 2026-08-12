<!-- Thank you. Keep this to one idea. Delete any section that does not apply. -->

## What this changes

<!-- One or two sentences. The diff says what. Say why. -->

## How it was tested

<!-- Which tests, and anything you checked by hand. -->

## Checklist

- [ ] `make check` passes: tidy, vet, lint, tests with `-race`
- [ ] Tests cover the change
- [ ] No new dependency, or the description argues for one
- [ ] No parameter value can reach a log line or an error message
- [ ] Nothing new drops configuration quietly. A layout this package cannot
      represent is a named error
- [ ] Public API changes are documented, and `CHANGELOG.md` is updated
- [ ] Prose follows `CLAUDE.md`: short sentences, active voice, the point first

<!--
CONTRIBUTING.md records what Viper's extension point permits. If this change
works around one of those constraints, say which one, and why.
-->
