---
applyTo: "docs/**,README.md,tools/docsgen/**"
paths:
  - "docs/**"
  - "README.md"
  - "tools/docsgen/**"
description: "Rules for the documentation: user, admin and developer folders, the generated in-app copy, accuracy, markdownlint."
---

# Documentation instructions

- The pages are in three folders, one for each reader: `docs/user/` (people who use the web interface), `docs/admin/` (the administrator who installs, configures and runs a server) and `docs/developer/` (people who change the code or write scripts against the API). Put a topic in the folder of the person who needs it, and split a page that serves two readers. Pages are `NN-name.md`, numbered in reading order within the folder; `docs/index.md` lists every page; `README.md` links the three groups. A new page is added to the index and, if relevant, to the README table.
- **Document what you ran.** Commands, flags and output you write must match the code: check flag names against `cmd/`, defaults against the code, and run the commands where you can. Do not describe planned behaviour as existing behaviour.
- The in-app documentation is **generated from these pages**: `tools/docsgen` renders them with goldmark into `pkg/frontend/docsite/`, which is committed and embedded. After you change anything under `docs/`, run `go generate ./pkg/frontend`; `TestGeneratedDocsAreUpToDate` fails otherwise. The generator fails on a link to a missing page or heading, so a dead link breaks the build. Do not edit `docsite/` by hand.
- A heading that the app links to (a `{{help "id"}}` badge or a `{{docsURL "id"}}` link in a template) gets an explicit id: `## Client ID {#provider-client-id}`. The id is unique across all pages, and `TestTemplateDocLinksResolve` checks that every id used in a template exists. Do not rename such an id without changing the templates.
- Write for the reader of that page: users in `user/`, operators in `admin/`, developers in `developer/`. Short sentences, concrete examples, one idea per paragraph. State limits and trade-offs honestly (`admin/11-limitations-and-security.md`).
- Flags and environment variables go in the tables of `admin/02-configuration.md`; commands in `admin/03-commands.md`; the two-process deployment in `admin/06-split-deployment.md`; the public API in `developer/02-json-api.md` (the internal API is not documented as an interface).
- Markdown style: ATX headings, fenced code blocks with a language, blank lines around lists and fences, no trailing spaces. `markdownlint-cli2` (default rules) is the reference; the long table rows already in the pages (MD013, MD060) are an accepted exception, new prose should be clean.
- Do not paste secrets, real hostnames or real addresses; use `example.com`, `203.0.113.0/24` and placeholders.
