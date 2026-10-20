# Platform contract and upgrade notes

This describes the source changes following the 20 October architecture review.
Deploy Core before the companion applications. The changes do not deploy themselves.

## Authorization

Installing an application with the registration key approves the scopes in its initial manifest. An application-token manifest update may request additional scopes, but those scopes start disabled. Approve them in Core's Links page after reviewing the reason. Removing and restoring a declaration preserves prior admin decisions and personal opt-outs.

An app can read its own events. To read another Pocket app's events, it must declare an approved `events:read` use, the publisher must offer that scope, and the event must identify an active person admitted to both applications with that link enabled. The SDK adds the offer to publishers. Attribution currently recognizes the existing string fields `user_email`, `owner_email`, and `user`; conflicting identities are refused. Unattributed legacy events, Core events and connector events are not exposed to other apps. Subscriptions express interest and never grant permission. The same checks cover history, individual events, SSE and webhook delivery. Event-topic and source inventories are admin-only.

An app whose token has been revoked no longer automatically reenrolls with the registration key. Remove enrollment keys from application environments after successful enrollment. Recover a revoked installation deliberately by installing its replacement app token; do not routinely delete `core.token` to bypass revocation.

## Delivery and operation retries

Core commits each event and its webhook jobs in one database transaction. `Idempotency-Key` is scoped to the publishing source: an identical retry returns the original event ID; changed content returns HTTP 409. Compact receipts survive log retention. Pending webhook jobs keep their events until delivery finishes. Webhooks are at least once, with four delivery workers; consumers must deduplicate `X-Pocket-Event-ID` and must not assume webhook arrival order.

SSE reads the persisted log using one ascending cursor across all requested patterns. It drains every replay page, deduplicates overlapping patterns and uses in-memory notifications only to wake the reader. A full notification buffer cannot skip a persisted event. Reconnect using `Last-Event-ID`, or use `after=0` to read all retained history. Retention still bounds available history; the stream is not a permanent archive.

All 17 applications now synchronously enqueue publication work in their local database and retry it through the same SDK. The queue survives process restarts and Core outages, and uses one stable idempotency key per publication. `/healthz` includes `core_outbox.pending`, `retrying`, and `oldest` (Unix seconds). Queue entries are retained on errors; operators should investigate growing counts rather than treating health alone as delivery success.

Money's transaction-creation path saves the domain change, operation receipt and event in one transaction. Retrying an `external_id` for the same caller and source returns the existing transaction; conflicting request contents return HTTP 409. Deleted transactions retain a tombstone so retries cannot recreate them or resolve to a reused row ID. Replays recheck account permissions. Legacy transactions without receipts cause an ambiguous repeat to be refused rather than booked again. This does not remove duplicates already present in a ledger.

**Remaining delivery boundaries:** other application publishers enqueue immediately after their domain mutation, so an abrupt exit between those two commits can still lose a notification. `sdk.QueueEvent(ctx, tx, ...)` provides the transactional primitive for migrating those write paths. General operation idempotency for Inventory adjustments, Split expenses and document uploads remains separate work; Money's guarantee must not be assumed for those APIs.

## Identity and revocation

Companion apps bind local users by verified OIDC issuer and subject, or Core introspection's stable identity, rather than repeatedly matching email. Existing accounts bind by verified email on first contact; a different subject cannot take over an already-bound account through email reuse. Subsequent verified email changes preserve the local account ID and refresh its email. If a legacy user's email changed before the first binding, resolve the account link explicitly rather than merging data automatically.

Existing Core subjects are preserved during migration. Newly created Core people receive non-reusable random subjects. Disabling a person, changing their role or changing their password advances their authorization revision.

The SDK revalidates bearer tokens on every request by default. Opt-in positive introspection caching requires a known expiry and never exceeds that expiry; its configured duration is also a deliberate revocation delay. Core-origin browser sessions retain issuer, subject and authorization revision, and revalidate against Core on each request. They fail closed when Core cannot be reached or the person loses access. Disabled/re-enabled accounts do not resurrect old sessions.

**One-time sign-in:** old app sessions did not record their origin. The first upgrade of each Core-connected app clears those sessions so everyone signs in again. App-local password credentials and API tokens remain independent credentials; disabling a Core person does not revoke those credentials. Third-party service credentials likewise require upstream rotation: Core's link switch controls retrieval, while cached or exported keys remain usable until rotated or removed upstream. The existing service-credential cache is separate from token introspection and keeps its documented expiry/stale behavior.

## Shared SDK and application integration

The canonical SDK is `sdk/` in Core. Each companion repository carries a generated, independently buildable snapshot under `third_party/pocket-core`, with a local module replacement, file hashes and a build-time checksum check. This is an unpublished development snapshot, not a claim that the changes shipped in upstream v0.18.5. Do not edit snapshots independently.

From Core, run `python3 scripts/sync-sdk.py` to update all 17 sibling repositories, and `python3 scripts/sync-sdk.py --check` to detect drift. The snapshot contains no test-only SQLite dependency. App startup calls `Client.Start` with its shutdown context; startup errors must stop the server.

Day now declares `tasks:read` and fetches completed Pocket Tasks into the journal with pagination and local-day boundaries. Notes declares Tasks read/write scopes and supports `GET /api/v1/tools/task/targets?destination=pocket-tasks` and `POST /api/v1/tools/task` with `destination: "pocket-tasks"`. Existing installations require admin approval for these new grants. Choosing that destination never falls back to Nextcloud after a refusal. Native one-scan pairing remains on the client roadmap; these server changes do not implement that feature.

## Validation and recovery

`python3 scripts/test-platform-contract.py` builds real Core and Money binaries and uses disposable databases and loopback servers. It exercises enrollment, pairing-token identity, repeated and conflicting writes, private-event isolation, revocation of an issued delegated token, stable identity after an email change, OIDC session invalidation, Core outage and publication recovery after both processes restart. It does not access deployed data.

For an upgrade, back up Core's complete data directory (database, encryption/signing material and files), each app's database and files, and its private Core token/OIDC configuration. Stop writers or use a SQLite-consistent backup method; copy queued events and operation receipts with their databases. Restore into an isolated instance first, verify identities and pending publications, and reconcile upstream credentials before reconnecting production services. Restoring an older snapshot can restore obsolete grants or keys; do not treat a database restore as revocation reconciliation. No production restore drill is claimed by the automated contract test.
