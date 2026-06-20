# Contributing to `srm`

Thanks for helping improve `srm`. It is a root-operated fleet administrator for
GitHub Actions self-hosted runners — it handles App private keys, mints runner
tokens, and writes systemd units as root — so the bar for changes is correctness,
safety, and a clean, auditable history. This document is the definition of "done".

The only supported runtime target is **Ubuntu x64** (systemd + apt + cgroup v2).
Development can happen on any OS Go supports; tests and `go vet` run cross-platform,
and the shipped artifact is a static `linux/amd64` build.

---

## 1. Prerequisites

- **Go** matching the version in [`go.mod`](go.mod) (`go 1.26.4`). CI reads the
  version from `go.mod`, so match it locally to avoid surprises.
- **make** (the [`Makefile`](Makefile) is the canonical build/test entrypoint).
- For end-to-end work, an Ubuntu x64 host you control. **Never** test destructive
  lifecycle commands (`uninstall`, `runners destroy`, in-place updates) against a
  fleet you don't own.

```bash
make build      # native dev binary (./srm)
make build-linux # static linux/amd64 (dist/srm) — the shipped shape
make test       # go test ./...
make vet        # go vet ./...
make tidy       # go mod tidy
make run        # go run ./cmd/srm
```

---

## 2. Branching model

- **`stable`** is the release branch. Its `HEAD` is **always releasable** — every
  commit on `stable` is a self-contained, tested, shippable change. Nothing
  half-finished, behind no flag, or "to be fixed in a follow-up" lands here.
- Do work on a topic branch off `stable`, named by intent:
  `feat/<slug>`, `fix/<slug>`, `docs/<slug>`, `ci/<slug>`, `refactor/<slug>`.
- CI runs on every push to `feat/**` and on every pull request, and again on
  `stable`. A branch is mergeable only when CI is green.

You may commit as messily as you like on your topic branch — those commits are
collapsed on merge (see §4), so commit early and often while iterating.

---

## 3. Commit messages

Commits follow **[Conventional Commits](https://www.conventionalcommits.org/)**:

```
<type>(<optional scope>): <imperative, lower-case summary>

<body: what changed and WHY — the reasoning that isn't obvious from the diff>
```

Types in use: `feat`, `fix`, `docs`, `refactor`, `test`, `ci`, `build`, `polish`,
`chore`. Scope is the area touched (`tui`, `cli`, `install`, `uninstall`,
`destroy`, `service`, …). The summary is what becomes the squash commit on
`stable` (§4), so make it accurate.

Do **not** add `Co-Authored-By` / "Generated with" trailers — commits are
single-authored.

---

## 4. Pull requests → `stable`

1. **Open a PR from your topic branch into `stable`.** One PR = one coherent,
   releasable change. If you find yourself writing "and also…", it's probably two PRs.
2. **CI must be green** — build, `go vet`, `go mod tidy` (no diff), `go test`, and
   the `govulncheck` gate (§7).
3. **Update the changelog in the same PR** (§5). A user-facing change without a
   changelog entry is incomplete.
4. **Squash and merge — always.** `stable` keeps **one commit per change**; there
   is no long trail of WIP commits and no merge commits. The squash commit's
   message is the PR's Conventional-Commit summary + a body explaining the change.
   - Merge commits and rebase-merge are **not** used for `stable`.
   - Recommended repo setting: enable *only* "Allow squash merging" and turn on
     "Automatically delete head branches" so this is enforced, not just convention.

---

## 5. Changelog

`srm` keeps a [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
[`CHANGELOG.md`](CHANGELOG.md). **Every PR with a user-visible effect updates it**,
under the top `## [Unreleased]` section, in the right category:

- **Added** — new features, flags, commands.
- **Changed** — changes to existing behavior.
- **Deprecated** — soon-to-be-removed behavior.
- **Removed** — removed behavior (a MAJOR bump; see §6).
- **Fixed** — bug fixes.
- **Security** — anything touching auth, secrets, isolation, hardening, or a CVE.

Write entries for the **user**, not the diff: describe the observable change and,
where it matters, why. Purely internal changes (refactors, test-only, CI plumbing)
may be omitted.

---

## 6. Versioning (SemVer)

`srm` follows **[Semantic Versioning](https://semver.org/)** — `MAJOR.MINOR.PATCH`:

- **MAJOR** — a breaking change: a removed/renamed CLI command or flag, an
  incompatible **config schema** change, or an on-disk layout change (install root,
  unit naming, control-file paths) that an existing host can't read.
- **MINOR** — new, backward-compatible functionality (a new command, flag, or TUI
  capability). Existing configs and hosts keep working untouched.
- **PATCH** — backward-compatible bug fixes and hardening.

Treat the **config file, the systemd unit/template layout, and the CLI surface** as
the public API for SemVer purposes — a managed host upgrading in place must not break.

The binary version is build-stamped (`-X main.version`) and shown by
`srm version` / `srm --version`. An unstamped `go build`/`go run` reports `dev`.

---

## 7. CI gates

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) must pass before merge:

- `go mod tidy` leaves no diff, `go vet ./...`, `go build` (linux/amd64), `go test ./...`.
- **`govulncheck ./...`** — fails on any known vulnerability reachable in our code
  or dependencies. Keep it green; bump the offending dependency rather than
  suppressing it.

Actions are **pinned to commit SHAs** (a tag is mutable and a supply-chain risk);
[Dependabot](.github/dependabot.yml) bumps the pins and Go modules weekly. When you
add a new action, pin it to a SHA with a `# vX.Y.Z` comment.

---

## 8. Releasing (maintainers)

Releases are cut from `stable` and built by
[`.github/workflows/release.yml`](.github/workflows/release.yml):

1. On `stable`, turn the `## [Unreleased]` block into `## [X.Y.Z] — YYYY-MM-DD`,
   add a fresh empty `## [Unreleased]`, and add the version's compare/tag link at
   the bottom of `CHANGELOG.md`.
2. Pick `X.Y.Z` per §6 from what's in that block.
3. Tag the release commit and push the tag:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

4. The release workflow builds the static `srm-vX.Y.Z-ubuntu-x64` binary + its
   `.sha256` and publishes a GitHub Release (`--verify-tag`). Hosts install by
   downloading the artifact and verifying the checksum.

> Forward-looking: in-tool **self-update** (roadmap v1.2) requires **signed**
> release artifacts, not checksum-only — see
> [docs/design/v1.1-lifecycle.md](docs/design/v1.1-lifecycle.md). Until then,
> releases are checksum-verified and installed manually/scripted.

---

## 9. Security & secrets

- **Never commit secrets.** `.env`, `config.yaml`, `*.pem`, and `secrets.age` are
  gitignored — do not force-add them. App private keys are root-only under
  `/etc/srm` on a host and are shown once by GitHub; treat them as unrecoverable.
- Changes touching auth, token minting, secret storage, per-org isolation, or
  systemd hardening get extra scrutiny and a **Security** changelog entry.
- Found a vulnerability? Report it privately to the maintainer rather than opening
  a public issue.

---

## 10. License

By contributing, you agree your contributions are licensed under the project's
**Apache License 2.0** ([LICENSE](LICENSE)).
