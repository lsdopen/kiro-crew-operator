# Tailnet setup

The operator does **not** manage your tailnet policy. It deliberately holds no
Tailscale credential, writes no ACLs, and runs no authentication code of its own.
Everything below is configured once, by a human, and then never changes as people
are onboarded.

## Why the nodes are user-owned rather than tagged

Each crew pod is authenticated **once, interactively, by the person who owns it**,
which makes the node *their* device. That is what unlocks `autogroup:self`:

> `autogroup:self` — "allow access for any user that is authenticated as the same
> user as the source. **Does not apply to tags.**"

A tagged node cannot express "only this one person may reach it" in a single rule,
because a grant matches static selectors and cannot bind the `src` user to a value
in the `dst` tag. You would need one rule per person, and something would have to
write those rules — which is exactly the ACL-writing credential we did not want the
operator to hold.

With user-owned nodes the whole policy is the two blocks below, they are written
once, and onboarding the seventy-first employee requires no policy change at all.

## The policy

```hujson
{
  "grants": [
    {
      // Every member reaches only their OWN crew's dashboard.
      "src": ["autogroup:member"],
      "dst": ["autogroup:self"],
      "ip":  ["tcp:8080"],
    },
  ],
}
```

There is no `ssh` block: Tailscale SSH is not enabled on these pods. The dashboard
is the interface, and `kubectl exec` covers administrative access, so an SSH server
on every crew would be a second way into a pod holding someone's Kiro token without
adding a capability anyone needs.

## Tailnet prerequisites

Both of these are settings on the tailnet, not on the cluster:

1. **MagicDNS and HTTPS certificates must be enabled.** `tailscale serve` needs them
   to publish `https://<hostname>.<tailnet>.ts.net`. This matters because the Kiro
   Crew desktop app accepts `https://` to any host but plain `http://` only on
   loopback — without a certificate there is no URL it will connect to.
2. **`capabilities.tailnet_origin` must not be pinned off.** If your administrator
   has pinned it, the gateway refuses to trust its tailnet origin and answers 403
   even though the network path works.

## Key expiry

Disable key expiry on the crew devices. They are long-lived infrastructure, and an
expired node key would force every owner to re-authenticate their pod on whatever
schedule the tailnet's expiry is set to.

## Onboarding a person

```console
$ kubectl apply -f my-crew.yaml
$ kubectl get kirocrew seagyn -o jsonpath='{.status.tailnetLoginURL}'
https://login.tailscale.com/a/...
```

The login URL is reported in status because it cannot be delivered over the tailnet
the node is still trying to join. The owner opens it, authenticates as themselves,
and the node becomes their device.

From then on:

```console
$ kubectl get kirocrew seagyn -o jsonpath='{.status.dashboardURL}'
https://kiro-crew-seagyn.example-tailnet.ts.net
```

Open that in the desktop app via **New Connection Window**. The first connection may
ask for the dashboard token once.

The last step is the crew's own Kiro login: the gateway runs with
`KIRO_AUTH_INSTALL_SHAPE=remote`, which forces the device-code flow, so it shows a
code and a URL to approve from any browser rather than trying a loopback callback
that could never reach the owner's laptop. That token is vaulted on the instance's
volume, so it survives restarts and the login is a one-time step.

## Two one-time human approvals, by design

Provisioning a crew needs exactly two interactive steps — authenticating the tailnet
node, and the Kiro device-code login. Neither can be automated away, and that is the
point: both are what prove a human is who they claim to be. A credential the operator
could mint unilaterally would be a credential an attacker could mint too. Both are
per-person self-service, done once, and everything between and after them is
automatic.
