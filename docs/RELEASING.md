# Releasing

How Reasonix ships, who can ship what, and the canary-before-stable flow.

## Branch model: trunk + tags

- **`main-v2`** is the single development line (the v2 / 1.x trunk). Every PR merges here.
- **Production is a tag, not a branch.** A release is a tagged snapshot of `main-v2`:
  `v1.4.0` (CLI), `npm-v1.4.0` (npm), `desktop-v1.4.0` (desktop).
- **`v1`** is the archived 1.0/legacy line — maintenance only.
- **Hotfix** an already-released version by branching from its tag, fixing, and tagging again.

There is no separate "production" or "develop" branch by design — the canary channel
provides the pre-release buffer instead of a long-lived branch.

## Channels

| Surface | Stable | Pre-release buffer |
|---|---|---|
| npm | `latest` (current 1.x stable) | `next` (rc), `canary` (`npm i reasonix@canary`) |
| Desktop | R2 `latest/` pointer + release gateway | R2 `canary/` pointer + release gateway proxy (never on the GitHub releases page) |

A canary build is isolated: it **never** moves `latest` / `next` / desktop `latest/`.
Testers opt in explicitly. (Desktop builds carry `-X main.channel=canary`; npm versions
ending in `-canary.N` publish under the `canary` dist-tag.)

## Who can release what

| Action | Who | Mechanism |
|---|---|---|
| **Cut a canary** | any maintainer (write access) | `workflow_dispatch`, runs without a production approval |
| **Ship stable** | release-tag creators + one configured reviewer | atomically push the three stable tags; the **Release stable** workflow requests one GitHub `release`-environment approval before every channel publishes |
| **Ship a standalone RC** | release-tag creators + one configured reviewer | push the surface-specific prerelease tag; that one standalone workflow requests one `release` approval |

So a maintainer can dispatch a canary anytime. A stable release pauses once in
the **Release stable** run until a configured reviewer approves the GitHub
`release` environment; the CLI, npm, and Desktop jobs then continue without
another GitHub environment prompt. Windows signing intentionally retains
separate SignPath confirmations for the AMD64 and ARM64 requests.

> Repo settings backing this: Environments → `release` has the release owners as
> required reviewers, and the release-tag ruleset restricts
> `v*`/`npm-v*`/`desktop-v*` creation, update, and deletion. Only the
> orchestrator and standalone RC/recovery paths reference the protected
> environment.

The reusable publishers additionally require the stable orchestrator to run on
the protected stable tag (or protected `main-v2` recovery ref). Normal tag-push
releases bind the caller workflow commit to the approved SHA. Recovery keeps the
fixed control-plane workflow on protected `main-v2`, resolves the existing three
tags to one immutable historical SHA on `main-v2`, and uses that SHA only for the
actual build and publication checkouts. Every publisher revalidates its remote
release tag immediately before publication. An unprotected branch cannot claim
that it already passed the approval job.

Repository `write` access remains a privileged role: GitHub Actions workflows on
repository branches can access repository-level Actions secrets. Do not grant
`write` to someone who must be technically unable to publish. A stricter trust
separation requires moving external publication credentials to protected
environment secrets or provider-side OIDC/trusted-publishing policies; the
workflow approval and tag ruleset primarily protect the supported release path
from accidental or unauthorized invocation.

## The release loop

1. **Develop** — PRs land on `main-v2` (branch auto-deletes on merge).
2. **Prepare the release notes without creating a release-only PR by default** —
   Actions → **Prepare release notes**. Enter the intended version, the previous
   desktop tag when needed, and the number of an existing release-bound PR when
   one is available. The reusable PR must be open, target `main-v2`, come from a
   branch in this repository, and already include the latest `main-v2`. The
   workflow commits the generated notes onto that branch, so product changes and
   their release copy share one PR and one review surface. The added commit still
   reruns that PR's required checks.

   Leave `target_pr` empty only when there is no suitable PR, the candidate PR
   comes from a fork that the repository token cannot update, or the release copy
   needs independent editorial review. In that fallback, the workflow opens or
   updates the dedicated `release-notes/vX.Y.Z` PR as before.

   The workflow sends only public merged-PR metadata to DeepSeek, creates
   equivalent English and Chinese product notes, validates their structure and
   citations, and includes the rendered draft in the workflow summary. Review
   the catalog diff like product copy. Once merged, the same entry drives
   `/changelog/` and both CLI and Desktop GitHub Releases; the desktop app links
   to that web history from Settings → Updates. A missing catalog entry still
   blocks stable publication.
3. **Cut a canary** before the intended release (e.g. heading for `1.4.0`):
   - Desktop: Actions → **Release desktop** → `channel: canary`, `base_version: 1.4.0`
   - CLI: Actions → **Release npm** → `base_version: 1.4.0`
   - Publishes `1.4.0-canary.N` to the desktop R2 `canary/` pointer (no GitHub release) and npm `@canary`.
4. **Test** — testers install `reasonix@canary` (CLI) or grab the desktop canary
   build from its R2 link, and report bugs.
5. **Fix** on `main-v2` via PRs; re-cut the canary as needed (`canary.N` bumps).
   Re-run **Prepare release notes** after material fixes. Reuse the still-open
   release-bound PR when possible; otherwise the workflow updates the same
   dedicated release-notes branch and PR without publishing anything.
6. **Ship stable** when the canary is clean and the release-notes PR is merged —
   create the three tags locally, then push them atomically:
   ```sh
   git tag v1.4.0
   git tag npm-v1.4.0
   git tag desktop-v1.4.0
   git push --atomic origin v1.4.0 npm-v1.4.0 desktop-v1.4.0
   ```
   The `v1.4.0` tag starts **Release stable**. Its preflight requires all three
   tags to exist on the exact current `main-v2` commit, renders the reviewed
   release notes, and runs the cache guard. It then **waits once for a configured
   reviewer to approve the GitHub `release` environment** before invoking all
   three publishers. The approval records the immutable release commit; every
   publisher checks out that SHA and fails if its remote tag moves afterward.
   The two Windows signing requests then retain their manual SignPath
   confirmations as a separate control.
   A stable `npm-v*` publish moves the `latest` dist-tag automatically (build.mjs)
   and release-npm.yml verifies it landed. **Do not skip the npm tag**: the stable
   preflight fails when the matching `npm-v*` or `desktop-v*` tag is missing or
   points elsewhere. That guard exists because 1.0.0–1.17.5 shipped without
   stable npm tags and `npm update -g` silently downgraded users to 0.53.2 (#5822).
   The CLI and npm jobs run concurrently; the CLI's freshness check may warn while
   npm is still propagating, while release-npm.yml's verify step owns the final
   assertion. The stable orchestrator finishes with a postflight that verifies
   both GitHub Releases contain their required assets and npm `latest` exactly
   matches the approved version; missing artifacts can no longer produce a green
   stable run.
7. **Next cycle** — the canary rolls on toward `1.5.0`.

### Release-PR frequency rule

- Do not open a dedicated PR merely because a version is being published.
- Prefer the final same-repository product, fix, or release-infrastructure PR
  that already defines the candidate boundary. Generate the notes into that PR
  before its final review.
- A dedicated release-notes PR is the safe fallback, not the default. Use it
  when no suitable PR exists, the only candidate PR comes from a fork, or the
  release copy needs an independent approval boundary.
- Never bypass protected `main-v2` with a direct catalog commit. Reducing PR
  frequency must not remove release-copy review, catalog validation, cache
  checks, atomic tags, the single `release` environment approval, or public
  artifact postflight.
- Release infrastructure and release-note schema changes still require their
  own focused PR. This rule only avoids redundant version-only PRs.

## Notes

- Canary version numbers use the workflow `run_number`, so the desktop and CLI canary
  numbers differ (e.g. `canary.11` vs `canary.2`). Only monotonicity per channel matters.
- A stable `-rc` tag (e.g. `npm-v1.4.0-rc.1`) still ships under `next`, not `canary`.
- Recover an interrupted stable release by dispatching **Release stable** from
  protected `main-v2` with the existing `vX.Y.Z` tag. Recovery requires the CLI,
  npm, and Desktop tags to remain aligned on an ancestor of current `main-v2`,
  then uses the same single approval and postflight. Never move or recreate the
  published tags to pick up a workflow fix.
- Windows release signing uses SignPath trusted-build and origin verification.
  Keep **Use approval process** enabled on `release-signing`: the AMD64 and ARM64
  requests can each require a manual confirmation after the single GitHub
  release-environment approval.
- Desktop in-app updates use R2 first, then the `crash.reasonix.io` desktop release
  gateway. The gateway resolves the `desktop-v*` release line directly and never uses
  GitHub's repository-wide `/releases/latest`, because plain `v*` tags are the CLI
  release line. Stable CLI releases also carry a compatibility `latest.json` asset so
  older desktop builds that still use GitHub `latest` do not 404.
- Canary uses R2 plus the same gateway proxy for the `canary/` pointer; it never
  appears on the GitHub releases page.
- DeepSeek is an editorial drafting dependency, not a runtime or publishing dependency.
  The API key is available only to the manually dispatched preparation workflow; tag
  workflows publish the reviewed JSON already committed to `main-v2` and never call a model.
- Windows applies the minisign-verified NSIS installer in place. Linux portable
  (`.tar.gz`) installs replace binaries in the install directory; Linux `.deb`
  installs download a signed package, authorize via Polkit, and upgrade with
  apt. The first `.deb` that ships the update helper is a one-time bootstrap
  (manual `sudo apt install ./Reasonix-linux-amd64.deb`). macOS applies in-app
  only for Developer ID signed and notarized builds; ad-hoc/local builds fall
  back to the download page. Desktop `latest.json` keeps `platforms` for
  portable channels and adds optional `native_packages` for OS packages.
