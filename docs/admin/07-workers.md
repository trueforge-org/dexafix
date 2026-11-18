# Workers

A worker is a process that talks to an outside service (Withings, Strength
Level, the sign-in providers, the mail server) for the api process. Only workers
reach the outside: the api process makes no outgoing connection. The api process
keeps the database and decides what is stored. The web process does neither.

```text
api (database, queue)  <──  worker  ──>  Withings, Strength Level,
                                          sign-in providers, SMTP server
        internal listener, worker secret
```

A worker has no database and no encryption key. It asks the api process for a
job, and a user's credentials reach it only inside the lease of that job. Every
call about a job carries the job's lease token. The api process hands each job
to exactly one worker, so you can run as many workers as you like.

## Single process and split deployment

- `dexafix serve` runs the built-in workers inside the same process. There is
  nothing to set up.
- In a [split deployment](06-split-deployment.md) you start workers yourself
  with `dexafix serve worker`. Without one, jobs stay queued: imports and
  syncs do not start.

## Start a worker

Create one worker secret **per provider**. A worker secret opens the jobs of its
provider only: a worker that is compromised, or a secret that leaks, cannot
lease the jobs of another provider, and so cannot read the credentials users
gave to it. Every secret must differ from the others and from the service
secret, because they open different doors. `serve api` refuses to start when two
are equal.

```bash
dexafix secrets worker-secret > withings.secret && chmod 600 withings.secret
dexafix secrets worker-secret > strengthlevel.secret && chmod 600 strengthlevel.secret
```

Give the api process every file, each with the provider it opens, and give each
worker only the file of its provider:

```bash
dexafix serve api \
  --worker-secret-file withings=withings.secret \
  --worker-secret-file strengthlevel=strengthlevel.secret ...
dexafix serve worker --backend-url http://api:8081 \
  --worker-secret-file withings.secret --providers withings
```

A worker that asks for kinds of a provider its secret does not open gets the same
answer as for a kind that does not exist. The api process records an audit entry
("Job handed to a worker") each time it hands a worker a job, which carries the
user's credentials, with the kind and the worker's name.

The api process does not trust what a worker returns. A Withings import result
may hold readings from the job's window only (since its start, or of its day in
your time zone, give or take a day) and none from before 1900 or after tomorrow;
the connection keeps the token fields only. Anything else fails the job.

**Upgrading from a single worker secret:** `--worker-secret-file` of `serve api`
now takes `PROVIDERS=FILE` entries, and `DEXAFIX_WORKER_SECRET_FILES` (entries
separated by `;`) is the variable. The old form still works, so an upgrade does not
stop your workers: a plain path (or `DEXAFIX_WORKER_SECRET_FILE`) is read as one
secret that opens every provider, and the api process logs a warning. Move to one
file per provider when you can: that is what keeps one compromised worker from
leasing another provider's jobs.
In the Compose deployment, run `docker compose pull && docker compose up -d`:
`init` makes `worker_secret_withings`, `worker_secret_strengthlevel`,
`worker_secret_oidc` and `worker_secret_mail`, and each worker container gets its
own secrets volume. The old `worker_secret` file and the
`secrets-worker` volume are no longer used and can be removed.

`--backend-url` is the internal address of the api process, the same one web
uses. Workers use the internal listener and, if you set it up,
[mutual TLS](06-split-deployment.md#mutual-tls-between-web-and-api) with the
`--backend-ca-file`, `--backend-client-cert-file`, `--backend-client-key-file`
and `--backend-server-name` flags. Over plain HTTP at an address that is not
private, the worker logs a warning, because the link carries credentials of
users' accounts at the providers.

The Compose deployment in `deploy/` turns mutual TLS on for the workers: its
`init` container makes a client certificate for the workers (the web process has
its own, from the same CA) and the worker secrets, and gives them to the worker
containers only. The certificate names its role, and the api process opens the
workers' door only to a certificate that is not the web process's. See
[The recommended deployment](06-split-deployment.md#the-recommended-deployment-deploy).

| Flag                   | Environment variable          | Default | Description                                                              |
| ---------------------- | ----------------------------- | ------- | ------------------------------------------------------------------------ |
| `--backend-url`        | `DEXAFIX_BACKEND_URL`         | empty   | Required. Internal address of the api process.                           |
| `--worker-secret-file` | `DEXAFIX_WORKER_SECRET_FILE`  | empty   | Required. The secret of this worker's provider, one per line (the first is sent). On `serve api` it is `PROVIDERS=FILE`, repeatable, and the variable is `DEXAFIX_WORKER_SECRET_FILES` (separated by `;`). |
| `--providers`          | `DEXAFIX_WORKER_PROVIDERS`    | empty   | Comma-separated: `mail`, `oidc`, `strengthlevel`, `withings`. Empty runs all. |
| `--concurrency`        | `DEXAFIX_WORKER_CONCURRENCY`  | `2`     | Jobs this process runs at once, from 1 to 64.                            |
| `--worker-id`          | `DEXAFIX_WORKER_ID`           | random  | Name of this worker on the Admin page.                                   |

`DEXAFIX_DSN_FILE`, `DEXAFIX_ENCRYPTION_KEY`, `DEXAFIX_ENCRYPTION_KEY_FILE` and
`DEXAFIX_SERVICE_SECRET_FILE` mean nothing to a worker. It logs a warning and
ignores them.

The `deploy/` Compose file runs one worker container per provider, so one that
hangs or is blocked cannot hold up the other. Scale one with
`docker compose up -d --scale worker-withings=2`.

## Sign-in providers and e-mail

Two providers are not about a user's data but about the server itself, and the
api process no longer does them:

| Kind | What the worker does | What comes back |
| ---- | -------------------- | --------------- |
| `oidc.discover` | Reads the discovery document and the signing keys of an OpenID Connect or Apple provider. | The endpoints and the key set, which the api process stores. |
| `oidc.exchange` | Swaps the authorisation code of a sign-in at the token endpoint (and, for plain OAuth 2.0, asks the userinfo endpoint). | The ID token or the userinfo, which the api process verifies and then clears. |
| `mail.send` | Sends one plain-text message through the SMTP server. | Nothing. |

These jobs belong to no user, because a visitor who signs in is not known yet.
At most 64 of a kind wait at once, whoever started them. The api process puts
the client secret (for Apple the short-lived signed JWT, never the private key)
or the SMTP password in the lease of the job and nowhere else; the message of a
`mail.send` job, which can hold a reset link, is cleared when the job ends.

- **Run both**, or the matching feature stops. With no `oidc` worker a sign-in
  with a provider says it is not available and the Admin page cannot save an
  OpenID Connect provider; with no `mail` worker a reset e-mail is reported as
  not sent and the test e-mail says no worker is running. There is no fallback to
  connecting from the api process.
- Turning these kinds off on the Workers page has the same effect, so do it only
  to stop them on purpose.
- The Admin settings **Allow sign-in providers on private network addresses** and
  **Allow the mail server on a private network address** are read when a job is
  leased and apply without a restart.
- An administrator who waits on the page (saving a provider, the test e-mail)
  waits for the worker for at most 20 and 50 seconds, and the test e-mail is
  tried once.

## Settings on the Admin page

Which providers a worker runs is decided when it starts. Everything else is on
the **Workers** page of the Admin section, is stored in the database and
reaches every worker without a restart. Per kind of job (for example
*Withings import*):

- **On**: turn a kind off to stop new jobs from starting.
- **Jobs at once, in all workers** and **Attempts per job**.
- A worker that stops (a restart) gives its job back; the api process counts that as
  an attempt and makes the job wait the same back-off as after a temporary failure,
  so giving jobs back is no way to try the same job over and over. The same goes
  for a job that could not be prepared for a worker. A result or new credentials that
  arrive after the job was canceled, or after its lease or time limit ran out, are
  refused. A person can have at most 10 jobs of one kind waiting, and a worker secret
  can ask for jobs about 50 times a second. At most 256 workers are listed.
- **First retry after** (doubled for each further attempt, at most one hour)
  and **Time limit of one attempt**.
- **Automatic run every (hours)**: `0` means no automatic runs.
- **Pause a user's automatic runs after (failures)**: after this many failed
  runs in a row the user's schedule for the kind is paused, with the reason
  shown to the user and the administrator. It starts again when the user fixes
  the connection (disconnect and connect again, or import by hand) or when an
  administrator resumes it.
- **Pause this kind after (failures in a row, all users)**: the circuit
  breaker. It turns the whole kind off until an administrator resumes it.
- Provider options, such as the pause between two writes to Strength Level.

Global settings: the **priority of new accounts** (1 to 100) and how many days
finished jobs are kept. An administrator can set the priority of each account
on the Users page. The Workers page also lists the running workers, with the version
and the time each last asked for work, and the queue, with the state, tries and
detail of each job.

The scheduler that starts automatic runs is part of the maintenance of the api
process (`--maintenance`), so only one process does it.

## When nothing happens

- Jobs stay *queued*: no worker runs that provider. Check the **All workers**
  list on the Admin page, `--backend-url`, and that both sides use the same
  worker secret.
- A worker stops with "the backend refused the worker secret": none of the
  lines in the api process's worker secret file equals the first line of the
  worker's. The file format is the one of the
  [service secret](06-split-deployment.md#service-secret): web sends the first
  line, the receiver accepts every line.
- A kind shows as paused: the circuit breaker or a user's pause fired. The
  reason is on the Workers page.
