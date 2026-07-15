# Releasing

How to cut a release. The pipeline is
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) — a
`v*` tag runs three ordered jobs: multi-arch image → Helm charts → GitHub
Release. Nothing else publishes artifacts.

## The one invariant

**The image tag is the bare version** (`v1.2.3` → `1.2.3`). `helm package
--app-version` stamps the same bare version into the operator chart, and the
chart's `image.tag` defaults to `.Chart.AppVersion`
(`charts/kube-headroom/templates/_helpers.tpl`). If the image were tagged
`v1.2.3`, a default `helm install` would pull a nonexistent image. The
workflow preserves this; don't "fix" it.

## Steps

1. **Prep the announcement first** — a news post under [`docs/news/`](../news/)
   (dated file + entry in `index.md` and the VitePress sidebar), plus any
   landing-page copy updates. Open the PR but **hold the merge until after the
   tag**: the site must never advertise a release that doesn't exist. The
   release URL is predictable (`…/releases/tag/vX.Y.Z`), so content can be
   final beforehand.

2. **Tag the tip of `main`** (all release PRs merged, CI green):

   ```sh
   git fetch origin main
   git tag -a vX.Y.Z -m "kube-headroom vX.Y.Z" origin/main
   git push origin vX.Y.Z
   ```

   Unsure about the pipeline? Rehearse with a `vX.Y.Z-rc.1` tag first, verify,
   then delete the rc tag, release, and ghcr package versions.

3. **Watch the Release workflow** (image → charts → release, in that order —
   charts are gated on the image so a failed build can't publish a chart
   pointing at a missing image).

4. **Verify the artifacts publicly** — from an unauthenticated client, so a
   ghcr package that silently landed private fails loudly:

   ```sh
   docker pull ghcr.io/karlkfi/kube-headroom:X.Y.Z          # bare version
   docker manifest inspect ghcr.io/karlkfi/kube-headroom:X.Y.Z  # amd64 + arm64
   helm pull oci://ghcr.io/karlkfi/charts/kube-headroom --version X.Y.Z
   helm pull oci://ghcr.io/karlkfi/charts/kube-headroom-crds --version X.Y.Z
   gh release view vX.Y.Z    # install notes present
   ```

   If a pull 403s, flip that package to Public at
   <https://github.com/users/karlkfi/packages> (three packages:
   `kube-headroom`, `charts/kube-headroom`, `charts/kube-headroom-crds`).

5. **Merge the announcement PR.** The website deploys on merge to `main`;
   confirm the post and banner at <https://kube-headroom.dev>.

## Versioning notes

- Chart `version` and `appVersion` are stamped from the tag at package time —
  the `0.1.0` values checked into `charts/*/Chart.yaml` are dev placeholders.
- The CRDs chart's checked-in `appVersion: v1alpha1` tracks API maturity, but
  the release pipeline stamps it with the release version like the operator
  chart; API-maturity progression is
  [its own plan](../plan/api-version-progression.md).
