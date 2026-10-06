# Changelog

## 0.6.0

- Add a models.dev reference capability directory and authenticated model-detail endpoint, preserving full request names and requiring explicit provider selection.
- Add per-member reference capability selection and use confirmed reference data when deriving combo capabilities. CPA-reported fields take precedence; refreshing references does not change saved combos.
- Refresh the directory asynchronously on plugin configuration and every 24 hours, with a manual management-page refresh button and refresh status endpoint.
- Persist valid data atomically to `plugins/orangeguard-models-cache.json` (override with `ORANGEGUARD_CATALOG_CACHE`). Retain last valid data on network/payload failure; embedded data is an initial fallback only.
- Bound network refreshes to 45 seconds and 32 MiB, reject unusable catalogs, and report network/cache-write failures.
- Add directory lookup, management endpoint, persistence and failed-refresh regression tests.

Automatic refresh interval is currently fixed at 24 hours. This release does not patch CPA's original provider model lists or change their routing. Browser and live CPA end-to-end verification remain outstanding.

## 0.5.0

- Attribute monitoring statistics to the complete client request name supplied by CPA (`Alias`), falling back to the executed model only when no alias is supplied. Keep different routing namespaces separate.
- Show CPA execution model metadata separately without rewriting the response's reported model identity.
- Align form controls when adjacent labels wrap onto multiple lines.
- Add a per-member “复制能力” button to replace combo capabilities with that model's reported metadata, preserving combo routing/name and warning about missing fields instead of guessing model capabilities.
- Read available capability metadata from both OpenAI and Gemini model-list responses.
- Add regression tests for all README identity examples and independent request-namespace statistics.
- Advertise version `0.5.0` in the CPA registry as well as the build version.

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
