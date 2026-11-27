# Pocket UI kit

The React components and stylesheets every Pocket app used to copy by hand
(the skill's "copy verbatim" list). `scripts/sync-sdk.py` copies them into an
app's `web/src/pocket-ui/` with a `SHA256SUMS` file the app's Dockerfile checks.

An app takes only the files it opted into: the ones already in its
`pocket-ui/` folder, plus any named with `--ui`. See `docs/appkit.md`.

Files import only React and two app files with a fixed shape:
`../components/icons` (`Icon`) and, for `PocketModelSelect.tsx`, `../lib/api`
(`api`, `V1`).
