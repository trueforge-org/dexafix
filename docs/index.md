# Documentation

The [README](../README.md) gives an overview. The pages are in three groups, one for each reader. The same pages are served inside the app at `/docs`.

## Using DexaFix (`user/`)

For people who use the web interface.

1. [Using the app](user/01-using-the-app.md): getting started, the pages and the settings.
2. [Accounts and sign-in](user/02-accounts-and-sign-in.md): signing up, passwords, password reset, two-step verification, passkeys, access tokens and your data.
3. [Estimates](user/03-estimates.md): how the daily estimates are calculated, calibration, methods and athlete mode.
4. [Withings and Strength Level](user/04-withings-and-strength-level.md): linking accounts and importing or sending data.
5. [Encryption and privacy](user/05-privacy-and-encryption.md): what is encrypted.
6. [DEXA scans and scale readings](user/06-scans-and-readings.md): every field of the two forms.

## Running a server (`admin/`)

For the person who installs, configures and runs DexaFix.

1. [Getting started](admin/01-getting-started.md): run the app from source.
2. [Configuration](admin/02-configuration.md): flags, environment variables and how secrets are passed.
3. [Commands](admin/03-commands.md): the `db`, `user`, `settings` and `secrets` commands.
4. [Database](admin/04-database.md): SQLite and PostgreSQL, migrations and moving between them.
5. [Running behind a reverse proxy](admin/05-reverse-proxy.md): trusted proxies, the base URL, the PROXY protocol, and HTTPS with HTTP/2 and HTTP/3.
6. [Split deployment](admin/06-split-deployment.md): the web and api processes, Compose, Valkey and troubleshooting.
7. [Workers](admin/07-workers.md): the processes that talk to Withings, Strength Level, sign-in providers and the mail server.
8. [Body service](admin/08-body-service.md): readings, scans and estimates as a process of their own.
9. [The Admin page](admin/09-the-admin-page.md): users, providers, network, e-mail, workers and the audit log.
10. [The encryption key](admin/10-encryption-key.md): where the key comes from, backups and rotation.
11. [Limitations and security](admin/11-limitations-and-security.md): read before you expose the app.

## Developing DexaFix (`developer/`)

For people who change the code or write scripts against the API.

1. [Development](developer/01-development.md): tests, project layout, schema changes and adding an importer.
2. [JSON API](developer/02-json-api.md): routes, authentication and the import endpoint.
3. [Estimation details](developer/03-estimation-details.md): the exact rules and the estimate cache.
4. [Body service internals](developer/04-body-service-internals.md): the limits of the split.
5. [CI and releases](developer/05-ci-and-releases.md): what CI checks, the secrets to configure and how to cut a release.
6. [Key rotation design](developer/06-key-rotation-design.md): the key check and why rotation stays manual.
