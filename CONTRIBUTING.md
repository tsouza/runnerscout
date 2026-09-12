# Contributing

Use a focused branch and pull request. Describe the behavior, its contract and checks.
Run `make verify`. Preserve failures and distinguish fixtures from live integration.
Commits use conventional prefixes (feat, fix, test, docs, chore). Squash merges keep
one reviewable change; the maintainer handles solo review without a fictitious reviewer.
Semantic versioning applies to releases. Pre-1.0 releases remain experimental.
Tags use vMAJOR.MINOR.PATCH and are created only after the release matrix passes.
Never commit credentials, raw conversation history, private logs or provider state.
