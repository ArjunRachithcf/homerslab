# Spike: Kanidm 1.12 with oauth2-proxy, held to the identity provider contract

- **Question**: can Kanidm 1.12.x meet HomersLab's identity provider contract
  ([spec](../spec.md) "Adapter contracts", [ADR 0004](../adr/0004-two-identity-providers-one-contract.md)) by API, with
  nothing clicked? The parts are:
  - per-app OIDC clients;
  - a forward-auth guard through oauth2-proxy in front of Caddy's `forward_auth`;
  - read-only LDAPS for an app;
  - groups for the roles adult and kid, and audiences;
  - step-up (`prompt=login` / `max_age=0`).

  This is spec "Open spikes" no. 3.
- **Checked**: 2026-11-09 and 2026-11-10, on the maintainer's Fedora 44 PC, in its own compose project
  (`hlspike-kanidm`, network `10.231.77.0/24`). Ports were published on 127.0.0.1 only. No sudo, no `/etc/hosts`,
  nothing of the live `homelab` project touched. Torn down afterwards with `docker compose -p hlspike-kanidm down -v`.
- **Answer**: yes, every operation works by API. But the edge has to pass oauth2-proxy's refreshed cookie, and the
  adapter has to cover three gaps.
  - **Proven by API.**
    - Clients, the guard, LDAPS, people, groups and step-up all worked by API.
    - The only commands run inside a container were `kanidmd cert-generate` and `kanidmd recover-account`. After
      that, one service-account token did everything.
    - Persons signed in through Kanidm's own pages in headless Chromium: password + TOTP for four persons, a passkey
      on a virtual authenticator for one.
  - **The edge must pass oauth2-proxy's refreshed cookie.**
    - Caddy's `forward_auth` shortcut drops the `Set-Cookie` that oauth2-proxy sends on its 202 after refreshing a
      session.
    - Kanidm rotates refresh tokens and destroys a session when an old one is replayed. So every guarded session went
      back through Kanidm at its second refresh (twice in 200 s with `cookie-refresh=1m`).
    - The guard written out with `copy_response_headers { include Set-Cookie }` fixed it: no bounce, and the cookie
      was refreshed in place. HomersLab writes the edge's JSON anyway.
  - **Gap 1: deleting a client ends live guard sessions only when they expire** (904 s measured).
    - After the delete, Kanidm's token endpoint answers 401 with an empty body, which oauth2-proxy treats as a
      passing error.
    - Removing a person, or a person's role, ends the session at the next refresh (61 s).
  - **Gap 2: narrowing an audience doesn't end live sessions.** Kanidm doesn't re-check scope maps when it refreshes.
    A live session survived 160 s and two refreshes after its scope map was gone, while a fresh sign-in was refused.
  - **Gap 3: LDAP needs extra rights.** By default, a service account sees no persons over LDAP and no groups at all.
    Per-app passwords for LDAP binds can only be created by the person.
  - **Step-up is honoured.** `amr`/`acr` are always null. A passkey-only account policy is enforced when a credential
    is enrolled, not at sign-in.
- **Evidence**:
  - outputs, with no secrets: [`spikes/kanidm-forward-auth/results/`](../../spikes/kanidm-forward-auth/results/);
  - the setup: [`spikes/kanidm-forward-auth/`](../../spikes/kanidm-forward-auth/), whose test credentials stay in its
    git-ignored `secrets/`.

## Versions

| Image or tool | Version | Digest |
|---|---|---|
| `kanidm/server` | 1.12.0 (2026-11-01, the latest release) | `sha256:71b6ff5a9c9c32b632894d8acef5a2afc77941b35a0aa95dcee959fe688d5ce9` |
| `kanidm/tools` | 1.12.0 | `sha256:9147fe75e7d2c47dbe46df5431255ab7abb05d22f404d16cd99cec27adedd38a` |
| `quay.io/oauth2-proxy/oauth2-proxy` | v7.15.6 (2026-11-05, go1.26.10) | `sha256:c3415e8660af553f5e18f5d90db050614c02493c0ac6260ac59442f6d9b12048` |
| `caddy` | 2.11.9-alpine | `sha256:0bbb055bda6793c999bde78b1b5ce3e89f829fb031af2cf50056ea1246559fee` |
| `traefik/whoami` | v1.12.0 | `sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44` |
| `alpine` (openldap-clients 2.6.8, openssl) | 3.22 | `sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f` |
| Playwright | 1.63.0, with the cached Chromium build 1243 | – |

Releases: [Kanidm](https://github.com/kanidm/kanidm/releases/tag/v1.12.0),
[oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy/releases/tag/v7.15.6),
[Caddy](https://github.com/caddyserver/caddy/releases/tag/v2.11.9). The host ran Docker Engine 29.8.1 and Compose
v5.5.1.

## The setup

([compose.yaml](../../spikes/kanidm-forward-auth/compose.yaml),
[Caddyfile](../../spikes/kanidm-forward-auth/caddy/Caddyfile),
[server.toml](../../spikes/kanidm-forward-auth/kanidm/server.toml))

| Service | Address | Role |
|---|---|---|
| `kanidm` | `10.231.77.10`, alias `idm.hlspike.test`, published `127.0.0.1:28743` (HTTPS); LDAPS `3636` unpublished | the identity provider |
| `caddy` | `10.231.77.11`, published `127.0.0.1:28744` | the edge: `app.hlspike.test` and `app2.hlspike.test` |
| `oauth2-proxy` | `10.231.77.12` | the guard of `app.hlspike.test` (client `whoami`) |
| `oauth2-proxy-b` | `10.231.77.14` | the guard of `app2.hlspike.test` (client `whoami2`) |
| `whoami` | `10.231.77.13` | the guarded upstream, which echoes the request headers |
| `ldapclient`, `tools`, `helper` | – | one-offs: `ldapsearch`, the `kanidm` CLI, copying the CA |

**Names without `/etc/hosts`.**
- Chromium mapped `*.hlspike.test` to 127.0.0.1 with `--host-resolver-rules`; curl used `--resolve`, and the Python
  helper patched `getaddrinfo`.
- Inside the network, a network alias makes `idm.hlspike.test` resolve to Kanidm. Kanidm listens on 28743 inside too,
  so the issuer URL is the same for the browser and for oauth2-proxy.

**TLS.**
- `kanidmd cert-generate` is the documented evaluation way
  ([quickstart](https://kanidm.github.io/kanidm/stable/evaluation_quickstart.html)). It wrote a CA and a server
  certificate for `DNS:idm.hlspike.test`, valid for 5 days.
- The CA certificate alone was copied into its own volume, and oauth2-proxy (`--provider-ca-file`) and `ldapsearch`
  (`LDAPTLS_CACERT`) verified against it.
- Caddy used `tls internal`.
- Two surprises:
  - `cert-generate` doesn't create the directory of `tls_chain`. The flat `/data/chain.pem` path from the docs works.
  - The CA lands next to the chain, inside the data volume.

**Bootstrap** ([bootstrap.py](../../spikes/kanidm-forward-auth/scripts/bootstrap.py),
[01-bootstrap.txt](../../spikes/kanidm-forward-auth/results/01-bootstrap.txt)):
1. `docker compose exec kanidm kanidmd recover-account admin`, then the same for `idm_admin`. It prints
   `new_password: …`, which went into `secrets/`. `kanidmd scripting recover-account` gives the same as
   machine-readable output.
2. `idm_admin` signed in over REST (`POST /v1/auth`: `init2` → `begin: password` → `cred`). It then:
   - created the service account `homerslab`;
   - put it in `idm_people_admins`, `idm_group_admins`, `idm_oauth2_admins`, `idm_service_account_admins` and
     `idm_account_policy_admins` (later also `idm_application_admins`);
   - minted a read-write API token (`POST /v1/service_account/homerslab/_api_token`, `read_write: true`).
3. Everything after that used only `Authorization: Bearer <that token>`. Being in admin groups taints the account
   with `idm_high_privilege`.

The `kanidm` CLI isn't scriptable for this: `kanidm login` fails without a TTY ("Failed to create password prompt …
not a terminal"). The adapter should use the REST API, which the CLI uses itself
([v1.rs routes](https://github.com/kanidm/kanidm/blob/v1.12.0/server/core/src/https/v1.rs),
[client library](https://github.com/kanidm/kanidm/tree/v1.12.0/libs/client/src)).

**Test persons and their credentials** ([setup.py](../../spikes/kanidm-forward-auth/scripts/setup.py),
[02-setup.txt](../../spikes/kanidm-forward-auth/results/02-setup.txt)):
- `ada` is an adult (group `adults`), and `kim` is a kid (group `kids`). `household` is a group whose members are the
  groups `adults` and `kids`.
- Later persons:
  - `ada2` (adult), for the removal test;
  - `ada3` (adult), for the role-change test;
  - `pat` (adult), with a passkey;
  - `adm1`, for the account-policy test.
- **Password + TOTP for `ada`, `kim`, `ada2`, `ada3`, `adm1`.** Each was enrolled the way an invite works, all by
  REST:
  1. the manager asks for a credential-update intent (`GET /v1/person/{id}/_credential/_update_intent/{ttl}`);
  2. the intent is exchanged (`POST /v1/credential/_exchange_intent`);
  3. then `{"password": …}`, `"totpgenerate"`, `{"totpverify": [code, label]}` (`POST /v1/credential/_update`), and
     `POST /v1/credential/_commit`.

  A password alone can't be committed: `warnings=['MfaRequired'] can_commit=False`, because `idm_all_persons`
  defaults to `credential_type_minimum = mfa`
  ([account policy](https://kanidm.github.io/kanidm/stable/accounts/account_policy.html)). TOTP is SHA-256, 30 s, 6
  digits. Kanidm's sign-in asks for the username, then the **TOTP, then the password**.
- **A passkey for `pat`.** Same intent, then `"passkeyinit"`. The WebAuthn ceremony ran in Chromium with a CDP virtual
  authenticator, on a Kanidm page and same-origin, then `{"passkeyfinish": [label, credential]}` and commit
  ([passkey.mjs](../../spikes/kanidm-forward-auth/scripts/passkey.mjs)).
  - Kanidm asked for RP `idm.hlspike.test` with user verification `required`.
  - Chromium refuses WebAuthn on a page with a certificate error, so Kanidm's (and Caddy's) key was pinned with
    `--ignore-certificate-errors-spki-list` instead of `ignoreHTTPSErrors`.
- The request bodies come from the proto types
  ([credupdate.rs](https://github.com/kanidm/kanidm/blob/v1.12.0/proto/src/internal/credupdate.rs),
  [auth.rs](https://github.com/kanidm/kanidm/blob/v1.12.0/proto/src/v1/auth.rs)).

**The guarded app** (`whoami` behind `app.hlspike.test`):
- **The client** was made by REST:
  - `POST /v1/oauth2/_basic` with `oauth2_strict_redirect_uri: true`;
  - the redirect URL, `POST …/_attr/oauth2_rs_origin`;
  - the scope map, `POST …/_scopemap/adults` with `openid profile email groups_name`;
  - `PATCH` setting `oauth2_prefer_short_username: true` and `oauth2_consent_prompt_enable: false`;
  - the secret, `GET …/_basic_secret`.
- **oauth2-proxy:**
  - `provider=oidc` with the per-client issuer `https://idm.hlspike.test:28743/oauth2/openid/whoami`;
  - `code-challenge-method=S256` and scope `openid profile email groups_name`;
  - `oidc-groups-claim=groups`, `allowed-group=adults` and `cookie-refresh=1m`;
  - `reverse-proxy`, with `trusted-proxy-ip` set to Caddy's fixed address;
  - `set-xauthrequest`, and `upstreams=static://202`.
- **Caddy**, in two forms:
  - **The `forward_auth` shortcut** (the oauth2-proxy docs' pattern,
    [caddy](https://oauth2-proxy.github.io/oauth2-proxy/configuration/integrations/caddy)):
    - `/oauth2/*` goes to oauth2-proxy;
    - everything else goes through `forward_auth … uri /oauth2/auth`, with `copy_headers` for the four
      `X-Auth-Request-*` headers;
    - a 401 redirects to `/oauth2/sign_in?rd=…`;
    - `request_header -Remote-User` and similar strip the headers an app might trust but oauth2-proxy never sets.
  - **The same guard written out** (route `/fixed/*`; see "The refreshed cookie"):
    - `reverse_proxy` with `method GET`, `rewrite /oauth2/auth` and the `X-Forwarded-Method`/`-Uri` headers;
    - a 2xx `handle_response` that deletes, then sets, each identity header and runs
      `copy_response_headers { include Set-Cookie }`.
- **The per-app pattern.** A second app (`app2.hlspike.test`) had its own client (`whoami2`), its own oauth2-proxy and
  its own cookie.

**The browser test** ([e2e.mjs](../../spikes/kanidm-forward-auth/scripts/e2e.mjs),
[05-e2e.console.txt](../../spikes/kanidm-forward-auth/results/05-e2e.console.txt)):
- Playwright signs in through Kanidm's own pages, filling only the visible field (username, then TOTP, then password).
- It never waits for a TOTP code that's already been used: Kanidm refuses a reused code.

## Results

22 of 23 checks in the browser test passed. The failure was the client-deletion check, which is gap 1, measured
separately below.

| Check | Result | Excerpt |
|---|---|---|
| Unauthenticated → sign-in | ✅ | `app…/api -> app…/oauth2/sign_in -> idm…/ui/oauth2`, page shows `username` |
| Forged identity headers without a session | ✅ refused | ends at `idm…/ui/oauth2` |
| The adult → 200 with identity headers | ✅ | `{"X-Auth-Request-User":"9d0243e2-…" (the person's UUID, Kanidm's sub), "X-Auth-Request-Email":"ada@hlspike.test", "X-Auth-Request-Preferred-Username":"ada", "X-Auth-Request-Groups":"idm_all_persons,idm_all_accounts,idm_people_self_name_write,adults,household"}` |
| The guard's cookie | ✅ | `_hl_guard (secure=true, httpOnly=true, sameSite=Lax)` |
| The kid → refused (Kanidm) | ✅ | after username → TOTP → password: page "Access Denied" at `/ui/oauth2/resume`; Kanidm logs `Identity does not have access to the requested scopes … kim@idm.hlspike.test` |
| The kid → refused (guard), with the scope map widened to `kids` by API | ✅ | Kanidm issues a code, oauth2-proxy answers `403 Forbidden` at `/oauth2/callback` (`--allowed-group=adults`) |
| A per-route audience on one guard (`/oauth2/auth?allowed_groups=kids`) | ✅ | the adult gets 403, which Caddy passes through |
| Forged `X-Auth-Request-*` from the signed-in adult | ✅ replaced | the app sees the real four values, on both Caddy forms |
| A forged copied header that oauth2-proxy didn't return (`X-Auth-Request-Access-Token`) | ✅ deleted | Caddy ≥ 2.11.2 deletes before copying ([CVE-2026-30851](https://github.com/caddyserver/caddy/security/advisories/GHSA-7r4p-vjf4-gxv4), [source](https://github.com/caddyserver/caddy/blob/v2.11.9/modules/caddyhttp/reverseproxy/forwardauth/caddyfile.go)); the written-out form deletes it too |
| Forged `Remote-User`, `X-Forwarded-User` | ✅ stripped | by the edge's `request_header -…` |
| A forged header nobody handles (`X-Spike-Unlisted`) | ⚠️ passes | expected: the edge must strip every header the app trusts |
| Step-up: see below | ✅ | – |
| Person removed (`DELETE /v1/person/ada2`) | ✅ after 61 s | the refresh gets `invalid_grant`; oauth2-proxy logs `Fatal refresh error detected … clearing session` |
| Client deleted (`DELETE /v1/oauth2/whoami`) | ❌ within 4 min; 904 s in the longer run | see "Revocation" |
| A new sign-in for the deleted client | ✅ refused | Kanidm: "An unrecoverable error occurred"; logs `Invalid OAuth2 client_id (whoami)` |
| The guard session across refreshes, `forward_auth` shortcut | ❌ re-sign-in | went through Kanidm again at +97 s and +178 s; see "The refreshed cookie" |
| The same, guard written out with `Set-Cookie` passed | ✅ | no re-sign-in in 200 s; cookie refreshed at +97 s and +158 s |

### Step-up

Kanidm 1.12 honours both parameters
([oauth2.rs, `check_oauth2_authorisation`](https://github.com/kanidm/kanidm/blob/v1.12.0/server/lib/src/idm/oauth2.rs)):
- `prompt=login` is treated as `max_age=0`;
- `max_age` is clamped to 0–86400;
- a session older than `max_age` gets `ReauthenticationRequired`;
- `prompt=none` without a session returns `login_required`.

The step-up probe was a second confidential client (`stepup-probe`). The test exchanged its codes itself and read
the ID token ([token_exchange.py](../../spikes/kanidm-forward-auth/scripts/token_exchange.py),
[12-passkey.txt](../../spikes/kanidm-forward-auth/results/12-passkey.txt)).

| Request, with a live session | `ada` (password + TOTP) | `pat` (passkey) |
|---|---|---|
| No prompt | silent; `auth_time` = sign-in time | silent; no assertion (signCount 3 → 3) |
| `max_age=3600` | silent | – |
| `prompt=login` | asks TOTP then password; `auth_time` +9 s | one fresh assertion (signCount 3 → 4); `auth_time` +6 s |
| `max_age=0` | asks TOTP then password; `auth_time` +30 s | one fresh assertion (4 → 5); `auth_time` +4 s |
| `prompt=none` | silent | – |
| `prompt=none`, no session | `error=login_required`, no page | – |

- **Binding to a plan works through `nonce`.** A nonce of `plan-<hash of the plan>` came back in the ID token, and
  `state` matched.
- **`amr` and `acr` are always null**, by design in the source ("AMR is a bit useless anyway"). The relying party can
  see that the sign-in was fresh (`auth_time`), but not which credential was used.
- **A passkey-only policy doesn't guarantee a passkey step-up**
  ([policy.py](../../spikes/kanidm-forward-auth/scripts/policy.py),
  [14-passkey-policy.txt](../../spikes/kanidm-forward-auth/results/14-passkey-policy.txt)):
  - A group `admins` with `credential_type_minimum = passkey` still let a new member sign in with their existing
    password + TOTP.
  - The policy only blocked their credential changes (`primary_state: PolicyDeny`).
  - The source says why: "TODO: We can't yet fully enforce account policy on auth"
    ([authsession](https://github.com/kanidm/kanidm/blob/v1.12.0/server/lib/src/idm/authsession/mod.rs)).
  - For "a fresh passkey assertion", the adapter must make sure admins hold only passkeys: remove the password in a
    reset when someone becomes an admin.
- **Discovery doesn't advertise it.** There's no `prompt_values_supported` or `claims_supported`, although both
  parameters work. Discovery also has no `end_session_endpoint` (no RP-initiated logout).
- **The rest of the discovery document:**
  - ID tokens are `ES256` only; RS256 needs the per-client `warning-enable-legacy-crypto`;
  - PKCE is `S256`;
  - client auth is `client_secret_basic`/`post`;
  - the access token is a JWS;
  - tokens last 900 s, with a refresh token
    ([03-client-and-discovery.txt](../../spikes/kanidm-forward-auth/results/03-client-and-discovery.txt)).

### The refreshed cookie

([refresh_ab.mjs](../../spikes/kanidm-forward-auth/scripts/refresh_ab.mjs),
[15-refresh-ab.txt](../../spikes/kanidm-forward-auth/results/15-refresh-ab.txt),
[15-refresh-ab-logs.txt](../../spikes/kanidm-forward-auth/results/15-refresh-ab-logs.txt))

**The mechanism.**
- oauth2-proxy refreshes a session older than `cookie-refresh` while answering `/oauth2/auth`, and sends the new
  session as `Set-Cookie` on its 202.
- Caddy's `forward_auth` shortcut copies only the listed headers onto the upstream request; the 202's `Set-Cookie`
  never reaches the browser. oauth2-proxy documents the same for nginx: "if you enabled --cookie-refresh, this is
  needed for it to work with auth_request", with `auth_request_set $auth_cookie $upstream_http_set_cookie`
  ([nginx](https://oauth2-proxy.github.io/oauth2-proxy/configuration/integrations/nginx)).
- Kanidm rotates the refresh token on each refresh. A refresh token older than the session's last issue is treated as
  a replay: "Attempt to reuse a refresh token detected, destroying session"
  ([oauth2.rs, `check_oauth2_token_refresh`](https://github.com/kanidm/kanidm/blob/v1.12.0/server/lib/src/idm/oauth2.rs)).

**The A/B run.** The same adult, two sessions, polled every 20 s for 200 s with `cookie-refresh=1m`:

| Route | What happened |
|---|---|
| `forward_auth` shortcut | The refresh at +77 s succeeded but the cookie stayed. At +97 s the old token was replayed: Kanidm destroyed the session, oauth2-proxy logged `Fatal refresh error detected … clearing session`, and the request went `oauth2/sign_in > idm…/ui/oauth2 > oauth2/callback`. The same again at +178 s. |
| Written out, `Set-Cookie` passed on 2xx | No re-sign-in. The cookie changed at +97 s and +158 s, each time after a successful refresh. |

**Why it matters, and the fix.**
- The re-sign-in is invisible on a page load only while Kanidm's own session lives. It would break `fetch`/XHR calls
  (a cross-origin redirect to the identity provider), POST bodies and WebSockets.
- **The fix is the guard written out.** A 2xx `handle_response` added inside the `forward_auth` shortcut would not
  help. It comes before the shortcut's own handler, so `copy_headers`' delete-then-set would no longer run (per the
  [source](https://github.com/caddyserver/caddy/blob/v2.11.9/modules/caddyhttp/reverseproxy/forwardauth/caddyfile.go)).
- In JSON, the edge's 2xx response handler gets:
  - the delete-then-set routes for each identity header, skipping empty values as `forward_auth`'s generated config
    does;
  - a `copy_response_headers` handler with `include: ["Set-Cookie"]`.

### Revocation

All with `cookie-refresh=1m` ([revoke.mjs](../../spikes/kanidm-forward-auth/scripts/revoke.mjs),
[11-revocation.txt](../../spikes/kanidm-forward-auth/results/11-revocation.txt),
[16-scope-map-fixed-route.txt](../../spikes/kanidm-forward-auth/results/16-scope-map-fixed-route.txt)):

| Change, by API | Live guard session ended | Why |
|---|---|---|
| Person removed (`DELETE /v1/person/ada2`) | after 61 s | the refresh gets `invalid_grant`, which is fatal |
| Role removed (`ada3` out of `adults`) | after 61 s | the refresh succeeds, but the new groups claim lacks `adults`: `[AuthFailure] Invalid authorization via session (unauthorized): removing session` |
| Client deleted (`DELETE /v1/oauth2/whoami2`, own guard) | after 904 s | the refresh gets 401 with an empty body, which isn't fatal; the session lives until it expires, sign-in + 900 s |
| Scope map `adults` removed, `forward_auth` shortcut | after 76 s | not because of the scope map, but the replay described above; Kanidm then refused the fresh sign-in ("Access Denied") |
| Scope map `adults` removed, guard written out | not within 160 s | two refreshes succeeded (the cookie changed twice); a fresh sign-in got "Access Denied" |

**Why.**
- **oauth2-proxy.** It re-checks `allowed-group` against the session's groups on every request
  ([oauthproxy.go](https://github.com/oauth2-proxy/oauth2-proxy/blob/v7.15.6/oauthproxy.go), `getAuthenticatedSession`).
  It refreshes after `cookie-refresh`, and drops the session at once only on `invalid_grant` or `invalid_client`
  ([stored_session.go](https://github.com/oauth2-proxy/oauth2-proxy/blob/v7.15.6/pkg/middleware/stored_session.go),
  `isFatalRefreshError`). Any other error keeps the session until it expires.
- **Kanidm's refresh** checks that the token hasn't expired, that the account and session are valid, and that the
  token isn't being replayed. It **doesn't re-check scope maps** (same file). It rebuilds the groups claim from the
  current memberships.
- **Kanidm's errors at the token endpoint.**
  - A deleted person gets `invalid_grant`.
  - A deleted client, or a wrong client secret, gets `HTTP 401` with an empty body, where
    [RFC 6749 §5.2](https://www.rfc-editor.org/rfc/rfc6749#section-5.2) asks for `{"error":"invalid_client"}`.
  - curl, after the delete:

    ```
    token endpoint, deleted client 'whoami', its old secret, refresh grant:  HTTP 401 content-type= size=0
    existing client, bogus refresh token:  HTTP 400 {"error":"invalid_request"}
    ```

  - oauth2-proxy, once a minute: `Unable to refresh session: … oauth2: cannot fetch token: 401 Unauthorized`.
- Access tokens last 15 minutes (`OAUTH2_ACCESS_TOKEN_EXPIRY`) and refresh tokens 16 hours by default
  ([constants](https://github.com/kanidm/kanidm/blob/v1.12.0/server/lib/src/constants/mod.rs)). That bounds gap 1 to
  15 minutes; gap 2 is bounded only by the refresh token (inferred, not waited out).

## LDAPS

([ldap_read.sh](../../spikes/kanidm-forward-auth/scripts/ldap_read.sh),
[09-ldap-read.txt](../../spikes/kanidm-forward-auth/results/09-ldap-read.txt),
[08-ldap-binds.txt](../../spikes/kanidm-forward-auth/results/08-ldap-binds.txt))

- **Binding as a service account works.** A read-only API token is the password, with DN `dn=token`
  ([docs](https://kanidm.github.io/kanidm/stable/integrations/ldap.html)):

  ```
  ldapwhoami -H ldaps://idm.hlspike.test:3636 -x -D dn=token -w <token>   →  u: ldap_reader@idm.hlspike.test
  ```

- **By default it sees nobody.**
  - `(class=account)` returned only the service account itself, and REST `GET /v1/person` returned 0 persons with its
    token. An anonymous bind is accepted but sees nothing.
  - Putting the account in `idm_account_mail_read` (allowed for the manager) made persons readable:
    - `uid`, `mail` and `entryuuid`;
    - `memberof`, including the nested `household`;
    - `cn` and `gecos`.
  - `idm_people_pii_read` would need more than the manager's rights (`403 accessdenied`).
- **Searching by role works**:

  ```
  (&(class=person)(memberof=spn=adults@idm.hlspike.test,dc=idm,dc=hlspike,dc=test))  →  uid: ada, mail: ada@hlspike.test
  ```

- **Group entries stay invisible.** `(class=group)` returned 0 entries to the reader and to anonymous, even with
  `idm_unix_authentication_read`, but 36 to the manager. Apps that only filter users by `memberof` are fine. Apps
  that search groups need a custom access control.
- **Read-only and LDAPS-only, as documented.**
  - `ldapmodify` gets `ldap_result: Protocol error (2)`, and the entry is unchanged.
  - A bad token gets `Other (80) NotAuthenticated`.
  - `ldap://` and StartTLS on the port get `Can't contact LDAP server (-1)`.
- **Persons' own binds**, for apps that check a password over LDAP
  ([ldap_apps.py](../../spikes/kanidm-forward-auth/scripts/ldap_apps.py)):
  - **The primary password never works over LDAP** (`Invalid credentials (49)`). That's right for a password + TOTP
    credential.
  - **LDAP applications.** An app is created on the SCIM API (`POST /scim/v1/Application` with a `linked_group`;
    needs `idm_application_admins`). Each person creates their own app password for it
    (`POST /scim/v1/Person/{id}/Application/_create_password`). They bind as `name=ada,app=media-ldap,<basedn>`
    ([ldap.rs](https://github.com/kanidm/kanidm/blob/v1.12.0/server/lib/src/idm/ldap.rs),
    [client](https://github.com/kanidm/kanidm/blob/v1.12.0/libs/client/src/application.rs)).
    - **The app's audience holds at bind**: `ada` (adults) → `u: ada@idm.hlspike.test`; `kim` (kids) →
      `Invalid credentials (49)`.
    - **Only the person can create an app password.** The manager's token got `403 accessdenied`; the person's own
      privileged session succeeded.
    - The feature isn't in the 1.12 book.
  - POSIX passwords were not tested.

## The contract, operation by operation

| Operation | How, with Kanidm 1.12 | Status |
|---|---|---|
| OIDC clients: create, change, delete | REST, as above. Per-client issuer and keys. Consent can be turned off per confidential client. | **Proven**. Deleting a client ends guard sessions only at expiry, up to 15 min (gap 1). |
| A forward-auth guard per app and audience | oauth2-proxy per app (its own client and cookie; two ran side by side). The audience holds twice: Kanidm's scope map (refused at sign-in) and oauth2-proxy's `allowed-group` (re-checked every request, groups refreshed every minute). `allowed_groups` on `/oauth2/auth` also allows one shared guard. | **Proven**, with the guard written out so refreshed cookies reach the browser |
| LDAP for apps that need it | LDAPS, read-only, with a service-account token. Per-app passwords whose linked group is the app's audience. | **Partial**: needs `idm_account_mail_read`; groups invisible; app passwords self-service only (gap 3) |
| People: invite | A person by REST, plus an intent (what a reset link carries). `POST /v1/person/{id}/_credential/_update_intent_send` queued a mail (a message id came back); delivery needs `kanidm-mail-sender` ([docs](https://kanidm.github.io/kanidm/stable/email_setup.html)), not tested. | **Proven** (mail delivery not tested) |
| People: role | Group membership by REST. Taking a person out of `adults` ended their guard session after 61 s. | **Proven** |
| People: passkey reset | A new intent; in the session the old TOTP was removed and a new password and TOTP enrolled. The old credential is refused, the new one works ([04-reset-ada.txt](../../spikes/kanidm-forward-auth/results/04-reset-ada.txt)). Passkeys enrol through the same session, with the WebAuthn ceremony in the person's browser. | **Proven** |
| People: removal | `DELETE /v1/person/{id}`; the guard session ended after 61 s. | **Proven** |
| Groups for roles and audiences | Groups and nested groups by REST. The `groups_name` scope gives short names in the claim, and LDAP `memberof` includes nested groups. | **Proven**. Narrowing an audience by scope map alone doesn't end live sessions (gap 2). |
| Step-up | `prompt=login` / `max_age=0` force re-authentication. A fresh passkey assertion for a passkey account. `auth_time` is fresh, and `nonce` binds the plan. | **Proven**. No `amr`, and the passkey policy isn't enforced at sign-in: admins must hold only passkeys. |
| SSH sign-in (kanidm-unixd) | – | Not tested |

## Memory

`docker stats` after the browser test (5 persons, 3 clients; the cgroup's own counters next to it):

| Container | `docker stats` | cgroup `memory.current` / `memory.peak` | anonymous |
|---|---|---|---|
| Kanidm | 84.9 MiB | 114.6 / 119.8 MiB (incl. 49 MiB page cache) | 64.2 MiB |
| oauth2-proxy (one per guarded app) | 23.0 MiB | 35.0 / 36.7 MiB | 5.8 MiB |
| Caddy | 13.7 MiB | 14.1 / 16.9 MiB | 12.3 MiB |
| whoami | 2.5 MiB | 2.5 / 4.7 MiB | 2.0 MiB |

The final snapshot, after every test and with 8 persons, 4 clients and 2 guards
([06-memory-final.txt](../../spikes/kanidm-forward-auth/results/06-memory-final.txt)):
- Kanidm: 87.5 MiB in `docker stats`, a peak of 132.2 MiB, 77.7 MiB anonymous.
- Each oauth2-proxy: 6.8–8.4 MiB in `docker stats`, 7–17 MiB in the cgroup.

- **Compared with Authentik.** Kanidm plus one guard is about 100–150 MiB. Authentik documents "at least 2 CPU cores
  and 2 GB of RAM" ([docs](https://docs.goauthentik.io/install-config/install/docker-compose/)), about 13–20 times
  more.
- **Kanidm's own sizing.** Its docs say to expect about 64 KB of RAM per entry, and require `x86_64_v2` or `aarch64`
  with NEON ([preparing](https://kanidm.github.io/kanidm/stable/preparing_for_your_deployment.html)). A Raspberry Pi 5
  qualifies.

## What else turned up

- **Brute-force soft lock.** One failed sign-in (the old credential, after the reset) locked the account for a few
  seconds: `Account is temporarily locked`. A test or an import must not retry blindly.
- **Who can do what.** The manager couldn't grant `idm_people_pii_read` (403). It needed `idm_admin` to grant it
  `idm_application_admins`. First-time setup should grant every role the adapter needs in one step, as `idm_admin`,
  then put `idm_admin`'s password in the recovery kit.
- **Playwright can leak a secret.** On a timeout, its error log printed the value given to `fill()`. A test password
  appeared once in this run's console. It was in no file and no finding, and it was rotated through a reset. CI must
  wrap `fill()` so that a failure never prints the value.
- **The evaluation certificate lasts 5 days.** `cert-generate` is not for production; HomersLab will hand Kanidm its
  ACME certificate. Kanidm re-reads `tls_chain`/`tls_key` on SIGHUP
  ([quickstart](https://kanidm.github.io/kanidm/stable/evaluation_quickstart.html)).
- **Kids without mail.** oauth2-proxy expects an email for every person; Kanidm's oauth2-proxy example says so too
  ([examples](https://kanidm.github.io/kanidm/stable/integrations/oauth2/examples.html)). The test persons all had
  one; a kid without mail needs `--oidc-email-claim` pointed elsewhere (untested).

## Open questions

- **Gap 1, upstream.** Should Kanidm answer an unknown client at `/oauth2/token` with RFC 6749's
  `{"error":"invalid_client"}`? That would make oauth2-proxy drop the session at the next refresh. Until then,
  removing an app must remove its guard (route and oauth2-proxy) together with the client.
- **Gap 2.** Is there an API to revoke a person's OAuth2 sessions for one client (removing values of
  `oauth2_session`)? Otherwise, changing an audience must also change the guard's `allowed-group` and each native
  app's own role mapping.
- **Gap 3.** Which access control profile lets a service account read groups over LDAP? Is a custom one, made as
  `admin`, acceptable for the catalog's LDAP apps? Home needs an "app password" page, because only the person can
  create one.
- **The written-out guard in JSON.** Do it as the edge's JSON, skipping empty identity values as `forward_auth` does,
  and run the request matrix against it. Is oauth2-proxy's Redis session store an alternative worth its weight? Its
  cookie holds only a ticket.
- **Passkeys only for admins.** When a person becomes an admin, removing their password in a reset session (and
  enrolling a passkey) is the only way to make every step-up a passkey assertion until Kanidm enforces the policy at
  sign-in.
- **Mail.** Does `kanidm-mail-sender` deliver invites through the household's mail account?
- **People admin.** Kanidm 1.12's new self sign-up versus HomersLab's invites limited to the household's domains.
- **Untested:** SSH through `kanidm-unixd`, Kanidm's online backups, and running Kanidm as a non-root user (it warns
  about uid 0).
