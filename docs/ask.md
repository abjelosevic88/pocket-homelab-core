# Pocket Ask

Every Pocket application serves the shared launcher at `/pocket-ask.js`. **Ask**
opens a desktop side panel or a phone bottom sheet. The target button beside it
lets the person choose a row, card or other item without leaving the application.
Inside Hub app tabs, the active application supplies the launcher so the entry
point is not duplicated.
Selected text, item title and a same-origin item link are preview context; the
agent uses Core tools to fetch current data and resolves ambiguous items before
changing them. The source remains pinned while a conversation is open. **Use
current item** starts a separate conversation after navigating elsewhere.

The panel is served by Core at `/ask`, using the person's Core browser session.
A first-use Enable action establishes a revocable 30-day Pocket Ask connection.
Core must enable `MCP_OAUTH_ENABLED` and `MCP_APP_TOOLS_ENABLED`, and Pocket AI must
be registered and shared with that person. The launcher discovers the public
Core address from the registered OIDC issuer. Applications and Core should share
a site so browser cookie restrictions allow the embedded session. Sign-in opens
Core in a separate tab if needed. Disconnect under Core → AI connections.

Questions become ordinary, private Pocket AI threads with `source=ask`, a source
link, immutable restrictions and saved item context. Recent conversations for the
same item appear in the panel; Open in Pocket AI continues the same conversation.
Sonnet is the default; a new conversation can select Haiku or Opus. Ask is available
to authenticated members without granting permission to run coding agents.

## How access and changes work

Core delegates only to the registered AI API URL, refuses redirects, checks the
source against an accessible registered application and requires the exact Core
Origin on browser mutations. It never returns MCP credentials to the panel.
Pocket AI encrypts the per-user credential with its existing data-directory key.
Core's gateway continues to enforce current app access, item permissions,
revocation, schemas, response limits and write request IDs.

Each live Ask session gets a private loopback MCP endpoint. Its random capability
is in a mode-0600 temporary configuration, removed when the process exits; Core's
credential is never given to the CLI. The server classifies tools from Core's
catalog. Every write pauses for a one-use approval of the exact captured arguments
before dispatch. Decline, disconnect, stop and expiry send no unapproved write.
Retries retain Core's existing idempotency protections. An uncertain outcome is
reported rather than automatically replayed with a new request ID.

Claude uses `--restricted --tools "" --strict-mcp-config`, disabled hooks and
commands, an explicit system prompt, and an isolated per-user configuration.
Authentication uses a link to the server's existing Claude credentials. The
installed Claude must support these switches. There are no built-in shell, file,
web or agent tools. Ask permissions cannot be changed or forked into a coding
thread, including from the normal Pocket AI UI. Private Ask logs are included in
their owner's Today and On this server views.

## Application coverage

Core's reviewed catalog covers Notes, Tasks, Calendar, Habits, Day, Inventory,
Bookshelf, Clips, Contacts, Documents, Files, Garage, Kitchen, Money, Split,
Invoicing, Collaborator, Travel, Office, Podcasts, Subtitles and Hub. Monitoring
adds accessible server summaries, snapshots and alerts. Pocket AI adds a bounded
reader for the person's own conversations. Core's app/status tools cover registry
and service health. The shared launcher is installed in all 25 applications.

The catalog deliberately covers supported application actions, not every HTTP
route. Deletion, sharing changes, credential administration, service restarts,
message delivery and arbitrary HTTP requests are not tools. File reads follow
format support: Documents supplies OCR text, Files supplies UTF-8 text, and Office
extracts bounded text from DOCX/XLSX/PPTX/OpenDocument/text files. Images, binary
formats, formatting and live spreadsheet calculations are not interpreted.

## Extending and checking

`internal/server/mcp_app_catalog.go` defines reviewed tools; `sdk/ask-widget.js`
defines the reusable launcher; `internal/server/ask.html` is the responsive panel.
Run `scripts/sync-sdk.py` after SDK changes; it includes embedded assets and updates
checksums for every app. An application can open Ask programmatically with:

```js
window.dispatchEvent(new CustomEvent('pocket-ask', {
  detail: { element: document.querySelector('[data-pocket-ask]') }
}))
```

The element can contain a title, a same-origin item link and readable text.
No authorization decision relies on that DOM text.

Tests cover browser-session/origin requirements, source validation, app-bound
identity, revocation, per-person thread access, approval/denial, and rejection of
mode escalation. Office tests cover text, spreadsheet strings, invalid archives,
unsupported formats and decompression limits. The live browser verification
checks a real read, a declined write, an approved write, saved-thread continuity,
mobile overflow and the actual restricted CLI tool list.

## Answer rendering

The panel renders assistant Markdown with Marked and sanitizes it with DOMPurify.
Tables scroll within the answer; headers, lists, tasks, quotations and code blocks
use the current light/dark theme. User input stays literal. Raw HTML is displayed
as text and Markdown images become links without loading remote images.

`npm run build` in `web` builds both the main application and the standalone
`ask-markdown.js` asset embedded in the Go binary. Run `npm run test:ask` for the
browser regression suite (install Chromium with `npx playwright install chromium`
first, or set `POCKET_ASK_CDP` to an existing Chromium debugging endpoint). Tests
cover formatting, unsafe content, incomplete streamed replies, stable polling and
320/390/460-pixel layouts in both themes.

## Direct app access in ordinary Pocket AI threads

After a person enables their Pocket Ask connection, normal Claude and Codex
threads also receive the per-user proxy when starting or resuming a process.
Claude receives a private MCP configuration; Codex receives a transient
`mcp_servers.pocket` thread configuration override. Global CLI configuration and
the user's model, reasoning effort and coding mode stay unchanged. Disconnected
users keep coding access and get guidance to reconnect for app-data questions.

These sessions use the same write approval gate as Ask. Switching a coding
thread to full access never approves a pending Pocket write, and session-wide
approval is rejected for Pocket operations. Agents receive instructions to use
app tools directly for data tasks instead of discovering files, credentials or
database locations. Coding tasks still use their usual development tools.

`pocket_money_summary` exposes Money's existing report overview in one call:
totals, categories, comparison period and top expenses. The response specifies
the date range, home currency and integer minor-unit amounts. Notes tools explain
that `category` is the folder path returned by `pocket_notes_folders`.
