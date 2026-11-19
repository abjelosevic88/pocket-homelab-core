# Moving in and out (Pocket Move)

Pocket Move takes a person's data out of every Pocket app as plain files, and
brings data in: from another Pocket Box, or from Google Takeout, Evernote,
Notion, iCloud, other podcast and feed readers, Goodreads and bank statements.
Move does the unpacking and converting; each app only has to say what it can
give and take. Everything is per person: Move acts for the person who is
signed in to it, through ordinary [app links](links.md), and an admin never
gets anyone else's data this way (the whole-box copy stays with Backups).

## What an app declares

```go
var portable = &core.Portable{
	Exports: []core.PortableExport{
		{Path: "/api/v1/export.zip", File: "notes.zip", Kind: core.KindPocket, Title: "Every note as a Markdown file, with its folders"},
	},
	Imports: []core.PortableImport{
		{Path: "/api/v1/import", Kind: core.KindPocket, Form: "file", Title: "Adds the notes from another box"},
		{Path: "/api/v1/import", Kind: core.KindMarkdownZip, Form: "file", Title: "Adds Markdown files as notes"},
	},
}

core.Manifest{ /* … */ Portable: portable }
```

- `Register` adds the offers `move:export` (when there are exports) and
  `move:import` (when there are imports), like `events:read`.
- Mount the description: `r.Get(core.PortablePath, portable.Handler())`
  behind the normal auth (`GET /api/v1/pocket/portable`).
- In `linkAllows`: `if portable.LinkAllows(scopes, r) { return true }`.
  That opens exactly the description, the export paths (GET, `move:export`)
  and the import paths (POST, `move:import`), nothing else.
- Several imports may share a path when one route reads several formats.

### Export kinds

`KindPocket` is the app's own full export: the file Move hands back to the
same app's `KindPocket` import on another box. Add open-format exports
(`.ics`, `.vcf`, `.opml`, Markdown, CSV, GPX) next to it where the app has
them, so the archive is useful without any Pocket app at all.

### Import kinds

| Kind | Body | From |
|---|---|---|
| `pocket` | the app's own export | another Pocket Box |
| `vcard` | `text/vcard` | Google, iCloud, Outlook contacts |
| `ical-events` | `text/calendar`, VEVENT | Google, iCloud, Outlook calendars |
| `ical-todos` | `text/calendar`, VTODO | Google Tasks (Move converts), Apple Reminders |
| `markdown-zip` | zip of `.md` files, folders kept | Evernote, Google Keep, Notion, Apple Notes (Move converts) |
| `opml-feeds` / `opml-podcasts` | OPML | Feedly, Inoreader / Apple Podcasts, Pocket Casts, Overcast |
| `goodreads-csv` | Goodreads CSV | Goodreads, StoryGraph |
| `bank-csv` | a statement as CSV | banks and cards |
| `geojson-places` | GeoJSON FeatureCollection | Google Maps saved places |
| `google-timeline` | Google location history JSON | Google Takeout / phone export |

`Form` names the multipart field; empty sends the file as the raw body with
`ContentType`. Imports must **add, never delete**, and should skip what is
already there (a second run of the same file changes nothing). Answer with
JSON; Move shows the top-level numbers (`imported`, `created`, `skipped`…).

## Links

Pocket Move declares `move:export` and `move:import` in every Pocket app. They
arrive off: switch them on in **Links** (the Pocket Move card has *Switch all
on*). An app that has not adopted this yet shows *Can't work* (`not_offered`),
and Move greys it out with "needs an update".
