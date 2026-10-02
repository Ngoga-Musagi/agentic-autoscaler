# Releasing & installing from the published image

The operator image is published to GitHub Container Registry (GHCR) as a
**multi-architecture** image (linux/amd64 + linux/arm64), so it runs unchanged on
x86 CI runners and Apple-Silicon laptops.

## Cutting a release

1. Set the chart version and app version in `deploy/helm/Chart.yaml` (the chart
   defaults `image.tag` to `appVersion`, so they should match the release).
2. Tag the commit and push the tag:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

3. The [`Release image`](../.github/workflows/release.yml) workflow builds the
   image for both architectures with `docker buildx` and pushes it to
   `ghcr.io/ngoga-musagi/agentic-autoscaler`, tagged `0.1.0`, `0.1`, and
   `latest`.

The tag `v*` is the only trigger; nothing is published on a normal push or PR.

## Installing from the published image (no local build)

On a clean machine with only `helm` and cluster access — no Go, no
`make docker-build` — install straight from the chart. The chart already points
at the GHCR repository and resolves the tag from the chart `appVersion`:

```sh
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set aiProvider.provider=anthropic \
  --set aiProvider.secretRef=ai-provider-secret
```

To pin or override the tag explicitly:

```sh
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set image.tag=0.1.0
```

Because the image is multi-arch, the same command works on amd64 and arm64 nodes;
the container runtime pulls the matching platform automatically.

## Verifying the image is pullable and multi-arch

```sh
docker buildx imagetools inspect ghcr.io/ngoga-musagi/agentic-autoscaler:0.1.0
# → lists linux/amd64 and linux/arm64 manifests
```
