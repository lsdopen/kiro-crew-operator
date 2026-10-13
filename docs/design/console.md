# Design: management console (phase 3)

Status: proposed. Scope: a web console for requesting, approving, starting,
stopping and opening Kiro Crews, with Google Workspace SSO. No implementation
lands with this document.

## Decisions needed

Only what is still open. Each has a recommended answer; everything else in this
document is decided.

1. **How does "Start" work outside a crew's schedule?** Today `spec.suspend`
   only stops (`computeDesired` in `internal/controller/schedule.go` returns
   replicas 0 for suspend, otherwise follows the schedule), so a user cannot
   start a scheduled crew at 21:00. *Recommend:* add
   `KiroCrewSpec.runUntil *metav1.Time`. "Start" sets `suspend=false` and
   `runUntil` = the next schedule Down (or now+8h with no schedule).
   `computeDesired` treats `now < runUntil` as up. "Stop" sets `suspend=true`;
   "Resume schedule" clears both.
2. **Accept sign-in brokering by `pods/exec` now (option a), and pursue the
   upstream identity bootstrap (option b) in parallel to remove it later?**
   The console ServiceAccount could then exec into every crew pod in the crew
   namespace, narrowed by an admission policy to one command. *Recommend:* yes.
3. **What does a Google user from an allowed domain with no `KiroCrewAccount`
   see?** *Recommend:* a "no account, ask an admin" page and nothing else.
   Do not self-register: the manager link is the approval boundary, and only
   an admin should set it.
4. **Who may write `spec.decision` outside the console?** *Recommend:* a
   ValidatingAdmissionPolicy that allows changes to `spec.decision` only from
   the console ServiceAccount plus a configurable break-glass group
   (`console.decisionBreakGlassGroups`, default `[]`). That turns the
   request controller's `canDecide` from a hint into a guarantee.
5. **Should the chart docs recommend a private ingress as the default, with
   public as the opt-in?** The console holds exec into every crew. Its
   Ingress may be private (internal load balancer, VPN-only, private DNS) or
   public; that is the operator's choice. *Recommend:* yes in the docs, not in
   the chart defaults. The docs lead with the private example and present
   public as an opt-in, with an IP allowlist or WAF suggested (not required).
   A private console costs users little, since opening a crew already needs
   the tailnet. The chart itself stays exposure-neutral (`ingress.enabled:
   false`, empty `className` and `annotations`), because it cannot know a
   cluster's controllers or load balancer scheme. SSO stays the only
   *authentication* step either way, as decided, and the threat model assumes
   neither a private network nor an allowlist.
6. **Can users request on someone else's behalf?** *Recommend:* no.
   `spec.requester` is always the session email. An admin who wants a crew
   for someone creates the account and that person requests it.

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
| Requests, approvals, start/stop, one-click dashboard sign-in | Editing a running crew's size or MCP config |
| Admin: accounts, global config | Kiro login or tailnet auth on the user's behalf. Both stay human steps (`docs/tailnet.md`) |

## Architecture

```
browser ──https──> Ingress, private or public (class, TLS, annotations: WAF/allowlist/oauth2-proxy)
                     │
                     ▼
            kiro-crew-console (Deployment, 2 replicas, no sidecar)
              │ informers: KiroCrewAccount, KiroCrewRequest, KiroCrew, KiroCrewConfig
              │ writes: requests (create, decision), crews (suspend/runUntil), accounts, config
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
| SA + code checks | Expresses "manager of the requester" and "owner of this crew", which are relations between CR fields. Needs no per-person RBAC objects. | The console code is the enforcement point, so it needs tests and admission backstops. |
| Impersonation | The API server enforces every call | RBAC cannot say "requests whose `requester`'s account names me as manager". You would need a generated RoleBinding per person with `resourceNames`, a controller to keep them in sync, and `impersonate` on users, which is a stronger grant than everything it replaces. |

Every request resolves `session.email` to a KiroCrewAccount (case-insensitive,
like `findAccount`) from the informer cache. Role is re-read on every request,
so deleting or demoting an account takes effect immediately.

| Action | user | manager | admin | Code check |
|---|---|---|---|---|
| View own crew + request | ✓ | ✓ | ✓ | `crew.spec.owner == me` |
| Create request | self | self | self | `requester := me`, no open request, no crew (`crewName(me)` free) |
| Approve / deny | – | reports | all | same rule as `canDecide`; `decision.by := me` |
| Start / stop crew | own | own + reports | all | owner, or owner's `account.manager == me`, or admin |
| Open dashboard (sign-in) | own | own | own | `crew.spec.owner == me` **only**. Managers and admins cannot broker into someone else's crew. |
| See `status.tailnetLoginURL` | own | own | own | as above. It is a one-time node-auth URL. |
| Accounts CRUD | – | – | ✓ | cannot delete self or demote the last admin |
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
| ClusterRole | `kirocrewaccounts` | create, update, patch, delete | 3d |
| ClusterRole | `kirocrewconfigs` | get, list, watch | 3a |
| ClusterRole | `kirocrewconfigs` (resourceNames: `default`) | update, patch | 3d |
| Role (crewNamespace) | `kirocrews` | get, list, watch | 3a |
| Role (crewNamespace) | `kirocrews` | patch (only `suspend`/`runUntil`; code-enforced) | 3c |
| Role (crewNamespace) | `kirocrewrequests` | get, list, watch | 3a |
| Role (crewNamespace) | `kirocrewrequests` | create, patch | 3b |
| Role (crewNamespace) | `pods` | get | 3c |
| Role (crewNamespace) | `pods/exec` | create (+ get for the WebSocket upgrade on API servers that authorize it as get) | 3c |

No Secrets, no `pods/log`, no StatefulSets, no `impersonate`, no `escalate`.

## Sign-in brokering (the hard problem)

**Choice: (a) now. The console execs `kirocrew token` server-side and
303-redirects the owner straight to their crew. Pursue (b) upstream so (a) can
be deleted later.**

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

Anything else is denied. A compromised console can then mint sign-in links,
which redeem only from the owner's own device. It cannot get a shell.
VAP supports `CONNECT`; 3c verifies it on the target cluster version before
granting `pods/exec`.

### Token path through the console's Ingress, private or public

| Hop | Control |
|---|---|
| Trigger | `POST /crews/{name}/open` with a CSRF token, from a session whose email equals `crew.spec.owner`. Rate limited to 6/min per session. |
| Exec | The API server audit log records the request URI (command args) but not stdout, so the token never reaches the audit log. The exec stream is read into memory and never written to disk or logs. |
| Target | The host is taken only from `status.dashboardURL`, which must be `https://` with a host ending `.<tailnet.domain>`. Otherwise the request is refused. |
| Response | `303`, `Location: <dashboardURL>/?token=<t>`, `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, and an empty body. |
| Logging | The broker handler's logger has no URL or header fields. A unit test asserts the token is absent from all log output. The access-log middleware logs method, path and status, never response headers. |
| Ingress | The default nginx and Traefik access logs, and AWS ALB access logs, record the request line, not response headers. Docs warn against custom formats that log `$sent_http_location`. oauth2-proxy in front: confirm it does not log upstream response headers. |
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
| ID token checks | Signature via JWKS, `iss`, `aud == clientID`, `exp`, `nonce`, `email_verified == true`, `hd ∈ auth.allowedDomains`, and `email` ends `@`+`hd`. A missing `hd` (consumer Gmail) is refused. **An empty `allowedDomains` refuses every login.** The console also refuses to start with an empty list unless `auth.allowEmptyDomains` is set for testing, so the misconfiguration is loud. |
| Session cookie | `__Host-kc_session`: AES-GCM sealed `{email, iat, exp, sid}`, Secure, HttpOnly, SameSite=Lax, Path=/. Absolute expiry 8h, idle 1h (sliding re-seal). No server store. |
| Revocation | Role and account are re-read per request, so deleting the account revokes it. Rotating `cookieKey` logs out everyone. |
| CSRF | Every unsafe method needs `X-CSRF-Token` (htmx `hx-headers` from a `<meta>`) or a form field equal to `HMAC(cookieKey, sid)`. Also Go's `http.CrossOriginProtection` (Origin / `Sec-Fetch-Site`). |
| Headers | CSP `default-src 'self'; frame-ancestors 'none'`, HSTS, `X-Content-Type-Options: nosniff`, `Cache-Control: no-store` on authed pages |
| Rate limits | Per client IP on `/auth/*` (from `X-Forwarded-For`, trusted only from `console.trustedProxies` CIDRs), per session on writes and `/open`. Ingress annotations can add more. |
| Secrets | Secret named by `console.auth.existingSecret`, with keys `client-id`, `client-secret` and `cookie-key` (32 bytes). Never in values.yaml. The chart fails if it is unset. |
| Other providers later | The config shape is `auth.provider` + `auth.issuerURL` + `auth.domainClaim` (Google: `hd`). Adding Entra or Okta later means a new claim mapping and no redesign. Not built now. |

## Global config

Two homes, split by plane and blast radius:

| Where | What | Why |
|---|---|---|
| **Chart values** (deployment plane) | `console.auth.allowedDomains`, `auth.provider`, `auth.issuerURL`, `auth.redirectURL`, `auth.existingSecret`, `crewNamespace`, `trustedProxies`, ingress | Login policy must exist before any admin can sign in, so it cannot bootstrap from a CR the console edits. It is also an authentication boundary: if it lived in a CR, one compromised admin session could widen login to any domain. Changing it is a reviewed `helm upgrade`. |
| **`KiroCrewConfig`** (cluster-scoped singleton, name `default`, enforced by CEL `self.metadata.name == 'default'`) | Product defaults admins tune day to day | Read by both the console and the operator. Edited in the console in 3d. |

`KiroCrewConfig.spec`:

| Field | Type | Default | Used by |
|---|---|---|---|
| `allowedSizes` | `[]Size` | all four | console request form; request controller rejects others as `Invalid` |
| `defaultSize` | `Size` | `light` | request form |
| `defaultSchedule` | `*ScheduleSpec` | nil | request form |
| `gateway.image` | string | operator's compiled `defaultGatewayImage` | crew controller, live (lets admins roll the fleet) |
| `tailscale.image` | string | compiled `defaultTailscaleImage` | crew controller, live |
| `mcpConfigRef` | string | "" | crew controller, when `KiroCrew.spec.mcpConfigRef` is empty |
| `signIn.sessionTTL` | `metav1.Duration` | 8h, max 20h | broker's `--ttl` |
| `runUntilDefault` | `metav1.Duration` | 8h | "Start" with no schedule (Decision 1) |

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
  trustedProxies: []            # CIDRs of the ingress controller
  decisionBreakGlassGroups: []  # Decision 4
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
    annotations: {}             # WAF, IP allowlist, oauth2-proxy, cert-manager issuer
    tls:
      enabled: true
      secretName: ""            # supplied cert, or let cert-manager fill it
  resources: {requests: {cpu: 10m, memory: 64Mi}, limits: {memory: 128Mi}}
```

Exposure is the operator's choice: the console's Ingress, private or public,
is set entirely by `ingress.className` and `ingress.annotations`. The Google
callback is a browser redirect, so a private host works; the console Pod needs
egress to the provider's discovery and JWKS endpoints either way.

Private, AWS ALB with the internal scheme (TLS terminates at the ALB via ACM):

```yaml
console:
  trustedProxies: [10.0.0.0/16]          # VPC CIDR the ALB ENIs sit in
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

Public, ingress-nginx with cert-manager TLS:

```yaml
console:
  trustedProxies: [10.42.0.0/16]         # pod CIDR of the ingress-nginx controller
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

Templates: `templates/console/{deployment,service,ingress,serviceaccount,rbac,vap}.yaml`,
plus a NetworkPolicy allowing ingress only from the ingress controller
namespace (with an ALB in `ip` mode, traffic arrives from the ALB's subnets,
so the policy admits the `trustedProxies` CIDRs instead). The Pod runs non-root with a read-only rootfs and drops ALL
capabilities. `tailnet.domain` is reused for redirect validation.

## Threat model

Every control below applies whether the console's Ingress is private or
public. A private ingress narrows who can reach the login page, but SSO stays
the authentication step regardless.

| Threat | Mitigation |
|---|---|
| Login from outside the org | `hd` ∈ allowlist, `email_verified`, email matches `hd`; fail closed on an empty list; no account means no access |
| Forged or replayed ID token | go-oidc signature, `aud`, `iss`, `exp` and `nonce` checks; PKCE; single-use `state` bound to the transient cookie |
| Session theft | `__Host-` sealed cookie, Secure, HttpOnly, SameSite=Lax; 8h absolute and 1h idle; TLS only (HSTS) |
| CSRF (approve, start, open) | Synchronizer token + `CrossOriginProtection`; SameSite=Lax; all state changes are POST |
| Privilege escalation by form tampering | `requester` and `decision.by` come from the session; role is re-read from the CR per request; a VAP rejects `spec.decision` writes from anyone except the console SA or break-glass (Decision 4) |
| Manager approves a non-report | Code check plus the operator's `canDecide` (marks the request `Invalid`) |
| Sign-in token leak | Only in one `Location` header to the verified owner; never logged or stored; 5 min, single-use, and redeemable only via the owner's tailnet identity |
| Console compromise | VAP limits exec to `kirocrew token`; no Secrets access; crews stay tailnet-gated, so a minted link is useless to an attacker who is not the owner |
| Brute force / DoS on the console's Ingress, private or public | Per-IP limits on `/auth/*`, per-session limits on writes and `/open`, ingress-level limits via annotations, 2 stateless replicas |
| Ingress header spoofing | `X-Forwarded-For` trusted only from `trustedProxies`; NetworkPolicy admits only the ingress controller |
| SSO is the single factor | Accepted (Decision 5). Workspace-level MFA and, optionally, a private ingress or an ingress allowlist are the layers above it. |

## Build plan

| Phase | Delivers | RBAC added | Exit test |
|---|---|---|---|
| **3a** login + read-only | OIDC login, sessions, CSRF scaffold, account resolution, read-only views of own crew / requests (user), reports' (manager), all (admin); crew card with `dashboardURL` link and owner-only `tailnetLoginURL`; image, chart, ingress, release step; `KiroCrewConfig` CRD (read-only) | accounts/configs/crews/requests read | envtest + httptest: empty-domain refusal, wrong `hd`, missing `email_verified`, no-account page, role views |
| **3b** requests + approvals | Request form (`allowedSizes`, defaults), approve/deny with `decision.by` from session; decision VAP; request controller honours `allowedSizes` | requests create, patch | posted `by=`/`requester=` ignored; non-manager approval → 403; VAP denies a kubectl decision by a non-break-glass user |
| **3c** start/stop + sign-in | `runUntil` field + `computeDesired` change (Decision 1); start/stop; broker with exec VAP; re-verify gateway facts on the pinned image | crews patch, pods get, pods/exec | the token never appears in logs (captured logger); the VAP denies `sh`; non-owner `/open` → 403; a redirect host outside `tailnet.domain` → refused |
| **3d** admin config | Accounts CRUD (last-admin guard), `KiroCrewConfig` editor; crew controller reads images and MCP ref from config | accounts write, config `default` write | demote-last-admin refused; config change rolls gateway image on the next reconcile |
