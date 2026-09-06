# Versioning and release retention

Aeontra uses `vMAJOR.MINOR.PATCH` for public signed releases.

- **MAJOR** changes a documented compatibility contract and requires an explicit
  migration and rollback plan.
- **MINOR** adds backward-compatible product capability.
- **PATCH** fixes or hardens existing behavior without intentionally changing public
  compatibility.

The historical `p15.x.y` line remains in Git evidence but is not used for new public
releases. The Go module, binaries, services, environment variables, paths and protocol
identifiers retain their documented `mcp-devbox` and `mcp-edge` compatibility names.

## Release identities

Keep these facts separate:

1. source commit and exact-head CI;
2. immutable Git tag and GitHub release;
3. signed Linux and Windows artifacts, checksums, SBOMs and third-party notices;
4. mutable signed `stable` channel;
5. backend and Front Door deployment identity;
6. installed and accepted device identity.

The `stable` release contains only signed channel metadata. It points to one immutable
versioned release; it is not itself a software version.

## Retention

Keep:

- all Git tags and repository history;
- the current immutable versioned release and its complete assets;
- the `stable` channel;
- named operational rollback branches required by current deployment topology;
- dated baselines that explain historical acceptance.

Intermediate GitHub releases may be removed after all supported devices have crossed
the compatibility boundary, the stable channel names the retained release, required
rollback state exists locally or in named rollback branches, and exact assets are no
longer needed for supported reinstall. Removing a GitHub release does not remove its
Git tag unless that is separately approved.

After a successful Linux Edge update, local bundle cleanup keeps only the trusted
release directories named by `current` and `previous`. It removes other trusted
signed release directories regardless of age and leaves untrusted or malformed
directories unchanged for fail-closed investigation.

## Release process

1. Merge an exact-head-green pull request into protected `main`.
2. Dispatch the official signed release workflow from the exact `main` commit.
3. Verify immutable artifacts, checksums, signatures, SBOMs and notice assets.
4. Verify both signed `stable` channel documents name the intended release and commit.
5. Update each real device independently and record profile-specific acceptance.
6. Validate backend deployment separately when the source change affects the control
   plane or public catalog.
7. Record user-visible changes in `CHANGELOG.md` and exact operational evidence in a
   dated baseline.

See `docs/edge-bundles.md`, `docs/open-source-release.md` and the platform-specific
installation guides for generation, migration, update and rollback details.
