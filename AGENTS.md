# AGENTS.md

## API Path Rules

- Runtime routes may expose both:
  - legacy unprefixed paths
  - `/kapis/costwise.wiztelemetry.io/v1alpha1/...` prefixed paths
- Prefer keeping backward-compatible runtime routes unless explicit removal is requested.
- The `/kapis/costwise.wiztelemetry.io/v1alpha1/...` route set is the canonical external API contract for frontend integration.
- If a handler serves both prefixed and unprefixed routes, treat the prefixed route as the canonical contract.

## Swagger Rules

- Swagger documentation must only expose `/kapis/costwise.wiztelemetry.io/v1alpha1/...` prefixed paths.
- Do not expose legacy unprefixed routes in `docs/swagger.json`.
- Any API change that adds, removes, or changes request/response behavior must update Swagger when applicable.
- Swagger is generated via:
  - `just swagger`
  - or `./tools/update-swagger.sh`
- The generated file `docs/swagger.json` must remain committed in the repository.
- The Swagger generation script must remain committed and aligned with the generated output.
- Swagger post-processing must preserve only prefixed paths.

## API Change Rules

- Any externally visible API change should update all relevant layers when applicable:
  - route registration
  - Swagger annotations
  - generated `docs/swagger.json`
  - route tests or handler tests
- Backward-compatible runtime aliases may be preserved, but the canonical documented contract must remain the prefixed route set.

## Aggregation Rules

- When adding new aggregate dimensions to allocation-derived endpoints, preserve existing cluster behavior unless explicit behavior changes are requested.
- Do not assume total or summary rows are simple averages of child rows.
- For aggregate endpoints, summary or total values must be recomputed from underlying aggregated inputs, not from displayed row percentages.

## Testing Rules

- Route additions should include route registration tests when practical.
- Changes to aggregation or summarization logic should include regression tests for:
  - single-group behavior
  - summary or total behavior
  - legacy-compatible behavior when required
- If runtime compatibility and Swagger exposure differ, test both concerns separately.

## Generated Artifact Rules

- Generated API artifacts committed to the repository must be reproducible from committed scripts.
- If a generated file is tracked in Git, the script or command path used to generate it must also be tracked in Git.
