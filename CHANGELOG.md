# Changelog

This file records user-visible changes to Aeontra. Git history and pull requests retain
the complete change record. Compatibility executable, module, service and protocol
identifiers continue to use `mcp-devbox` and `mcp-edge` where documented.

## Unreleased

- Retain only the current and previous trusted Linux Edge bundles after a successful
  update so obsolete local releases do not accumulate.
- Preserve terminal timestamps in native Windows process listings so reconciled stopped
  workers remain valid list results.
- Add a bounded public-alpha install, acceptance, and feedback path and describe signed
  Edge assets directly in release notes.
- Report native Windows bundle/onboarding diagnostics through the paired Edge with an
  exact SCM process binding instead of returning `diagnostic_unavailable_windows`.
- Continue an already-persisted durable process stop during identity-safe
  reconciliation while preserving ordinary running workers across Edge updates.

## v1.2.25 — 2026-08-27

- Generate deterministic Linux and Windows third-party notice assets alongside each
  signed Edge release.
- Reconcile the public roadmap with the accepted Linux/Parrot, Windows, Codex and P16
  execution surfaces.

## v1.2.24 — 2026-08-26

- Accepted native Windows workcell results without applying Linux-only filesystem
  assumptions.
- Published signed Linux and Windows Edge artifacts, checksums, signatures and SBOMs.
- Reconciled the backend, stable Front Door and both paired Edge platforms on one commit
  and catalog identity.

Earlier release evidence remains in Git, GitHub tags and `docs/baselines/`. Historical
release assets are retained according to the current release-retention policy rather
than duplicated in this changelog.
