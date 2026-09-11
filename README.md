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
    { "src": ["autogroup:member"], "dst": ["autogroup:self"], "ip": ["tcp:8080"] },
  ],
}
```

One static rule, written once by a human, gives every person access to exactly
their own crew and nobody else's. Onboarding the seventy-first employee needs no
policy change, and the operator never touches your ACLs.

A tagged node cannot do this — `autogroup:self` does not apply to tags, and a grant
cannot bind the `src` user to a value inside the `dst` tag — which is why these
nodes are deliberately user-owned rather than provisioned with a machine
credential.

The gateway binds `127.0.0.1` only and a deny-all NetworkPolicy closes the pod
network, so the tailnet's decision is the only one that can admit anybody.

See [docs/tailnet.md](docs/tailnet.md) for the policy and prerequisites.

## Connecting

The dashboard is published on the tailnet by `tailscale serve`, so each instance
gets an HTTPS address:

```console
$ kubectl get kirocrew seagyn -o jsonpath='{.status.dashboardURL}'
https://kiro-crew-seagyn.example-tailnet.ts.net
```

Open that in the Kiro Crew **desktop app** via *New Connection Window*. HTTPS
matters: the app accepts `https://` to any host but plain `http://` only on
loopback.

Tailscale SSH is not enabled. The dashboard is the interface and `kubectl exec`
covers administrative access, so an SSH server on every crew would be a second way
into a pod holding someone's Kiro token without adding a capability anyone needs.

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

Prerequisites: an EKS cluster, a tailnet with MagicDNS and HTTPS certificates
enabled, and the policy from [docs/tailnet.md](docs/tailnet.md) applied.

```sh
# Install
helm install kiro-crew-operator ./dist/chart \
  --namespace kiro-crew --create-namespace \
  --set tailnet.domain=example-tailnet.ts.net

# Create a crew
kubectl apply -f config/samples/kirocrew_v1alpha1_kirocrew.yaml

# Give its owner the login URL
kubectl get kirocrew seagyn -o jsonpath='{.status.tailnetLoginURL}'
```

## Development

```sh
make manifests generate   # after editing api/**/*_types.go or markers
make test                 # unit tests (envtest)
make build                # compile
```
