# RunnerScout contributor instructions

Read README.md, docs/architecture.md, docs/operations.md and docs/releases.md.
Use focused changes and run `make verify`; generated API changes also require
`make verify-generated`. Never weaken required checks to obtain a pass.

Full development, harness/product construction, repository configuration, CI,
commits, pushes, issues and PRs in tsouza/runnerscout are authorized. Use the account-scoped `gh-tsouza` wrapper for every GitHub operation.
Sandbox and automatic approval review remain enabled. Production deployment,
unrelated repository writes and unbounded paid cloud execution are excluded.
No paid cloud test budget has been allocated.

Preserve settled behavior and uncertainty. Keep handoffs, decision ledgers,
progress reports and raw evidence in ignored `work/` or `evidence/`, never in
release source. Existing private history is under `work/private-history/`.
Publish concise user and contributor documentation; track unfinished work in
GitHub issues. Do not mistake fixture tests for live-cloud qualification or
editable CI for independent evaluator custody.

Immediately before cutting any release, run a full critical review on the
final release candidate, after implementation is complete. Review
DRY, KISS, inconsistencies, illogical reasoning, unjustified deferrals,
contradictions and tests that can pass without proving behavior. Resolve findings
and re-review the final candidate. Keep review evidence outside release source.
