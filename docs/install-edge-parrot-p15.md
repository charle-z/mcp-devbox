# Historical P15 Parrot installation contract

> **Historical.** This document preserves the P15 package, signing, updater, rollback,
> and onboarding design. The canonical current operator procedure is
> [`install-edge-parrot-p16.md`](install-edge-parrot-p16.md).

Do not infer an installed Edge from this source document. A source release, published
package artifact, and installed Edge require separate evidence. Verify live device
state with the supported local doctor/status flow rather than a release name copied
from documentation.

## Operator workflow

The supported initial artifact is the official `mcp-devbox-edge_<version>_amd64.deb`
plus its detached armored signature and SHA-256 file. Release automation publishes
all three from the same immutable commit. The package must be obtained from the
official release and its signature/hash must be verified by the supported bootstrap
or signed APT repository before installation.

The only privileged installation action is:

```text
sudo apt install ./mcp-devbox-edge_<version>_amd64.deb
```

Then the future Edge user performs one guided action:

```text
mcp-edge onboard --server https://mcp-devbox-charlez.duckdns.org
```

The pairing code is read from standard input and is never accepted as an argument.
Onboarding verifies the installed signed bundle, runs the Bubblewrap/systemd/rootless/
Node/Go/signed-harness/provider/driver preflight, pairs without replacing an existing
identity, and waits for the systemd path unit to start and health-check the Edge. It
prints one safe final result containing only the device ID and valid/active states.

After this bootstrap, the operator does not compile, copy binaries/providers/drivers,
edit units, restart services or register workspaces for individual machines.

## Installed layout

The Debian package installs one release below `/opt/mcp-devbox/releases/<RELEASE>`,
atomically points `/opt/mcp-devbox/current` to it, and maintains only these compatibility
links:

- `/usr/local/bin/mcp-edge`;
- `/usr/local/libexec/mcp-devbox/model-turn-driver`;
- `/usr/local/libexec/mcp-devbox/mcp-autopilot-worker`;
- `/usr/local/libexec/mcp-devbox/mcp-bundle-updater`;
- `/usr/local/libexec/mcp-devbox/node` (the reviewed Node 24.18.0 runtime);
- `/opt/mcp-devbox/current/codex/codex` and `codex/pin.json` in manifest-v4 releases;
- `/opt/mcp-devbox/opencode-provider`;
- `/opt/mcp-devbox/opencode-1.18.1`.

It installs the Edge template, restricted updater oneshot and onboarding path unit
under `/etc/systemd/system`, root-owned configuration under `/etc/mcp-devbox`, and
documentation under `/usr/share/doc/mcp-devbox`.

Node 24.18.0 is part of the signed bundle rather than an ambient host dependency.
The Debian package declares Bubblewrap, Go, Podman, polkit and systemd dependencies,
enables the Edge user's rootless Podman socket, and makes rootless validation mandatory
in onboarding. A missing user-owned socket therefore blocks onboarding instead of
silently producing a partially capable Edge.

## Migration and repeat installation

Package installation is idempotent. It reuses the same release and links, never
regenerates identity or keys, and never deletes workspaces, contracts, checkpoints or
artifacts. The current P12–P14 state root
`~/.local/state/mcp-edge` is left in place. Only when that preferred identity is absent
and the legacy `~/.config/mcp-devbox-edge` identity exists is the complete private state
directory moved atomically to the preferred location; bytes and opaque IDs are not
rewritten.

`postinst` records the previous release link, activates the new link atomically,
installs the signed unit, runs bundle verification and preflight, and restores the
previous link/unit if any step or final service health check fails.

## Automatic updates and repair

`mcp-bundle-updater` runs only as root in a hardened oneshot. Its accepted operations
are exactly `status`, `update stable`, `rollback`, and `repair`; no URL, path, command,
script or caller-provided hash is accepted. `stable` is resolved only from the compiled
official GitHub release base. The Ed25519-signed canonical channel binds release,
commit, protocol, catalog, architecture and archive hash.

The updater downloads a bounded archive, rejects unexpected entries, traversal,
links, duplicates and oversized files, verifies every bundle component in staging,
renames the release into place, swaps `current`, installs only the packaged Edge unit,
restarts only the configured Edge service, checks health and restores the previous
signed release on failure. Rollback accepts only the prior locally known signed bundle.
Repair restores exact compatibility links/modes/unit/service from a valid signed
release or fetches `stable` when the active bundle is incomplete. After a successful
update, cleanup keeps only current and previous and removes every other trusted signed
release directory. It leaves untrusted or malformed directories unchanged.

The unprivileged Edge can request only three fixed root-owned units through a generated
polkit rule: official stable update, previous signed rollback, and official repair.
The rule accepts only `start` for those exact unit names. A private `0600` operation
receipt survives the Edge restart performed by an update; the same operation resumes
diagnosis, while a different or malformed receipt fails closed. The receipt is removed
only after the signed control plane acknowledges completion.

Public tools never accept updater implementation details. `edge_bundle_update` accepts
only `device_id` plus `release=stable`; status, rollback, repair and onboarding status
accept only `device_id`. Diagnostics contain opaque identity and version/health booleans
plus closed blocker codes—never URLs, filesystem paths, hashes supplied by a caller,
commands, scripts, targets, credentials or flags.

## Release automation

`.github/workflows/edge-release.yml` is the only supported publication path. A protected
`edge-release` environment supplies the base64-encoded raw Ed25519 bundle key and the
base64-encoded Debian GPG signing identity. The workflow checks out exact `main`, derives
the public key without disclosing private material, stages all pinned components, runs
the Edge tests/vet/build gates, generates an SPDX SBOM, publishes one immutable GitHub
release, and updates only the separately signed `stable` channel documents. Historical
bridge releases use `p15.x.y`; public Aeontra releases use
`vMAJOR.MINOR.PATCH`. See [`edge-bundles.md`](edge-bundles.md) for the ordered migration.

For an Edge still on manifest v3, dispatch and install one `bridge-v3` release first so
the installed updater learns version 4 while the active service remains OpenCode. After
verifying that bridge, dispatch and install one `codex-v4` release to add the hashed
Codex binary/pin and activate Codex. An Edge already on manifest v4 must keep
`codex-v4` for its compatibility bridge; sending it `bridge-v3` would remove the Codex
layout and is rejected by the updater. Skipping the compatibility bridge causes an old
updater to reject the new release-name format before installation.

`.github/workflows/p15-edge.yml` uses ephemeral release identities on pull requests. It
builds the Debian package twice and compares bytes, exercises a clean isolated package
transaction, reruns post-install migration twice, and proves that identity/workspace
bytes remain unchanged. Ephemeral CI keys never produce an official release.
