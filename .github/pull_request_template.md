## What changed

<!-- One paragraph. Why, not what: the diff already says what. -->

## Checklist

- [ ] `make verify` passes locally
- [ ] New behaviour is covered by a test that would fail without the change
- [ ] No test was skipped, deleted or weakened to make a gate pass
- [ ] Error paths return problem documents with no infrastructure detail
- [ ] Concurrency changes ran under `-race`
- [ ] Migrations run clean from an empty database and reverse cleanly
- [ ] Documentation or an ADR updated when a decision changed
