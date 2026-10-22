# Application tools through Core

One remote MCP connection to `https://core.example.com/mcp` can use the main
workflows in every current Pocket app. It works from another computer or a cloud
assistant with network access to that URL. The person needs a Core account;
SSH access and application API keys are unnecessary.

Enable both `MCP_OAUTH_ENABLED=true` and `MCP_APP_TOOLS_ENABLED=true` in Core,
rebuild/restart, and configure app sharing under Core services. Apps must be
registered with Core, with a reachable registered API URL, and use Core's user
token introspection. The gateway uses their existing APIs; no separate MCP server
or app deployment is needed. Unsupported third-party services remain outside
this application catalog.

## Links in tool results

Successful application calls include `app_url` (registered public app address)
and `source_url` (the relevant page). Records with supported browser routes also
include `item_url`, including reviews, notes, tasks, documents, transactions,
recipes, vehicles and trips. Link these directly in assistant answers. Links use
the public address rather than the internal API host and require normal app
access; they do not create public shares or carry authorization tokens.

Child resources such as review comments link to the containing review through
`source_url`. Apps without individual browser routes provide a page/app link;
assistants must not claim these links select a specific record or comment.

## Connect without a localhost redirect

Use this option when Codex is on a remote machine or a browser callback to
`127.0.0.1` fails. That address refers to the browser's machine; it does not reach
a callback listener on a different server.

1. Sign in to Core and open **AI connections → Connect without redirects**.
2. Name the connection and choose status, reading, or reading and creating/updating.
3. Choose an expiry (1, 7 or 30 days), then **Create token with this access**.
4. Copy the token. It appears only in that response; Core stores a hash.
5. On the machine running the assistant, store it as `POCKET_ACCOUNT_MCP_TOKEN`
   in the environment or the hosting service's secret manager.

```sh
codex mcp add pocket_account --url https://core.example.com/mcp \
  --bearer-token-env-var POCKET_ACCOUNT_MCP_TOKEN
```

Equivalent `~/.codex/config.toml` configuration:

```toml
[mcp_servers.pocket_account]
url = "https://core.example.com/mcp"
bearer_token_env_var = "POCKET_ACCOUNT_MCP_TOKEN"
```

For an interactive terminal you can read the token without putting it in shell
history. Use the command for your shell, paste the token, press Enter, then launch
Codex from that same shell:

```sh
# Bash
read -rs -p 'Core connection token: ' POCKET_ACCOUNT_MCP_TOKEN
export POCKET_ACCOUNT_MCP_TOKEN

# Zsh (default on macOS)
read -rs 'POCKET_ACCOUNT_MCP_TOKEN?Core connection token: '
export POCKET_ACCOUNT_MCP_TOKEN
```

Use **one** of these shell examples. Desktop apps and hosted instances need the
variable in their own launch environment/secret settings; exporting in an unrelated
terminal does not configure an already-running app. Restart the assistant after
configuring the connection. **Do not run `codex mcp login` in token mode.** Bearer
environment configuration is documented in the [official Codex MCP guide](https://developers.openai.com/codex/mcp/).

Other clients need support for a Bearer token or custom Authorization header.
Clients supporting only OAuth still use the browser flow below. A connection token
is tied to your account, scope and expiry; it cannot authenticate ordinary Core
APIs. It has no refresh token or callback. Replace it at expiry, or disconnect it
under **Your connections** to revoke it immediately. Limit: 20 active connection
tokens per account. Account disable/authentication-revision changes and app sharing
changes apply just as they do to OAuth connections. Do not share tokens in chat,
source control, URLs or screenshots.

## Connect with browser sign-in and choose access

```sh
codex mcp add pocket_account --url https://core.example.com/mcp
codex mcp login pocket_account --scopes status:read,apps:read,apps:write
```

Sign in in the browser and approve **Allow application access**. Start a new
assistant session so it discovers the tools. For an existing status-only Codex
connection, first run `codex mcp logout pocket_account`, then log in with the
scopes above. Do not attach an operator bearer token to an account connection.

For read-only content, request `status:read,apps:read`. For health checks alone,
request `status:read`. Other MCP clients can use the same URL and OAuth scopes.
They must support Streamable HTTP, OAuth discovery and dynamic registration.
Client feature availability varies; a URL alone cannot add OAuth support to a
client that lacks it.

Core's consent page states what the connection requests. **AI connections** shows
each connection's approved access and provides **Disconnect**. Existing grants
and operator status keys never silently gain content access. Disabling the app
tools setting immediately removes application tools while keeping status access.

## Current coverage

These are curated operations, not an export of every API route. Native tool names
start with `pocket_<app>_`; the assistant discovers the exact argument schemas.
The three status tools work across all registered, permitted Pocket apps.

| App | Read | Create/update |
|---|---|---|
| Notes | Notes, folders, full-text search | Create, append, update with current etag |
| Tasks | Lists, tasks, dates and completion | Create, edit, complete/reopen |
| Calendar | Calendars, date-range events, search | Create/edit events and recurring series, without invitees |
| Habits | Habits, today and daily check-ins | Done/missed check-in, journal text |
| Day | Journal entries and moments | Append journal text, update a day |
| Inventory | Items, quantities and locations | Create/edit items, adjust quantities |
| Bookshelf | Books, reviews and reading progress | Add/edit books, record current page |
| Clips | Clips and snippets | Save text, rename/pin clips |
| Contacts | Address books and contact cards | Create contact, star/add categories |
| Documents | Metadata, OCR text and annotations | Annotate, update title/date |
| Files | Spaces, listings, metadata, search, text files | Create folders, create/replace text files |
| Garage | Vehicles, maintenance records and notes | Create/edit vehicles, add notes |
| Kitchen | Recipes, shopping lists and pantry | Create/edit recipes, add/edit/check shopping items |
| Money | Spending/income summaries, accounts, categories and transactions | Record/edit expense or income bookkeeping |
| Split | Groups, expenses and balances | Create groups and expense records |
| Invoicing | Clients and invoices | Create/edit clients, create draft invoices |
| Collaborator | Reviews, tasks and comments | Create reviews/tasks, edit/complete tasks |
| Travel | Trips, journals and checklists | Create/edit trips, append journal entries |
| Office | Sources, directory listings and recent documents | Create blank DOCX/XLSX/PPTX documents, rename |
| Podcasts | Subscriptions, episodes, show notes and queue | Subscribe, favorite, save/queue episodes |
| Subtitles | Titles, files, metadata and subtitle text | Nudge timing with backup, pin with note |
| Hub | Personal preferences and layout | Update appearance, favorites and personal spaces |

Examples:

- “Read my renovation notes and create a task for each unfinished action.”
- “Find the recipe for lentil soup and add its missing ingredients to my shopping list.”
- “Show the current trip plan and append today's journal entry.”
- “Create a draft invoice for this client with these line items.”
- “Create a blank spreadsheet in my Office drafts folder.”

The assistant should look up IDs through the read tools and resolve ambiguous
items with you before changing them. Operations on multiple apps are separate
requests; they are not an atomic transaction. Existing application automations
(such as configured webhooks) can still run after a normal item change.

## Calling tools directly

List tools using your MCP client's `tools/list`. For example:

```json
{"name":"pocket_notes_search","arguments":{"query":{"q":"renovation","limit":20}}}
```

```json
{
  "name": "pocket_notes_create",
  "arguments": {
    "request_id": "bfbdc908-8bb0-4d54-8258-084defa36928",
    "data": {"title":"Renovation next steps","content":"Measure the kitchen."}
  }
}
```

Read the note again with `pocket_notes_get`, then use its current `etag` when
calling `pocket_notes_update`. Update tools generally preserve omitted fields.
Supplied arrays replace those arrays. Files `save_text` replaces the complete
file; Office currently creates blank documents and renames them, rather than
editing spreadsheet cells or rich document content. Documents uploads, binary
file uploads, contact-card replacement, calendar invitees and occurrence edits
are not part of this catalog.

Money and Split amounts are integer minor units (`1250` means EUR 12.50).
Invoicing line prices are major units (`12.50`). Split modes are `even`, `amount`,
`percent`, and `shares`; percentage shares total `10000`. These tools record
bookkeeping and do not perform bank transfers or send invoices.

Every write requires a unique `request_id` of 16–80 letters, digits, underscores
or hyphens. Use a UUID. Retries with the same ID and identical arguments return
the saved result without resending; reuse with different arguments is refused.
IDs are unique across an account's connections. Do not generate a new ID merely
because a request timed out. Core cannot prove whether an application committed
an interrupted request: read the app and reconcile first. Receipt bodies expire
after seven days; small ID/digest tombstones remain to prevent old requests from
being repeated. A server crash after reservation can leave an unknown outcome.

## Authorization and boundaries

Core checks the OAuth grant, account status, current app audience and approved
scope. Each app checks its own per-user item permissions. A Core administrator's
connection does not bypass the app's item ownership checks.

The gateway never forwards an assistant's OAuth token or uses a box administrator
credential. For each call it mints an internal `pcu_mcp_` credential, bound to that
user's grant and the target app, expiring after 60 seconds. Only that app's
registered `pca_` credential can introspect it. It is removed after the call and
is not accepted as a general Core API token. This works with existing Pocket
SDKs that recognize Core `pcu_` tokens. Apps must not cache introspection beyond
credential validity; the shipped default SDK clients do not cache it.

Routes, methods and accepted fields are fixed in Core's reviewed catalog. There
is no arbitrary URL, HTTP method or API passthrough. Redirects are refused, calls
have a 15-second deadline, and responses are limited to 512 KiB. Queries supporting
a limit default to 50 items and accept at most 100; narrow large results and page
using the supported filters. Text file reads require a textual content type and
valid UTF-8. Credential-shaped fields and the internal token are removed from
structured responses. User content itself can still contain sensitive text;
approving read access lets the assistant read that content. Treat returned text
as data, never as instructions to grant access or invoke other tools.

Deletion, credential/settings administration, sharing/invitation changes, service
restarts, shell commands and message-sending endpoints are not exposed. Some
updates intentionally replace content (text files, recipe arrays) or change
completion status; clients should use their normal confirmation controls for
changes. Core logs user, client, app, operation, request ID and success/error,
without recording request content or bearer credentials. Private write receipts
are held in Core's database; protect backups as you would other user data.

## Adding or extending an application

Add reviewed operations to `internal/server/mcp_app_catalog.go`, reusing an app's
existing user-authenticated API. Define strict JSON fields, required inputs, units,
update semantics and fixed route parameters. Do not add arbitrary API dispatch.
For replacement-style APIs, preserve omitted values explicitly or require a
complete input; the Hub preferences adapter currently reads/merges before PUT.
That API has no etag, so simultaneous preference edits follow the app's existing
last-write-wins behavior.

Add meaningful permission and compatibility tests. The regular test suite covers
OAuth scope isolation, app-bound identity, revocation, schema rejection, bounded
responses and duplicate protection. To check the deployed application API versions
without touching production data, build local `pocket-<slug>:local` images and run:

```sh
POCKET_MCP_DOCKER_TEST=1 go test ./internal/server \
  -run TestMCPApplicationDockerCompatibility -count=1 -v
```

This Linux/Docker test launches isolated containers using temporary in-memory
storage and a temporary Core database, with test-only identities. It mounts no
live application volumes or credentials. Review new permissions before extending
the shared scopes: people who previously approved `apps:write` can discover newly
added permitted-app operations within that scope.
