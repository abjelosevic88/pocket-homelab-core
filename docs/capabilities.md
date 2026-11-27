# Capabilities

Some Pocket apps bring the others together: Pocket Search finds things in every app, Pocket Day writes down what happened in each, Pocket Hub shows a widget per app and keeps a quick line where it belongs. Until 0.49 each of them carried a hand-written table of every other app (its routes, its answer's shape, its scope), so a new app meant a change in three or four other repositories.

A capability turns that around. The app says in its manifest what it can do, answers on one fixed route in one shape, and the apps that ask find it on their own.

| Capability | Manifest | Route (in the app) | Scope | Asked by |
|---|---|---|---|---|
| Search | `search` | `GET /api/v1/pocket/search?q=&limit=` | `pocket:search` | Pocket Search |
| Day | `day` | `GET /api/v1/pocket/day/{YYYY-MM-DD}[?tz=]` | `pocket:day` | Pocket Day |
| Widget | `widget` | `GET /api/v1/pocket/widget` | `pocket:widget` | Pocket Hub |
| Capture | `capture` | `POST /api/v1/pocket/capture` | `pocket:capture` | Pocket Hub |

Everything stays per person: the route runs as the person the caller acts for, and returns only what is theirs. Nothing here is a second API: each scope opens exactly its one route with its one method, and the three reading ones change nothing.

## The app that offers

```go
// one value, used for the manifest, the routes and linkAllows
var capabilities = core.Capabilities{
    Search:  &core.Searchable{What: "Your notes, by title and text", Kinds: []string{"note"}, Bangs: []string{"notes"}},
    Day:     &core.DayFeed{Label: "Notes", One: "note", Many: "notes", What: "The notes you wrote or changed that day"},
    Widget:  &core.Widget{Title: "Recent notes", What: "The notes you touched last"},
    Capture: &core.QuickCapture{Kinds: []core.CaptureKind{{Kind: "note", Label: "Note", What: "A new note in Quick Notes", Fields: []string{"title"}}}},
}

// manifest
core.Manifest{Slug: "notes", …, Category: "Everyday", Capabilities: capabilities}

// routes, inside the group that already has the app's auth (/api/v1)
r.Get("/pocket/search", core.SearchHandler(s.pocketSearch))
r.Get("/pocket/day/{date}", core.DayHandler(s.pocketDay))
r.Get("/pocket/widget", core.WidgetHandler(s.pocketWidget))
r.Post("/pocket/capture", capabilities.Capture.CaptureHandler(s.pocketCapture))

// linkAllows
if capabilities.LinkAllows(scopes, r) { return true }
```

`Register` adds the four offers (`pocket:search`…) with the same name and description in every app, so a person reads the same sentence whichever app it is about. The handlers do the dull part: they trim and cap the query and the limit, check the date, the capture's kind, request id and dates, drop items without a title, cut long lines, and answer `500 {"error": "the app could not answer"}` without the error's text. The app's function only fetches:

```go
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error)
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error)   // nil = nothing that day
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error)                         // nil = nothing to show
func (s *Server) pocketCapture(r *http.Request, in core.CaptureInput) (*core.CaptureResult, error)
```

### Shapes

```jsonc
// GET /api/v1/pocket/search?q=bread&limit=8
{ "items": [ { "id": "12", "title": "Bread recipe", "snippet": "…flour, water, salt…", "kind": "note",
               "date": "2026-11-20T08:12:00Z", "url": "/note/12", "link": "", "score": 3.2 } ] }

// GET /api/v1/pocket/day/2026-11-27?tz=Europe/Sarajevo
{ "date": "2026-11-27", "summary": "3 notes written",
  "items": [ { "id": "12", "time": "09:30", "end": "", "title": "Bread recipe", "sub": "Recipes", "kind": "written",
               "minutes": 0, "done": false, "amount": 0, "currency": "", "qty": 0, "url": "/note/12", "image": "", "note": "" } ],
  "metrics": [ { "label": "Words", "value": "412" } ] }

// GET /api/v1/pocket/widget
{ "line": "2 due today, 1 late", "metrics": [ { "label": "Due", "value": "2" } ],
  "items": [ { "id": "7", "title": "Call the plumber", "detail": "today", "done": false, "url": "/task/7" } ], "url": "/today" }

// POST /api/v1/pocket/capture
{ "request_id": "2f1c…(16–80 of A-Z a-z 0-9 _ -)", "kind": "note", "title": "", "text": "Milk, eggs", "due": "", "date": "" }
→ 201 { "id": "44", "title": "Milk, eggs", "url": "/note/44" }      // 400 {"error": "…"} for something the person can fix
```

- `url` and `image` are a path in the app (`/note/12`) or an absolute URL; the SDK's callers make them absolute.
- `time` and `end` are local wall-clock `HH:MM`; an item without one happened "sometime that day". `amount` is minor units of `currency`.
- Search: `score` orders matches inside the app (higher first); `0` everywhere = the order given. An empty `q` answers `{"items": []}` without calling the app's function.
- Day: `summary` empty and no items = nothing happened; the reader shows nothing for the app that day.
- Capture adds one new item. The same `request_id` again must answer the same and add nothing (the app's own mechanism, or the SDK's `NewOperation` / `Replay` / `Save`). Return `fmt.Errorf("%w: the list is full", core.ErrCapture)` for a refusal the person can act on.
- Limits: 50 search items, 200 day items, 8 widget rows and 4 metrics, lines cut at 300 (200 in a widget) characters.

## The app that asks

It declares the scope once, for every app:

```go
Uses: []core.Use{core.UseEvery(core.ScopeSearch, "Find your things in every app that can be searched")}
```

and then:

```go
apps, err := s.core.Capable(ctx, user.Email, core.ScopeSearch)   // who offers it, and how the link stands for this person
for _, a := range apps {
    // a.App, a.Name, a.Icon (absolute), a.PublicURL, a.Category, a.On, a.State, a.Detail, a.Search.Bangs…
    if !a.On { continue }
    items, err := s.core.SearchIn(ctx, user.Email, a.App, q, 8)  // also DayIn, WidgetIn, CaptureIn
}
```

`Capable` leaves out apps that are not registered or no longer offer the scope. `SearchIn` and the others take a fresh link token for the one scope (`ActAs`), call the route and return the typed answer; a link that is off comes back as `*sdk.LinkError`, an app that refuses or fails as `*sdk.CapabilityError`.

A person signed in to the app can call the same routes with their own session or token: Pocket Hub, which acts with the person's own Core token rather than a link, reads the manifest's `widget` and `capture` from `GET /api/v1/apps` and calls the routes as the person.

## "Every app" in Core

`uses` with `"app": "*"` is a **rule** rather than a link: "this scope, in every app that offers it" ([links](links.md#every-app)). Core keeps an ordinary link to each app that offers the scope, makes one when an app that offers it registers, and hides it when the app stops offering it. So:

- the admin has **one switch** per rule on the Links page (the acting app's card, on top), which also decides how a newly installed app starts;
- every link under it still has its own switch for the box and for each person, as any link;
- installing an app that offers a capability needs no visit to the Links page when the rule is on.

A rule in the manifest an app enrolls with is approved, like its links; one added by a later update starts off until an admin switches it on.

## What a new app no longer needs

- **In Core:** nothing. An app Core has never heard of registers with its name, icon, `category` and env prefix from its manifest (the catalog entry and the bundled icon are only for apps and services that are not installed yet).
- **In Search, Day, Hub:** nothing, once the app declares the capability. The hand-written adapters in those apps stay for apps that predate the contract:
  - Search and Hub read an app through the contract as soon as it offers it (and its link is on), and fall back to their own adapter otherwise.
  - Day keeps its hand-written source for the apps that have one (their cards show more than the common shape can, and old days are stored in that shape); apps without one become sources through the contract.
