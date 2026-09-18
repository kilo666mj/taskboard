# Publishing the public repository

Taskboard's private predecessor history contains internal deployment names and
old synthetic secret-like test fixtures. It was therefore not mirrored into the
public repository: the public history starts with a reviewed snapshot, and
GitHub is the authoritative upstream from that point onward.

## First publication

1. Start from a clean checkout and copy the reviewed tracked tree without its
   `.git` directory, local environment files, databases, build output, or local
   Ansible inventory.
2. Initialize a new repository, commit the snapshot, and run the complete local
   verification suite plus a full-history secret scan against that new history.
3. Create `kilo666mj/taskboard` on GitHub as a public repository, push the clean
   `main` branch, and require pull requests plus successful CI and CodeQL checks.
4. Enable GitHub secret scanning, push protection, Dependabot alerts and security
   updates, private vulnerability reporting, and code scanning. The repository
   workflows already use least-privilege permissions and SHA-pinned actions.
5. Install the Renovate GitHub App for the repository. `renovate.json` groups
   compatible updates and maintains action digest pins.
6. Confirm that CI, CodeQL, dependency review, vulnerability scanning, and the
   container build succeed before creating the first tag.

Repository administrators must perform the GitHub settings and app-installation
steps after the public repository exists; workflow files alone cannot enable
those account-level controls.

## Releases

Push a semantic version tag such as `v1.0.0`. The release workflow builds the
Linux and macOS binary bundles and publishes their checksums. The container
workflow publishes multi-architecture images to GHCR with version, major/minor,
`latest`, and immutable digest-backed tags, plus provenance and an SBOM. The
release also includes a version-matched Helm chart archive in `SHA256SUMS`.

Keep deployment credentials and real infrastructure inventory outside the
repository. Use GitHub environments and narrowly scoped secrets only when a
future release step genuinely needs them.
