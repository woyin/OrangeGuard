# Changelog

## 0.3.0

- Fix false model-substitution alarms when both request and response carry different routing/provider namespaces, e.g. `cline-pass/deepseek-v4.1-flash` → `deepseek/deepseek-v4.1-flash`.
- Preserve model version and tier distinctions (`pro`/`flash`, `mini`/`nano`, `air`/`airx`, etc.); do not infer model quality or permanent mappings for rolling aliases.
- Restrict legacy alias-prefix tolerance to recognizable model families; reject fragment identities such as `gpt-5-mini` → `mini` and empty snapshot suffixes.
- Enable OpenAI, DeepSeek and GLM guard rules by default, including namespaced requests. Explicit rule lists still replace defaults; `models: []` still disables guarding.
- Apply unqualified expect/deny globs to model IDs inside namespaces, with deny taking precedence; qualified globs remain literal.
- Keep execution and passive-monitor identity matching consistent, and update management-page guard badges/default-rule presets.
- Add provider response-field, identity, non-stream and stream regression coverage; document protocol sources, missing-model behavior, dynamic aliases and the limits of reported-model evidence.
- Set the default local release build version to `0.3.0`.

Existing explicit configurations are not automatically merged with new default rules. Review namespace-aware unqualified globs when upgrading. Retry defaults remain three extra attempts at 200ms intervals; missing model remains fail-open (`accept`).
