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
      // Every member reaches only their OWN crew over SSH.
      "src": ["autogroup:member"],
      "dst": ["autogroup:self"],
      "ip":  ["tcp:22"],
    },
  ],
  "ssh": [
    {
      "action": "accept",
      "src":    ["autogroup:member"],
      "dst":    ["autogroup:self"],
      "users":  ["kirocrew"],
    },
  ],
}
```

Tailscale SSH is the only way in. It runs inside the gateway container as the
gateway's own non-root user, so the only login user it can serve is `kirocrew`, and
a session is exactly as privileged as the gateway process. The desktop app uses it
for the port forward to the loopback-only dashboard and to mint its login token.

## Tailnet prerequisites

**MagicDNS must be enabled**, so the desktop app can reach `<hostname>.<tailnet>`.

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

From then on, add `kiro-crew-seagyn.example-tailnet.ts.net` as a remote crew in the
desktop app, which forwards a local port to the dashboard over SSH and signs in
with a token minted on the pod.

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
