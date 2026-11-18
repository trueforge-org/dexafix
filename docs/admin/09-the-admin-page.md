# The Admin page

The Admin page is at `/admin` and is for administrators only (menu under your name). It holds the users, the sign-in settings, the provider credentials, the network and retention settings, e-mail, HTTPS, the workers and the audit log. The settings on it are stored encrypted in the database and take effect at once, without a restart. There are no flags or environment variables for them, except where a section says otherwise. For what must be known before the database opens (listen addresses, key and secret files), see [Configuration](02-configuration.md).

## Server status {#admin-status}

Version, Go version, database driver and schema version, whether encryption is on, counts of users, scans, readings, active sessions and access tokens, and whether Withings is configured.

## Users {#admin-users}

- **Add user:** name, e-mail and password. Works even when sign-up is closed.
- **Make admin / Remove admin:** the last administrator cannot be removed.
- **Set password:** sets a new password for a user (8 to 72 bytes). The user is signed out everywhere and their access tokens are revoked.
- **Reset link:** makes a one-time password reset link, valid for 24 hours and shown once here (see [Password reset](../user/02-accounts-and-sign-in.md#password-reset)). *E-mail it* sends it instead, when e-mail is set up.
- **Remove two-step:** turns off a user's two-step verification and deletes their recovery codes. Use it for a user who lost their device and their recovery codes.
- **Delete:** deletes the user with all their data, immediately and without undo. You cannot delete yourself.

### Strength Level permissions {#strengthlevel-permissions}

*Enable Strength Level sync* lets that user store a Strength Level login and send single days. Once enabled, *Allow multi-day sync* additionally lets them run the background sync of many days.

## New accounts {#admin-signup}

Default: closed. Only the very first account on an empty server can be created without an invitation. After that nobody can create their own account, with a password or on first sign-in through a provider, until you open **New accounts**. When it is closed, administrators still add users above. Existing accounts keep working. This keeps a server that is reachable from the internet from handing out accounts by accident.

## Password sign-in {#admin-password-login}

See [Password sign-in](../user/02-accounts-and-sign-in.md#password-login). Turn it off only after linking your own account to a provider.

## Two-step verification {#admin-two-step}

Choose who must use it: *Nobody* (default), *Administrators* or *Everyone*. Administrators with it on are asked for a code again before any Admin page. See [Two-step verification](../user/02-accounts-and-sign-in.md#two-step).
## Sign-in providers {#providers}

Administrators add providers on the Admin page. There are three types:

- **OpenID Connect**, for example Google, with the issuer
  `https://accounts.google.com`.
- **Sign in with Apple**. Enter the Services ID as the client ID, then the team
  ID, the key ID, and the contents of the `.p8` key file as the secret. DexaFix
  generates Apple's short-lived client secret from these. Apple requires HTTPS.
- **OAuth 2.0**, with the authorisation, token and userinfo URLs.

Register this callback URL with the provider:

```text
https://your-host/auth/<short name>/callback
```

The Admin page shows the callback URL for each provider. Replace `<short name>`
with the provider's short name.

DexaFix connects to a provider from the `oidc` [worker](07-workers.md), never from the api process, so that process needs no route to the internet. A provider on a private
network address (Authentik or Keycloak on your own LAN, say) is refused until
you turn on **Allow sign-in providers on private network addresses** under
**Network and retention**. Cloud metadata and other
link-local addresses are always refused, and so are redirects to them. When a
provider cannot be reached, the Admin page says so and, for a refused address,
how to allow it; what the provider answered is only in the server log. See
[Outgoing connections](11-limitations-and-security.md).

Behind a reverse proxy, forward the `Host` header and set `X-Forwarded-Proto`.
The callback URL is built from them.

Users link a provider in Settings. With **Create accounts on first sign-in**
turned on, a login from an unknown account creates a new account. Accounts are
matched only by the provider's subject, never by e-mail. A new account takes
the provider's e-mail address on first sign-in, unless the provider marks it
unverified. If that address is already in use, sign-in is refused. The person
must then sign in to the existing account and link the provider in Settings.
Removing a provider also removes its links.


### The provider form, field by field {#admin-providers}

Register this server with the provider first (the callback URL is above).

#### Type {#provider-type}

`oidc` OpenID Connect with discovery (Google and others), `apple` Sign in with Apple, or `oauth2` plain OAuth 2.0. The form shows only the fields that apply.

#### Short name {#provider-slug}

Lowercase letters, digits and dashes, up to 32 characters, starting with a letter or digit, for example `google`. It is part of the callback URL and cannot be changed after saving.

#### Display name

The text of the button: *Continue with Google*.

#### Issuer URL {#provider-issuer}

OpenID Connect only. The base URL of the provider, which serves `/.well-known/openid-configuration`. Example: `https://accounts.google.com`. For Apple it is fixed. A provider on a private network address needs **Allow sign-in providers on private network addresses** under [Network and retention](#admin-network).

#### Client ID {#provider-client-id}

The id of the application you registered at the provider. For Apple, the *Services ID*.

#### Client secret {#provider-client-secret}

The secret of that application, stored encrypted. When editing, leave it empty to keep the saved one. For Apple, paste the contents of the `.p8` key file.

#### Apple team ID and key ID {#provider-apple}

Apple only. DexaFix generates Apple's short-lived client secret from team id, key id, services id and the `.p8` key. Apple requires HTTPS.

#### Authorisation, token and userinfo URL {#provider-oauth2}

Plain OAuth 2.0 only: the three endpoints of the provider. DexaFix reads the account id and name from the userinfo response.

#### Account ID field {#provider-subject-claim}

The field in the user information that uniquely and permanently identifies the account (the *subject*). Default `sub`. Change it for OAuth 2.0 providers that use another name, such as `id`. Never use something that can change, like an e-mail address.

#### Scopes {#provider-scopes}

Space-separated. Default for OpenID Connect and Apple: `openid email profile`; for OAuth 2.0: `name email`. Leave empty for the default.

#### Name field {#provider-name-claim}

The field to take the username from, with other characters changed to `-`. If empty or missing, `preferred_username`, `name` and `email` are tried in that order. A taken username gets `-2`, `-3` and so on. The `given_name` and `family_name` fields fill the first and last name.

#### Create accounts on first sign-in {#provider-allow-signup}

Default off. When on (and sign-up is open), an unknown person who signs in with this provider gets a new account. When off, only people who linked the provider from Settings can use it.

## Withings application {#withings-app}

1. In the Withings developer dashboard, create an application and register the
   callback URL, for example
   `https://dexa.example.com/integrations/withings/callback`.
2. As an administrator, enter the application's client id and secret in the
   Withings application section of the Admin page. They are stored encrypted in
   the database. The redirect URL defaults to this server's own callback
   address, so set it explicitly only if the address Withings must use differs
   (for example behind a proxy without a base URL on the Admin page). Connecting is possible
   only when all three are set; until then the Settings page shows Withings as
   not set up. Withings is configured only on the Admin page: the old
   `DEXAFIX_WITHINGS_*` variables and flags no longer exist and are ignored with
   a warning.
3. On the estimates or Settings page, choose **Connect Withings** and grant
   access. Finish within 10 minutes, the lifetime of the connection state.
4. Choose **Import from Withings**.

**Disconnect Withings** forgets the user's tokens. Imported readings are kept.

Imported: weight, fat ratio, fat mass, fat-free mass, muscle mass, hydration,
bone mass and visceral fat. Measurements Withings marks as possibly belonging
to someone else (for example a guest) are ignored. Days are calendar days in the
user's time zone.


## Withings workers and automatic imports {#withings-workers}

Talking to Withings is done by a [worker](07-workers.md), never by the web or
api process. Connecting and importing are jobs in a queue. Without a running
worker for the `withings` provider (in a split deployment, `serve worker
--providers withings`) they wait there and nothing is imported. An
administrator sets limits, retries and time limits per kind of job on the
Workers page of the Admin section.

Automatic imports are off by default. An administrator turns them on by giving
*Withings import* an interval in hours on that page. Each run then fetches the
last 14 days for every user with a linked account. After the number of failed
runs in a row that the administrator set, a user's automatic imports pause. They
start again when the user disconnects and connects Withings again, or when an
administrator resumes them.


## Network and retention {#admin-network}

- **Base URL:** the public address, scheme and host only, such as `https://dexa.example.com`. Saving it is accepted only while you use that address.
- **Trusted proxies:** addresses or ranges of your reverse proxies, as in [Running behind a reverse proxy](05-reverse-proxy.md). Empty trusts nobody.
- **Accept the PROXY protocol from the trusted proxies:** off by default; needs trusted proxies. See [PROXY protocol](05-reverse-proxy.md#proxy-protocol).
- **Allow sign-in providers on private network addresses:** off by default. The server then refuses to connect to a sign-in provider on a `10.x`, `172.16.x`–`172.31.x`, `192.168.x`, `100.64.x` or `fc00::/7` address, so that a provider setting cannot be used to reach other machines on your network. Turn it on when your provider (Authentik, Keycloak, ...) runs on your own network. Cloud metadata and other link-local addresses are always refused, and a provider on this machine (`localhost`) is always allowed.
- **Allow the mail server on a private network address:** off by default, for the same reason, for the SMTP server of the [E-mail](#admin-email) settings. It is separate from the setting for providers: turning one on does not open the other. After an upgrade, a provider or mail server on a private address stops working until you turn on the setting for it; password sign-in keeps working.
- **Keep the audit log for (days):** 365 by default; `0` keeps it forever.
- **Remind me to rotate the encryption key after (days):** empty by default, which never reminds. See [Rotating the key](10-encryption-key.md#rotating-the-key).

## Analytics and ads {#admin-analytics}

Off by default. A Scarf pixel ID (a UUID), a Google Analytics measurement ID (`G-…`) and a Google AdSense publisher ID (`ca-pub-…`). When one is set, a visitor who is **not signed in** is asked, on the sign-in page, the documentation, the status page and the terms and privacy pages, whether to accept it. Nothing is loaded from Scarf or Google before they press Accept, signed-in people never get it, and the footer's **Privacy choices** reopens the question. The sign-in page never offers AdSense (it widens the script policy to inline scripts, and the page asks for passwords), only Scarf and Google Analytics. On those pages the content security policy allows the hosts of the services that are on; AdSense also needs inline scripts and images from any HTTPS address, so turn it on only if you accept that. The privacy policy (`/privacy`) describes it to visitors. The ad wiring follows Google's published requirements and has not been tried against a live AdSense account.

## E-mail {#admin-email}

SMTP server, port (587 by default), encryption (`starttls` by default, `tls`, or `none` only for a server on localhost), user name, password (stored encrypted, never shown again) and sender address. *Send a test e-mail to me* checks the settings. The e-mail is used for [password reset](../user/02-accounts-and-sign-in.md#password-reset) links. The api process does not connect to the mail server itself: a `mail` worker does (see [Workers](#admin-workers)), so with no mail worker running a reset e-mail is reported as not sent and the test e-mail says that no worker is running. A mail server on a private network address needs **Allow the mail server on a private network address** under [Network and retention](#admin-network); when the test e-mail fails, the page says so without what the server answered, and the details are in the server log.

## HTTPS, HTTP/2 and HTTP/3 {#admin-tls}

An optional HTTPS listener on its own port, with an uploaded certificate or a generated self-signed one. The page shows the certificate names, expiry, SHA-256 fingerprint and any bind error. See [HTTPS, HTTP/2 and HTTP/3](05-reverse-proxy.md#https-http2-and-http3).

## Workers {#admin-workers}

Imports and syncs with Withings and Strength Level, the connections to sign-in providers and the sending of e-mail run as jobs in a queue. The api process itself makes no outgoing connection; [workers](07-workers.md) do it. Without a worker for a kind of job, those jobs stay queued. The **Workers** page of the Admin section holds the settings, applied without a restart:

- per kind of job: whether it is on, the jobs at once, attempts, retry delay, time limit and the automatic run interval in hours (`0` is off)
- when to pause: after a number of failed runs in a row for one user's schedule, and after a number in a row for the whole kind (the circuit breaker). A paused schedule starts again when the user reconnects or imports by hand, or when an administrator resumes it.
- the priority of new accounts and how long finished jobs are kept
- the list of running workers and the queue

An administrator sets each account's job priority on the Users list. The worker flags, the secrets and the providers are described in [Workers](07-workers.md).

## Audit log {#audit-log}

Security-relevant events are recorded in the database: sign-ins (successful,
failed and blocked), sign-outs, account creation, profile, password, token and
sign-in method and two-step verification changes, imports, integration connects,
data deletions, exports,
and every administrator action (users, roles, permissions, providers, server
settings). Each event has a time, who did it, whom it concerned, a short detail
and the client's IP address. Details never hold passwords, tokens, e-mail
addresses or login names.

Administrators see the latest 100 events on the Admin page and can read the
whole log at `GET /api/v1/audit` (`limit`, `before_id` and `user_id` query
parameters; an administrator with two-step verification on uses an access token
there, because a session cookie is not enough for the Admin functions). Everyone sees the latest events about their own account in
Settings, and at `GET /api/v1/users/{id}/audit`.

Events are deleted after the **Keep the audit log for N days** setting on the
Admin page (365 by default; `0` keeps them forever). When a user
is deleted, the IP addresses in the events about them are cleared and the
events no longer refer to them. Recording an event never fails the request
that caused it; a failed write is logged.

