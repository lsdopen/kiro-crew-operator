# kiro-crew-operator

A Kubernetes operator that runs one Kiro Crew instance per person.

Each `KiroCrew` is a single-owner deployment: it holds that person's own Kiro
token, so their agent activity draws down their own credits, and it keeps its own
memory, lessons and workspace on its own volume. Nothing is shared between
instances.

## How access works

There is no Ingress, no reverse proxy, no identity provider to run, and no
authentication code in this operator.

Every crew pod joins your tailnet as a **user-owned** node — its owner
authenticates it once, interactively. That single fact is what makes the whole
design collapse to almost nothing, because a user-owned node can be addressed by
Tailscale's `autogroup:self`:

```hujson
{
  "grants": [
    { "src": ["autogroup:member"], "dst": ["autogroup:self"], "ip": ["tcp:22"] },
  ],
  "ssh": [
    { "action": "accept", "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["kirocrew"] },
  ],
}
```

Two static rules, written once by a human, gives every person access to exactly
their own crew and nobody else's. Onboarding the seventy-first employee needs no
policy change, and the operator never touches your ACLs.

A tagged node cannot do this — `autogroup:self` does not apply to tags, and a grant
cannot bind the `src` user to a value inside the `dst` tag — which is why these
nodes are deliberately user-owned rather than provisioned with a machine
credential.

The gateway binds `127.0.0.1` only and a deny-all NetworkPolicy closes the pod
network, so the tailnet's decision is the only one that can admit anybody: the one
way in is Tailscale SSH, served by a non-root `tailscaled` inside the gateway
container, and the dashboard is reached through SSH port forwarding.

See [docs/tailnet.md](docs/tailnet.md) for the policy and prerequisites.

## Connecting

Each crew runs Tailscale SSH inside the gateway container, as the same non-root
user as the gateway, so the shell lands where `kirocrew` and the data volume are.
The Kiro Crew **desktop app** adds a remote crew over SSH to the node's tailnet
hostname (`spec.tailscale.hostname`, default `kiro-crew-<name>`): it forwards a
local port to `127.0.0.1:8080` in the pod and signs in with the login token the
`kirocrew` CLI mints there. Nothing is served over HTTPS, so `status.dashboardURL`
is always empty.

The SSH login user is `kirocrew`; the tailnet `ssh` rule above must allow it.

## The gateway image

`spec.gateway.image` defaults to `ghcr.io/lsdopen/kiro-crew-gateway`, built from
[images/gateway/Containerfile](images/gateway/Containerfile). It is upstream's own
`ghcr.io/kirodotdev/kirocrew` — which already carries `kiro-cli`, the agent
runtime — plus three things a crew on a cluster turns out to need:

| Added | Why |
|-------|-----|
| `uv`, `uvx` | Most MCP servers are distributed as `uvx` commands |
| `node`, `npm`, `npx` | The rest are `npx` commands; also any JS/TS repo work |
| `tailscale`, `tailscaled` | Tailscale SSH runs inside this container, supervised by a small entrypoint that exits if either process dies; `kirocrew` is also linked into `~/.local/bin` for the desktop app |

Upstream's image is deliberately minimal — `ca-certificates`, `curl`, `git`,
`ripgrep`, `tini`, `unzip` and Python — so on it a crew's MCP servers simply fail
to start, which makes `spec.mcpConfigRef` close to decorative. Set
`spec.gateway.image` to the upstream image to run it unmodified instead.

Each base is a precise semver tag (`repo:X.Y.Z`), which Dependabot follows. Every added binary is
executed during the build (`uv --version`, `node --version`, `tailscale version`,
…) so a changed upstream layout fails the build instead of shipping a subtly
broken image.

### It releases independently of the operator

The gateway image tracks **upstream KiroCrew**, not this operator's version, so
the two have separate cadences and an operator `v*` tag publishes no gateway
image. Dependabot watches the bases daily and opens a PR when upstream's version
tag moves; merging that PR to `main` republishes the image via
[.github/workflows/release-gateway.yml](.github/workflows/release-gateway.yml).
So a new upstream KiroCrew release reaches crews without an operator release and
without anyone watching for it. A weekly schedule catches a tag that moved
without a PR, and a pull request touching the image builds it without publishing,
so a broken `Containerfile` fails in review.

Published tags:

| Tag | Meaning |
|-----|---------|
| `latest` | Moves with every publish; not used as a default |
| `kirocrew-<version>` | The upstream KiroCrew release it carries, read from the base image's own label rather than maintained by hand |
| `sha-<commit>` | The exact commit of this repo that built it |

The operator defaults to a precise `kirocrew-<version>` tag, never `latest`, so a
crew's runtime changes only through a reviewed commit. Admins can roll the fleet
without an operator release by setting `gateway.image` in the `KiroCrewConfig`,
or per crew with `spec.gateway.image`.

## Two one-time human approvals

Provisioning a crew requires exactly two interactive steps, and neither can be
automated away:

1. **Authenticate the tailnet node.** The URL appears in
   `status.tailnetLoginURL`, because it cannot be delivered over the tailnet the
   node is still trying to join.
2. **Log the crew in to Kiro.** The gateway runs with
   `KIRO_AUTH_INSTALL_SHAPE=remote`, which forces the device-code flow — it prints
   a code to approve from any browser instead of attempting a loopback callback
   that could never reach the owner's laptop.

Both are per-person self-service, done once. That they need a human is the point:
a credential the operator could mint on its own would be a credential an attacker
could mint too. Everything else is automatic, and both survive pod restarts
because the tailnet node identity and the Kiro token vault live on the volume.

## Sizes

`spec.size` selects a resource tier, mirroring Kiro Crew's own cloud sizes with an
extra tier below Light:

| Tier | CPU request | Memory | Volume | ~Parallel sub-agents |
|---------------|-------------|--------|--------|----------------------|
| `mini` | 500m | 8Gi | 20Gi | ~1 |
| `light` *(default)* | 1 | 16Gi | 40Gi | ~3 |
| `development` | 2 | 32Gi | 60Gi | ~6 |
| `power` | 4 | 64Gi | 80Gi | ~12 |

Memory request equals its limit, because memory cannot be safely overcommitted — a
crew that bursts past a soft request is OOMKilled. CPU is requested well below the
tier and left unlimited, so the many idle crews on a shared cluster cost almost
nothing while an active fan-out still bursts to the whole tier.

Budget by memory: it is paid continuously per crew, and idle instances cannot be
scaled to zero because the gateway runs the cron scheduler in-process. Seventy
`light` crews is roughly 1.1 TiB of memory requests, which is why `light` rather
than `development` is the default here even though `development` is Kiro Crew's own
default for a dedicated machine.

## Storage must be block storage

`spec.storage.storageClassName` must resolve to block storage such as `gp3`.

**Do not use EFS or any other network filesystem.** Kiro Crew keeps `memory.db`,
`memory_index.db` and `knowledge.db` as SQLite in WAL mode, and SQLite documents
WAL as unsupported over a network filesystem — it needs an mmap'd shared-memory
file. The failure modes are `database is locked`, `database disk image is
malformed`, and silent corruption of a user's memory. This holds even with a
single writer, because it is not a concurrency problem.

The consequence is that an instance is pinned to its volume's availability zone.
A node failure reschedules fine within that zone; only a full-zone outage takes a
crew offline.

## Getting started

Prerequisites: an EKS cluster, a tailnet with MagicDNS enabled, and the policy from [docs/tailnet.md](docs/tailnet.md) applied.

```sh
# Install
helm install kiro-crew-operator \
  oci://ghcr.io/lsdopen/charts/kiro-crew-operator --version 0.1.0 \
  --namespace kiro-crew --create-namespace \
  --set tailnet.domain=example-tailnet.ts.net

# Create a crew
kubectl apply -f config/samples/kirocrew_v1alpha1_kirocrew.yaml

# Give its owner the login URL
kubectl get kirocrew seagyn -o jsonpath='{.status.tailnetLoginURL}'
```

The chart and the operator image are published to GHCR by
[.github/workflows/release.yml](.github/workflows/release.yml) on every `v*` tag.

If `helm install` reports `unauthorized`, the GHCR **package** is still private:
package visibility is set per package and is not inherited from the repository, so
each one has to be made public once under its own *Package settings → Change
visibility*.

## Releasing

Two independent streams.

The **operator** (its image and the chart) is released automatically from
Conventional Commit messages. [release-please](https://github.com/googleapis/release-please)
maintains a **release PR** on `main` that bumps the version, rewrites
`CHANGELOG.md` and bumps `dist/chart/Chart.yaml` — merging that PR is the release
decision. On merge it tags `vX.Y.Z` and publishes a GitHub Release, which triggers
[.github/workflows/release.yml](.github/workflows/release.yml) to push the image
and chart. So the version is *computed* from commits, but a human still gates it
by merging the PR, and `main` history stays reviewed and signed.

This means commit messages matter: `feat:` → minor, `fix:`/`perf:`/`deps:` →
patch, and a `!` or a `BREAKING CHANGE:` footer → major (minor while pre-1.0). A
hand-pushed `v*` tag still works as an escape hatch if a release ever has to be
cut without release-please.

The **gateway image** is not versioned at all — it republishes whenever
`images/gateway/**` changes on `main`, which is normally a merged Dependabot base
bump. See [The gateway image](#it-releases-independently-of-the-operator).

## Development

```sh
make manifests generate   # after editing api/**/*_types.go or markers
make test                 # unit tests (envtest)
make build                # compile
```
