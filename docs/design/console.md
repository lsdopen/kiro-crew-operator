# Design: management console (phase 3)

Status: proposed. Scope: a web console for requesting, approving, starting,
stopping and opening Kiro Crews, with Google Workspace SSO. No implementation
lands with this document.

## Decisions

Rulings 1–11 are in. Rulings 7–11 cover phase 3e only
(see [Editing a running crew](#editing-a-running-crew-3e)). Everything else
in this document is decided.

| # | Question | Ruling | Status |
|---|---|---|---|
| 1 | How does "Start" work outside a crew's schedule? | Latest event wins. Add one event field, `KiroCrewSpec.startedAt *metav1.Time`, set by the console on Start. `computeDesired` / `inUpWindow` (`internal/controller/schedule.go`) treat it as an Up event: desired is up when the most recent of {last Up, last Down, `startedAt`} is an Up or `startedAt`, so the next scheduled Down stops the crew with no deadline field. The requeue includes the next Down after `startedAt`. Start also clears `suspend`; "Stop" sets `suspend=true`, which still forces stop. With no schedule a crew always runs, so `startedAt` is irrelevant. | Changed |
| 2 | Sign-in brokering by `pods/exec`? | Yes. The console execs `kirocrew token`, limited to that one command (see [Narrowing (a)](#narrowing-a)). The upstream gateway identity bootstrap (option b) is pursued in parallel to remove the exec. | Accepted |
| 3 | What happens on a first login from an allowed domain with no `KiroCrewAccount`? | Self sign-up is allowed. The console creates a `KiroCrewAccount` with role `user` and no manager. Until an admin sets a manager, only admins can approve that user's requests. `auth.allowedDomains` is now the only boundary on who gets an account (see the [threat model](#threat-model)). Admins can still edit roles and managers. | Changed |
| 4 | Who may write `spec.decision` outside the console? | No ValidatingAdmissionPolicy and no break-glass group for now. Cluster admins can approve through the Kubernetes API. Residual risk: anyone with update or patch on `kirocrewrequests` can forge `decision.by`, so chart RBAC grants that only to the console ServiceAccount, and the controller's `canDecide` check stays defence in depth. Future option: a ValidatingAdmissionPolicy that allows `spec.decision` changes only from the console ServiceAccount plus a configurable break-glass group. | Deferred |
| 5 | Should the chart docs recommend a private ingress, with public as the opt-in? | Yes, in the docs, not in the chart defaults. The docs lead with the private example and present public as an opt-in. The chart stays exposure-neutral (`ingress.enabled: false`, empty `className` and `annotations`). SSO stays the only *authentication* step either way, and the threat model assumes neither a private network nor an allowlist. | Accepted |
| 6 | Can users request on someone else's behalf? | No. `spec.requester` is always the session email. | Accepted |
| 7 | Who may change a running crew's schedule, and does it need approval? | Owner, owner's manager and admin, applied directly with no request. Same set as Start/Stop. | Accepted |
| 8 | How is a size change approved? | Reuse `KiroCrewRequest` with a new `spec.crewRef` naming the crew. Same approvers as `canDecide`. A manager or admin resizing a report's crew files and approves in one action, so every resize leaves a request with `decision.by`. | Accepted |
| 9 | May a crew be downsized, and what happens to its disk? | Yes for CPU and memory. The PVC never shrinks (Kubernetes cannot shrink one); it grows to the new tier's storage when that is larger. | Accepted |
| 10 | When does an approved resize take effect on a running crew? | Immediately, which restarts the pod. The request form warns the owner. A stopped crew picks it up on its next start. | Accepted |
| 11 | Who may change a crew's MCP config, and to what? | Admin only, choosing from a `KiroCrewConfig.spec.mcpConfigs` allowlist of ConfigMap names. Clearing it falls back to `KiroCrewConfig.spec.mcpConfigRef`. The console gets no ConfigMap access. | Accepted |

Flagged on the decisions already made:

| Decision | Verdict |
|---|---|
| Console behind a Kubernetes Ingress, no tailscale sidecar (revised) | Agree. Crews stay tailnet-only (see [Crew links](#crew-links-and-the-tailnet)). |
| Allowed Workspace domains configurable, checked on `hd` (revised) | Agree, with additions: also require `email_verified=true` and `email` ending `@<hd>`. Keep the list in chart values, **not** the CRD (see [Config](#global-config)). |
| All state in CRDs, no DB | Agree. The console is stateless: sessions are sealed cookies, so it needs no Lease and can run 2+ replicas. |

## Goals and non-goals

| In | Out |
|---|---|
| Google OIDC login for configured Workspace domains | Other OIDC providers (the shape allows them, see [OAuth](#oauth-and-sessions)) |
| Role-gated views: user, manager, admin | Reaching a crew without the tailnet |
| Requests, approvals, start/stop, one-click dashboard sign-in | Editing a running crew's owner, storage class, AWS role or images |
| Editing a running crew's size, schedule and MCP config (3e) | Shrinking a crew's disk |
| Admin: accounts, global config | Kiro login or tailnet auth on the user's behalf. Both stay human steps (`docs/tailnet.md`) |

## Architecture

```
browser ──https──> Ingress, private or public (class, TLS, annotations: WAF/allowlist/oauth2-proxy)
                     │
                     ▼
            kiro-crew-console (Deployment, 2 replicas, no sidecar)
              │ informers: KiroCrewAccount, KiroCrewRequest, KiroCrew, KiroCrewConfig
              │ writes: requests (create, decision), crews (suspend/startedAt/schedule/mcpConfigRef), accounts, config
              │ exec: kirocrew token  (3c only)
              ▼
            kube-apiserver ──> operator reconciles as today
```

| Component | Choice |
|---|---|
| Binary | `cmd/console/main.go`, package `internal/console/...` (`auth`, `authz`, `web`, `broker`) |
| Libraries | `api/v1alpha1`, controller-runtime cache/client (already a dependency), `golang.org/x/oauth2` (already indirect), `github.com/coreos/go-oidc/v3`, `github.com/a-h/templ`. htmx is vendored as a static asset, with no CDN. Pin exact versions. |
| Rendering | templ components, htmx partial swaps, no JS build |
| Image | `ghcr.io/<org>/kiro-crew-console`, `images/console/Containerfile`, distroless static, non-root, read-only rootfs |
| Release | A second build step in `.github/workflows/release.yml`, same version tag as the operator. release-please already owns `Chart.yaml`, so nothing new to bump. |

## Authorization

**Choice: the console ServiceAccount with a minimal Role, plus checks in code.
No impersonation.**

| Option | Wins on | Loses on |
|---|---|---|
| SA + code checks | Expresses "manager of the requester" and "owner of this crew", which are relations between CR fields. Needs no per-person RBAC objects. | The console code is the enforcement point, so it needs tests. The controller's `canDecide` stays as defence in depth (Decision 4). |
| Impersonation | The API server enforces every call | RBAC cannot say "requests whose `requester`'s account names me as manager". You would need a generated RoleBinding per person with `resourceNames`, a controller to keep them in sync, and `impersonate` on users, which is a stronger grant than everything it replaces. |

Every request resolves `session.email` to a KiroCrewAccount (case-insensitive,
like `findAccount`) from the informer cache. Role is re-read on every request,
so demoting an account takes effect immediately.

Sign-up (Decision 3): a first login from an allowed domain with no account
creates one with role `user` and no manager. The console sets both and never
reads them from input. Until an admin sets a manager, only admins can approve
that user's requests, so the request form says "No manager set: an admin must
approve" and the request waits for an admin. Admins can edit roles and managers.

| Action | user | manager | admin | Code check |
|---|---|---|---|---|
| View own crew + request | ✓ | ✓ | ✓ | `crew.spec.owner == me` |
| Create request | self | self | self | `requester := me` (never on someone else's behalf, Decision 6), no open request, no crew (`crewName(me)` free) |
| Approve / deny | – | reports | all | same rule as `canDecide`; `decision.by := me`. A user with no manager has admins as the only approvers. |
| Start / stop crew | own | own + reports | all | owner, or owner's `account.manager == me`, or admin. Start sets `suspend=false` and `startedAt=now`; Stop sets `suspend=true` (Decision 1). |
| Open dashboard (sign-in) | own | own | own | `crew.spec.owner == me` **only**. Managers and admins cannot broker into someone else's crew. |
| See `status.tailnetLoginURL` | own | own | own | as above. It is a one-time node-auth URL. |
| Change schedule (3e) | own | own + reports | all | as Start / stop; applied directly (Decision 7) |
| Request a resize (3e) | own | own | own | `crew.spec.owner == me`, no open request; creates a `KiroCrewRequest` with `crewRef` (Decision 8) |
| Resize a report's crew (3e) | – | reports | all | creates the resize request with `decision.by := me` in one action |
| Change MCP config (3e) | – | – | ✓ | value ∈ `KiroCrewConfig.spec.mcpConfigs` or empty (Decision 11) |
| Accounts CRUD (roles, managers) | – | – | ✓ | cannot delete self or demote the last admin |
| Global config | – | – | ✓ | |

`decision.by` and `requester` are never read from form input. Handlers take
only `{verdict}` or `{size, schedule, reason}`. The identity fields are set from
the session before the patch. A test asserts that posting `by=` or `requester=`
is ignored.

### RBAC (console ServiceAccount)

Crews and requests live in one namespace, `console.crewNamespace`. That namespace
MUST hold only crew pods, because `pods/exec` below is namespace-wide.

| Scope | Resource | Verbs | Phase |
|---|---|---|---|
| ClusterRole | `kirocrewaccounts` | get, list, watch | 3a |
| ClusterRole | `kirocrewaccounts` | create (first-login sign-up, role `user` enforced in code) | 3a |
| ClusterRole | `kirocrewaccounts` | update, patch, delete | 3d |
| ClusterRole | `kirocrewconfigs` | get, list, watch | 3a |
| ClusterRole | `kirocrewconfigs` (resourceNames: `default`) | update, patch | 3d |
| Role (crewNamespace) | `kirocrews` | get, list, watch | 3a |
| Role (crewNamespace) | `kirocrews` | patch (only `suspend`/`startedAt`; code-enforced) | 3c |
| Role (crewNamespace) | `kirocrews` | patch, same verb: the code-enforced field set widens to `schedule` and `mcpConfigRef`, never `size` | 3e |
| Role (crewNamespace) | `kirocrewrequests` | get, list, watch | 3a |
| Role (crewNamespace) | `kirocrewrequests` | create, patch, delete (delete is "cancel my Pending request") | 3b |
| Role (crewNamespace) | `pods` | get, list (live reads, no watch) | 3c, `console.broker.enabled` |
| Role (crewNamespace) | `pods/exec` | create (the console execs over SPDY, a POST, so `get` is not needed) | 3c, `console.broker.enabled` |

No Secrets, no `pods/log`, no StatefulSets, no `impersonate`, no `escalate`.
The chart grants no other subject update or patch on `kirocrewrequests`
(Decision 4); that is what keeps `decision.by` unforgeable without an
admission policy.

## Sign-in brokering (the hard problem)

**Choice (Decision 2): (a) now. The console execs `kirocrew token`
server-side, limited to that one command, and 303-redirects the owner straight
to their crew. Pursue (b) upstream in parallel so (a) can be deleted later.**

### What the gateway actually does

Read from the KiroCrew gateway source (`kiro_crew/dashboard/token_auth.py`,
`tailnet.py`, `cli_server.py`, 0.9.0rc1). The image pins 0.8.0, so 3c MUST
re-verify each row against the pinned image.

| Fact | Consequence |
|---|---|
| `token_auth_middleware` resolves tailnet identity **only when a credential is present** (`?token=` or an `mc_token_*`/`mc_refresh_*` cookie). With none, it returns "Token required". | Identity is a constraint on a credential, never a credential. A credential-less browser cannot be bootstrapped without a gateway change. This is deliberate upstream: it stops header-driven `whois` spawns from unauthenticated callers. |
| With `trust_identity` + `allowed_logins`, a request carrying `?token=` whose daemon-verified login is not allowlisted is denied outright. | A minted link redeems only through the owner's own tailnet identity. The ACL's `autogroup:self` already keeps everyone else off the node. |
| `generate_token` registers the link nonce in the **running gateway's memory**, and the link window is short (`LINK_WINDOW_SECS`, 5 min). | A token cannot be minted offline or by another process holding the signing key, so the gateway process itself must mint it. |
| `kirocrew token --ttl` calls `GET /api/token/local` on loopback with `X-Local-Secret` read from `KIROCREW_HOME`. The gateway binds `127.0.0.1` and the crew NetworkPolicy denies all ingress. | Nothing outside the pod can reach the mint endpoint. In-pod exec is the only remote path. |
| `/api/tailnet/mobile/qr` mints for a peer, but requires an existing owner session. | Chicken-and-egg, so no help for a first sign-in. |

### Options

| | (a) console exec | (b) upstream identity bootstrap | (c) alternatives from the gateway code |
|---|---|---|---|
| Mechanism | Exec `kirocrew token --ttl <signIn.sessionTTL>` in container `gateway` of `kiro-crew-<crew>-0`. Parse the token from stdout in memory. Redirect to `status.dashboardURL + "/?token=…"`. | The gateway mints a session cookie for a credential-less request from a daemon-verified forwarded peer whose login is in `allowed_logins` (opt-in config). | Give the console the signing secret: forgeable sessions for every crew, and it fails anyway on the in-memory nonce. Mint via the operator and pass the token through a CR: already rejected. Reach `/api/token/local` over the network: blocked by the loopback bind and NetworkPolicy, and opening it would expose a mint endpoint. |
| User handles a token | Never | Never; the link is just `dashboardURL` | – |
| Token location | Console memory → one `Location` header → owner's browser | Nowhere | – |
| RBAC | `pods/exec` create on crewNamespace, narrowed by VAP (below) | None. Console only reads `status.dashboardURL`. | – |
| Works today | Yes | Needs an upstream release, and upstream must accept relaxing the credential-presence gate | No |
| Blast radius if console is compromised | Exec into every crew pod (Kiro tokens, workspaces), limited by VAP to one command | None beyond CR writes | – |

(c) produced nothing better. It did surface two facts that make (a) safe
enough: owner-only redemption and the in-process nonce.

### Narrowing (a)

A `ValidatingAdmissionPolicy` on `CONNECT pods/exec` in crewNamespace, matched
to the console ServiceAccount, admits only:

- `container == "gateway"`,
- `command == ["kirocrew","token","--ttl",<duration>]`,
- `stdin == false`, `tty == false`.

This policy is separate from the deferred decision-write policy (Decision 4)
and stays. Anything else is denied. A compromised console can then mint sign-in links,
which redeem only from the owner's own device. It cannot get a shell.

Verified in 3c on a real kube-apiserver (envtest, Kubernetes 1.37): a VAP matches
`CONNECT pods/exec` and sees the `PodExecOptions` object, including `command`, so it
can narrow the exec. `internal/console/broker/vap_test.go` denies a shell, extra
args, another `--ttl`, another container, stdin and a tty, and admits the one
command. Needs Kubernetes 1.30+ (VAP GA). The policy binds the console
ServiceAccount only. Not exercised: a live SPDY stream against a kubelet.

### Token path through the console's Ingress, private or public

| Hop | Control |
|---|---|
| Trigger | `POST /crews/{name}/open` with a CSRF token, from a session whose email equals `crew.spec.owner`. Rate limited to 6/min per session. |
| Exec | The API server audit log records the request URI (command args) but not stdout, so the token never reaches the audit log. The exec stream is read into memory and never written to disk or logs. |
| Target | The host is taken only from `status.dashboardURL`, which must be `https://` with a host ending `.<tailnet.domain>`. Otherwise the request is refused. |
| Response | `303`, `Location: <dashboardURL>/?token=<t>`, `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, and an empty body. |
| Logging | The broker handler's logger has no URL or header fields. A unit test asserts the token is absent from all log output. The access-log middleware logs method, path and status, never response headers. |
| Ingress | Common ingress controllers' default access logs (nginx, Traefik, AWS ALB) record the request line, not response headers. Docs warn against custom formats that log the response `Location` header. oauth2-proxy in front: confirm it does not log upstream response headers. |
| Browser | The URL lands in history. The link window is 5 min and the nonce is single-use. It redeems only via the owner's tailnet identity. |

### Option (b) upstream proposal (outline)

`dashboard.tailscale.identity_bootstrap: true` (default false). It is valid only
with `trust_identity` and a non-empty `allowed_logins`. A credential-less
top-level `GET /` forwarded by `tailscale serve` resolves the peer via `whois`.
If the login is allowlisted, the gateway mints the same identity-pinned
(`ts:login:` / `ts:node:`) session the token exchange would, rate-limited per
peer. When it lands, delete `broker`, the `pods/exec` grant and the VAP, and the
console links `dashboardURL` directly.

## Crew links and the tailnet

| Fact | Effect for the user |
|---|---|
| Console is reached through its Ingress, private or public; crews are tailnet-only (`autogroup:self`, tcp:8080 via serve) | Signing in to the console works from wherever that Ingress is reachable: anywhere if public, the internal network or VPN if private. **Opening a crew needs the Tailscale client connected as the owner.** Off the tailnet, `*.ts.net` does not resolve. |
| Console cannot see the browser's tailnet state | The Open button explains this up front ("Requires Tailscale, connected as you@example.com"). The console does not probe. |
| `status.dashboardURL` is empty until the node joins | The crew card shows `status.tailnetLoginURL` (owner only) as "Authenticate your crew node", then the Open button once joined. |
| First Kiro login (device code) happens inside the dashboard | The console shows a `KiroAuthenticated` condition hint. It does not broker it. |

## OAuth and sessions

| Item | Design |
|---|---|
| Flow | OIDC authorization code with PKCE (S256) against the provider's discovery doc. Google is the only implemented provider. |
| Request | `scope=openid email profile`, random `state` and `nonce`. `hd=` is sent only as a UI hint when exactly one domain is configured. It is never trusted. |
| Transient cookie | `__Host-kc_oauth`: sealed (`state`, `nonce`, PKCE verifier, return path), 10 min, Secure, HttpOnly, SameSite=Lax. Lax is required because the callback is a cross-site top-level GET from the provider. |
| ID token checks | Signature via JWKS, `iss`, `aud == clientID`, `exp`, `nonce`, `email_verified == true`, `hd ∈ auth.allowedDomains`, and `email` ends `@`+`hd`. A missing `hd` (consumer Gmail) is refused. `allowedDomains` is the only boundary on who gets an account (Decision 3). **An empty `allowedDomains` refuses every login.** The console also refuses to start with an empty list unless `auth.allowEmptyDomains` is set for testing, so the misconfiguration is loud. |
| Session cookie | `__Host-kc_session`: AES-GCM sealed `{email, iat, exp, sid}`, Secure, HttpOnly, SameSite=Lax, Path=/. Absolute expiry 8h, idle 1h (sliding re-seal). No server store. |
| Revocation | Role is re-read per request, so demoting takes effect immediately. With self sign-up, deleting an account no longer revokes sign-in: the next login recreates it as `user` with no manager. Blocking a person means removing their domain from `allowedDomains` or their Workspace account. Rotating `cookieKey` logs out everyone. |
| CSRF | Every unsafe method needs `X-CSRF-Token` (htmx `hx-headers` from a `<meta>`) or a form field equal to `HMAC(cookieKey, sid)`. Also Go's `http.CrossOriginProtection` (Origin / `Sec-Fetch-Site`). |
| Headers | CSP `default-src 'self'; frame-ancestors 'none'`, HSTS, `X-Content-Type-Options: nosniff`, `Cache-Control: no-store` on authed pages |
| Rate limits | Per session on writes and `/open`. The app does no per-IP limiting and does not read forwarded headers: behind an ingress the remote address is the ingress pod, so limiting `/auth/*` per IP is the ingress or gateway's job. |
| Secrets | Secret named by `console.auth.existingSecret`, with keys `client-id`, `client-secret` and `cookie-key` (32 bytes). Never in values.yaml. The chart fails if it is unset. |
| Other providers later | The config shape is `auth.provider` + `auth.issuerURL` + `auth.domainClaim` (Google: `hd`). Adding Entra or Okta later means a new claim mapping and no redesign. Not built now. |

## Global config

Two homes, split by plane and blast radius:

| Where | What | Why |
|---|---|---|
| **Chart values** (deployment plane) | `console.auth.allowedDomains`, `auth.provider`, `auth.issuerURL`, `auth.redirectURL`, `auth.existingSecret`, `crewNamespace`, ingress | Login policy must exist before any admin can sign in, so it cannot bootstrap from a CR the console edits. It is also the authentication and account-creation boundary (Decision 3): if it lived in a CR, one compromised admin session could widen sign-up to any domain. Changing it is a reviewed `helm upgrade`. |
| **`KiroCrewConfig`** (cluster-scoped singleton, name `default`, enforced by CEL `self.metadata.name == 'default'`) | Product defaults admins tune day to day | Read by both the console and the operator. Edited in the console in 3d. |

`KiroCrewConfig.spec`:

| Field | Type | Default | Used by |
|---|---|---|---|
| `allowedSizes` | `[]Size` | all four | console request form; request controller rejects others as `Invalid` |
| `defaultSize` | `Size` | `light` | request form |
| `defaultSchedule` | `*ScheduleSpec` | nil | request form |
| `gateway.image` | string | operator's compiled `defaultGatewayImage` | crew controller, live (lets admins roll the fleet) |
| `mcpConfigRef` | string | "" | crew controller, when `KiroCrew.spec.mcpConfigRef` is empty |
| `mcpConfigs` | `[]string` | [] | console MCP picker (3e); ConfigMap names in crewNamespace |
| `signIn.sessionTTL` | `metav1.Duration` | 8h, max 20h | broker's `--ttl` |

Precedence: `KiroCrew.spec` > `KiroCrewConfig` > compiled default. Size and
schedule are snapshotted into the KiroCrew when the request is provisioned.
Images and the MCP ref are read live.

## Packaging (chart values)

```yaml
console:
  enabled: false
  replicas: 2
  image: {repository: ghcr.io/example-org/kiro-crew-console, tag: "", pullPolicy: IfNotPresent}
  crewNamespace: kiro-crews
  auth:
    provider: google
    issuerURL: https://accounts.google.com
    redirectURL: https://console.example.com/auth/callback
    allowedDomains: []          # e.g. [example.com]; empty refuses all logins
    existingSecret: ""          # keys: client-id, client-secret, cookie-key
  ingress:
    enabled: false
    className: ""
    host: console.example.com
    annotations: {}             # WAF, IP allowlist, /auth rate limit, oauth2-proxy, cert-manager issuer
    tls:
      enabled: true
      secretName: ""            # supplied cert, or let cert-manager fill it
  resources: {requests: {cpu: 10m, memory: 64Mi}, limits: {memory: 128Mi}}
```

Exposure is the operator's choice (Decision 5): the console's Ingress, private
or public, is set entirely by `ingress.className` and `ingress.annotations`.
The docs recommend private and present public as the opt-in. The Google
callback is a browser redirect, so a private host works; the console Pod needs
egress to the provider's discovery and JWKS endpoints either way.

Private, AWS ALB with the internal scheme (TLS terminates at the ALB via ACM). One example controller, not a requirement:

```yaml
console:
  auth:
    redirectURL: https://console.internal.example.com/auth/callback
  ingress:
    enabled: true
    className: alb
    host: console.internal.example.com   # private DNS zone
    annotations:
      alb.ingress.kubernetes.io/scheme: internal
      alb.ingress.kubernetes.io/target-type: ip
      alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'
      alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:eu-west-1:111122223333:certificate/00000000-0000-0000-0000-000000000000
    tls:
      enabled: true
```

Public, with cert-manager TLS. nginx is one example controller, not a requirement:

```yaml
console:
  auth:
    redirectURL: https://console.example.com/auth/callback
  ingress:
    enabled: true
    className: nginx
    host: console.example.com
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt
      nginx.ingress.kubernetes.io/whitelist-source-range: 203.0.113.0/24  # optional allowlist
    tls:
      enabled: true
      secretName: console-example-com-tls
```

Templates: `templates/console/{deployment,service,ingress,serviceaccount,rbac,vap}.yaml`.
There is no console NetworkPolicy; adding one is left to the operator for now. The Pod runs
non-root with a read-only rootfs and drops ALL capabilities. `tailnet.domain` is reused for redirect validation.

## Threat model

Every control below applies whether the console's Ingress is private or
public. A private ingress narrows who can reach the login page, but SSO stays
the authentication step regardless.

| Threat | Mitigation |
|---|---|
| Login from outside the org | `hd` ∈ allowlist, `email_verified`, email matches `hd`; fail closed on an empty list |
| Account creation by anyone in an allowed domain | Self sign-up (Decision 3) makes `auth.allowedDomains` the only boundary on who gets an account, so it lives in chart values (a reviewed `helm upgrade`), not a CR. A new account is role `user` with no manager (set in code, not from input), can only request a crew, and only admins can approve it until a manager is set. Nothing is provisioned before approval. |
| Forged or replayed ID token | go-oidc signature, `aud`, `iss`, `exp` and `nonce` checks; PKCE; single-use `state` bound to the transient cookie |
| Session theft | `__Host-` sealed cookie, Secure, HttpOnly, SameSite=Lax; 8h absolute and 1h idle; TLS only (HSTS) |
| CSRF (approve, start, open) | Synchronizer token + `CrossOriginProtection`; SameSite=Lax; all state changes are POST |
| Privilege escalation by form tampering | `requester` and `decision.by` come from the session; role is re-read from the CR per request. Residual risk (Decision 4, no admission policy for now): anyone with update or patch on `kirocrewrequests` can forge `decision.by`, so chart RBAC grants that only to the console SA, cluster admins can approve through the Kubernetes API, and the controller's `canDecide` stays defence in depth. |
| Manager approves a non-report | Code check plus the operator's `canDecide` (marks the request `Invalid`). A user with no manager has no manager-approver; only admins can approve. |
| Owner grants their own crew more resources | `size` is written only by the request controller after an approved request; the console's crew patch never carries it; request spec is immutable after creation |
| Sign-in token leak | Only in one `Location` header to the verified owner; never logged or stored; 5 min, single-use, and redeemable only via the owner's tailnet identity |
| Console compromise | VAP limits exec to `kirocrew token`; no Secrets access; crews stay tailnet-gated, so a minted link is useless to an attacker who is not the owner |
| Brute force / DoS on the console's Ingress, private or public | Per-session limits on writes and `/open`, per-IP limits on `/auth/*` at the ingress or gateway, 2 stateless replicas |
| Ingress header spoofing | The app trusts no forwarded headers |
| SSO is the single factor | Accepted (Decision 5). Workspace-level MFA is the layer above it; a private ingress is recommended in the docs and an ingress allowlist is optional. |

## Editing a running crew (3e)

Three fields of a running `KiroCrew` become editable. Everything else in its
spec stays set at provisioning or by an admin through the Kubernetes API.

| Field | Who | Path | Decision |
|---|---|---|---|
| `spec.schedule` | owner, owner's manager, admin | Console patches the crew directly | 7 |
| `spec.size` | owner asks; manager of the owner or admin approves | `KiroCrewRequest` with `spec.crewRef`; the request controller patches the crew | 8 |
| `spec.mcpConfigRef` | admin | Console patches the crew directly, from the `mcpConfigs` allowlist | 11 |

### Resize through the request flow

The request flow fits with one field. `KiroCrewRequestSpec.crewRef` (optional)
turns a request into a resize of an existing crew instead of a new one:

- The console sets `crewRef` to the crew being resized and `size`, never `schedule` (CEL: `!has(self.crewRef) || !has(self.schedule)`). The owner files for their own crew and it stays `Pending`, including for an admin resizing their own crew.
- A manager or admin filing for a report's crew sets `requester` to the crew's owner and `decision.by` to the filer, and annotates the request `kirocrew.lsdopen.io/filed-by: <filer email>` so the record shows who asked.
- Approval is unchanged: `decision`, `canDecide`, `decision.by` from the session.
- On approval the request controller gets the crew. It marks the request `Invalid` with `CrewNotFound` if the crew is missing, or `NotOwner` if `crew.spec.owner` is not the requester. A size outside `allowedSizes` is `Invalid` as today. Otherwise it patches `crew.spec.size`, sets `status.crewRef`, and moves to a new terminal phase, `Applied`. `requestsForAccount` treats `Applied` as terminal like `Provisioned`.
- One spec-level CEL transition rule makes `requester`, `size`, `schedule` and `crewRef` immutable. This is CRD schema validation, not the deferred admission policy (Decision 4): it stops an approved request's size being swapped, while `decision` and `reason` stay writable.
- If `spec.gateway.resources` or `spec.storage.size` is set, it still wins over the tier (`resolveSize`). The console shows "Resources pinned by an admin" and disables the resize.

### Enforcement

No admission policy guards requests or crews (Decision 4), so enforcement is the
same two layers as 3b and 3c:

| Layer | What it enforces |
|---|---|
| Console code | Per-field authz from the table above. The crew patch is built from an allowlist of fields (`suspend`, `startedAt`, `schedule`, `mcpConfigRef`) and never carries `size`; a test asserts it. Values come only from `{schedule}`, `{mcpConfigRef}` or `{size, reason}` form fields. |
| RBAC | Only the console SA and the operator may write crews and requests. RBAC cannot narrow `patch` by field, which is why the field set is code-enforced. `size` is written only by the operator's request controller, so a console bug cannot grant a size without an approved request. |
| Operator | `canDecide` and the owner check on resize stay as defence in depth. |

### Effect on the pod and disk

| Change | Running pod | Disk (PVC) |
|---|---|---|
| Schedule | Pod template unchanged. The crew controller recomputes desired state (latest event wins, Decision 1), so the edit may start or stop the crew at once. The form warns "Saving can start or stop the crew at once"; there is no per-save preview. | Untouched |
| Size, crew running | New resources in the pod template, so the StatefulSet replaces the pod: a short outage, and in-flight sessions end. The pod may stay `Pending` if no node fits; the crew card shows it. | Kept. The crew controller patches the data PVC to the new tier's storage when larger (`volumeClaimTemplates` are immutable, so the template keeps its original size). Never shrunk. If the StorageClass does not allow expansion, CPU and memory still change and the crew reports `StorageResized=False`. |
| Size, crew stopped | Applied at the next start | As above |
| MCP config ref | The volume source changes, so the pod is replaced | Untouched |

Editing an MCP ConfigMap's content stays a deployment-plane change (`helm upgrade`), not a console action.

### Operator RBAC added

| Controller | Resource | Verbs |
|---|---|---|
| request controller | `kirocrews` | patch (adds to get, list, watch, create) |
| request controller | `kirocrewconfigs` | get, list, watch, for `allowedSizes`, unless 3d already grants it |
| crew controller | `persistentvolumeclaims` | patch, already granted |

## Build plan

| Phase | Delivers | RBAC added | Exit test |
|---|---|---|---|
| **3a** login + read-only | OIDC login, sessions, CSRF scaffold, account resolution with first-login sign-up (role `user`, no manager), read-only views of own crew / requests (user), reports' (manager), all (admin); crew card with `dashboardURL` link and owner-only `tailnetLoginURL`; image, chart, ingress, release step; `KiroCrewConfig` CRD (read-only) | accounts create + read, configs/crews/requests read | envtest + httptest: empty-domain refusal, wrong `hd`, missing `email_verified`, first login creates a `user` account with no manager, role views |
| **3b** requests + approvals | Request form (sizes, optional schedule), cancel own Pending request, approve/deny with `decision.by` from session (no admission policy, Decision 4); per-session write rate limits. `allowedSizes`/`defaultSize` wait for the `KiroCrewConfig` editor in 3d | requests create, patch, delete | posted `by=`/`requester=` ignored; non-manager approval → 403; a no-manager user's request is approvable only by an admin |
| **3c** start/stop + sign-in | `startedAt` field + `computeDesired`/`inUpWindow` change: latest of last Up, last Down, `startedAt` wins, requeue includes next Down after `startedAt` (Decision 1); start/stop; broker with exec VAP; re-verify gateway facts on the pinned image | crews patch, pods get, pods/exec | the token never appears in logs (captured logger); the VAP denies `sh`; non-owner `/open` → 403; a redirect host outside `tailnet.domain` → refused |
| **3d** admin config | Accounts CRUD (last-admin guard), `KiroCrewConfig` editor; crew controller reads images and MCP ref from config | accounts write, config `default` write | demote-last-admin refused; config change rolls gateway image on the next reconcile |
| **3e** edit a crew | Edit form on the crew card: schedule (direct), resize via a `crewRef` request with an `Applied` phase, MCP config from the `mcpConfigs` allowlist (admin); CEL immutability on request spec; crew controller grows the data PVC on resize | console: no new verbs (crews patch field set widens); operator: crews patch, configs read | console patch never carries `size`; a resize of someone else's crew → `Invalid`; an approved resize replaces the pod and grows the PVC, a downsize leaves it; a swapped size after approval is rejected; a non-admin MCP change → 403 |
