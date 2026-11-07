package server

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Browser routes verified against the application routers, not API URLs.
// Empty item routes link to the containing page, not an individual record.
type mcpBrowserRoute struct{ page, item string }

var mcpBrowserRoutes = map[string]mcpBrowserRoute{
	"tasks/dashboard": {"/today", ""}, "review/dashboard": {"/reviews", ""}, "money/subscriptions": {"/subscriptions", "/subscriptions/{id}"}, "garage/reminders": {"/reminders", ""},
	"notes/notes": {"/", "/note/{id}"}, "notes/search": {"/", "/note/{id}"},
	"tasks/tasks": {"/inbox", "/inbox?task={id}"}, "tasks/lists": {"/inbox", "/list/{id}"},
	"calendar/events": {"/", "/event/{id}"}, "calendar/search": {"/", "/event/{id}"},
	"habits/habits": {"/manage", ""}, "habits/today": {"/today", ""}, "habits/days": {"/today", ""},
	"day/entries": {"/journal", ""}, "day/days": {"/journal", ""}, "day/moments": {"/journal", ""},
	"inventory/items": {"/items", "/items/{id}"}, "inventory/locations": {"/places", ""},
	"bookshelf/books": {"/library", "/books/{id}"},
	"clips/clips":     {"/clipboard", ""}, "clips/snippets": {"/snippets", ""},
	"documents/documents": {"/documents", "/documents/{id}"},
	"garage/vehicles":     {"/vehicles", "/vehicles/{id}"},
	"kitchen/recipes":     {"/recipes", "/recipes/{id}"}, "kitchen/lists": {"/shopping", "/shopping/{id}"}, "kitchen/pantry": {"/pantry", ""},
	"money/accounts": {"/accounts", "/accounts/{id}"}, "money/transactions": {"/transactions", "/transactions/{id}"},
	"money/categories": {"/transactions", ""}, "money/reports": {"/reports", ""},
	"split/groups":      {"/groups", "/groups/{id}"},
	"invoicing/clients": {"/clients", "/clients/{id}"}, "invoicing/invoices": {"/invoices", "/invoices/{id}"},
	"review/reviews":    {"/reviews", "/reviews/{id}"},
	"pr-review/pulls":   {"/", "/pr/{id}"},
	"travel/trips":      {"/trips", "/trips/{id}"},
	"podcasts/podcasts": {"/podcasts", "/podcasts/{id}"}, "podcasts/episodes": {"/episodes", "/episodes/{id}"}, "podcasts/queue": {"/queue", "/episodes/{id}"},
	"subtitles/library": {"/library", ""}, "subtitles/files": {"/library", "/files/{id}"},
	"monitoring/servers": {"/", "/server/{id}"}, "monitoring/alerts": {"/alerts", ""},
	"backups/widget": {"/", ""}, "backups/coverage": {"/coverage", ""},
	"ai/threads": {"/", "/t/{id}"}, "ai/ask-context": {"/", "/t/{id}"},
	"projects/dashboard": {"/my-issues", ""}, "projects/issues": {"/my-issues", "/issue/{id}"}, "projects/projects": {"/projects", "/project/{id}"},
	"projects/teams": {"/teams", ""}, "projects/cycles": {"/projects", ""}, "projects/calendar": {"/calendar", ""}, "projects/search": {"/search", ""},
	"projects/notifications": {"/inbox", ""},
	"movement/summary":       {"/timeline", ""}, "movement/where": {"/timeline", ""}, "movement/now": {"/", ""}, "movement/day": {"/timeline", ""},
	"movement/insights": {"/insights", ""}, "movement/countries": {"/insights/countries", ""}, "movement/places": {"/places", "/places/{id}"},
	"movement/visits": {"/timeline", ""}, "movement/search": {"/places", "/places/{id}"},
	"home/overview": {"/", ""}, "home/due": {"/upkeep", ""}, "home/homes": {"/homes", "/homes/{id}"}, "home/things": {"/things", "/things/{id}"},
	"home/jobs": {"/upkeep", "/upkeep/{id}"}, "home/records": {"/log", "/log/{id}"}, "home/spending": {"/log", ""},
	"home/meters": {"/meters", "/meters/{id}"}, "home/pros": {"/pros", "/pros/{id}"}, "home/search": {"/", ""}, "home/plants": {"/plants", "/plants/{id}"},
	"pets/overview": {"/", ""}, "pets/due": {"/care", ""}, "pets/pets": {"/pets", "/pets/{id}"}, "pets/care": {"/care", "/care/{id}"}, "pets/records": {"/log", "/log/{id}"},
	"pets/weights": {"/pets", ""}, "pets/spending": {"/log", ""}, "pets/pros": {"/pros", "/pros/{id}"}, "pets/search": {"/", ""},
	"places/summary": {"/", ""}, "places/overview": {"/", ""}, "places/places": {"/places", "/places/{id}"}, "places/visits": {"/visits", ""},
	"places/lists": {"/lists", "/lists/{id}"}, "places/inbox": {"/", ""}, "places/search": {"/places", ""}, "places/day": {"/visits", ""},
	"places/spending": {"/visits", ""}, "places/trips": {"/", ""},
	"wallet/summary": {"/", ""}, "wallet/overview": {"/", ""}, "wallet/cards": {"/cards", "/cards/{id}"}, "wallet/passes": {"/passes", "/cards/{id}"},
	"wallet/expiring": {"/ids", "/cards/{id}"}, "wallet/search": {"/cards", "/cards/{id}"}, "wallet/day": {"/passes", ""},
	"weather/summary": {"/", ""}, "weather/forecast": {"/", ""}, "weather/warnings": {"/warnings", ""}, "weather/day": {"/diary", ""},
	"weather/diary": {"/diary", ""}, "weather/locations": {"/places", ""},
	"fitness/summary": {"/", ""}, "fitness/week": {"/", ""}, "fitness/day": {"/history", ""}, "fitness/workouts": {"/history", "/workouts/{id}"},
	"fitness/records": {"/progress", ""}, "fitness/progress": {"/progress", ""}, "fitness/routines": {"/routines", "/routines/{id}"},
	"fitness/plan": {"/routines", ""}, "fitness/exercises": {"/exercises", "/exercises/{id}"},
	"nutrition/summary": {"/", ""}, "nutrition/day": {"/", ""}, "nutrition/week": {"/", ""}, "nutrition/trends": {"/trends", ""}, "nutrition/entries": {"/", ""},
	"nutrition/water": {"/", ""}, "nutrition/foods": {"/foods", "/foods/{id}"}, "nutrition/meals": {"/meals", ""},
	"blog/summary": {"/", ""}, "blog/blogs": {"/look", ""}, "blog/posts": {"/posts", "/posts/{id}"}, "blog/search": {"/posts", "/posts/{id}"}, "blog/comments": {"/comments", ""},
	"chat/summary": {"/", ""}, "chat/conversations": {"/", "/c/{id}"}, "chat/search": {"/search", ""}, "chat/unread": {"/", ""}, "chat/day": {"/", ""},
	"chat/people": {"/", ""}, "chat/saved": {"/saved", ""},
	"forms/summary": {"/", ""}, "forms/forms": {"/forms", "/forms/{id}"}, "forms/responses": {"/", "/answers/{id}"}, "forms/search": {"/", ""}, "forms/day": {"/", ""},
	"forms/templates": {"/forms/new", ""},
	"crm/summary":     {"/", ""}, "crm/today": {"/", ""}, "crm/deals": {"/deals", "/deals/{id}"}, "crm/search": {"/deals", ""}, "crm/people": {"/people", ""},
	"crm/lookup": {"/people", ""}, "crm/day": {"/", ""}, "crm/reports": {"/reports", ""}, "crm/stages": {"/pipeline", ""},
	"league/summary": {"/", ""}, "league/today": {"/", ""}, "league/upcoming": {"/games", "/games/{id}"}, "league/games": {"/games", "/games/{id}"},
	"league/groups": {"/", "/groups/{id}"}, "league/matches": {"/results", ""}, "league/search": {"/", ""}, "league/lookup": {"/", ""}, "league/day": {"/games", ""}, "league/tournaments": {"/tournaments", "/tournaments/{id}"},
	"tables/summary": {"/", ""}, "tables/today": {"/", ""}, "tables/bases": {"/", "/b/{id}"}, "tables/tables": {"/", "/t/{id}"}, "tables/records": {"/", "/r/{id}"},
	"tables/search": {"/", ""}, "tables/lookup": {"/", ""}, "tables/day": {"/", ""},
	"rentals/summary": {"/", ""}, "rentals/today": {"/", ""}, "rentals/rent": {"/rent", ""}, "rentals/leases": {"/leases", "/leases/{id}"},
	"rentals/properties": {"/properties", "/properties/{id}"}, "rentals/tenants": {"/tenants", "/tenants/{id}"}, "rentals/requests": {"/repairs", "/repairs/{id}"},
	"rentals/inspections": {"/inspections", "/inspections/{id}"}, "rentals/search": {"/", ""}, "rentals/lookup": {"/tenants", ""}, "rentals/entries": {"/", ""},
	"map/summary": {"/spots", ""}, "map/layers": {"/settings/apps", ""}, "map/features": {"/", ""}, "map/day": {"/timeline", ""}, "map/spots": {"/spots", ""},
	"map/lines": {"/spots", ""}, "map/views": {"/", ""}, "map/search": {"/", ""},
	"errors/summary": {"/", ""}, "errors/issues": {"/", "/issues/{id}"}, "errors/projects": {"/projects", "/projects/{id}"}, "errors/search": {"/", ""}, "errors/day": {"/", ""},
	"trails/summary": {"/", ""}, "trails/today": {"/", ""}, "trails/routes": {"/routes", "/routes/{id}"}, "trails/outings": {"/outings", "/outings/{id}"},
	"trails/day": {"/outings", ""}, "trails/search": {"/", ""}, "trails/stats": {"/year", ""}, "trails/gear": {"/gear", ""}, "trails/packs": {"/gear", ""},
	"blog/stats": {"/readers", ""}, "blog/day": {"/posts", ""}, "blog/tags": {"/posts", ""}, "blog/ideas": {"/", ""},
	"voice/summary": {"/", ""}, "voice/memos": {"/memos", "/memos/{id}"}, "voice/search": {"/memos", ""}, "voice/day": {"/memos", ""}, "voice/tags": {"/memos", ""},
	"renewals/overview": {"/", ""}, "renewals/due": {"/all?status=soon", ""}, "renewals/renewals": {"/all", "/r/{id}"}, "renewals/history": {"/history", ""},
	"renewals/search": {"/all", "/r/{id}"}, "renewals/travel": {"/", ""},
	"wealth/summary": {"/", ""}, "wealth/history": {"/", ""}, "wealth/assets": {"/assets", "/a/{id}"}, "wealth/positions": {"/investments", ""},
	"wealth/activity": {"/investments", ""}, "wealth/debts": {"/debts", "/a/{id}"}, "wealth/plan": {"/plan", ""}, "wealth/search": {"/assets", "/a/{id}"},
	"wealth/maturing": {"/", ""}, "wealth/valuations": {"/assets", ""},
	"parcels/summary": {"/", ""}, "parcels/overview": {"/", ""}, "parcels/parcels": {"/parcels", "/parcels/{id}"}, "parcels/search": {"/parcels", ""},
	"parcels/day": {"/", ""}, "parcels/stats": {"/", ""},
	"sign/summary": {"/", ""}, "sign/overview": {"/", ""}, "sign/documents": {"/documents", "/documents/{id}"}, "sign/templates": {"/templates", "/templates/{id}/edit"},
	"sign/inbox": {"/", ""}, "sign/search": {"/documents", ""}, "sign/day": {"/", ""}, "sign/stats": {"/", ""},
}

var mcpLinkID = regexp.MustCompile(`^(?:[1-9][0-9]*|[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})$`)
var mcpIssueIdent = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,6}-[1-9][0-9]{0,9}$`)
var mcpLinkDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var mcpTrailsRoutePath = regexp.MustCompile(`^/routes/[1-9][0-9]*$`)

func addMCPAppLinks(result *mcp.CallToolResult, publicURL string, op *mcpAppTool, input map[string]any) *mcp.CallToolResult {
	if result.IsError {
		return result
	}
	base, err := url.Parse(publicURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && base.Scheme != "http") {
		return result
	}
	envelope, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return result
	}
	// Preserve registered path prefixes; never derive an origin from item content.
	// a connected workspace's pages: /r/<its address>/… (Projects sends that to the person's /r/{id}/…)
	ws := ""
	addr := mcpWorkspaceSaid(op, input)
	if said, _ := envelope["workspace"].(string); addr == "" && op.App == "projects" && mcpWorkspaceAddr.MatchString(said) {
		addr = said // named by Projects: the workspace that answered
	}
	if addr != "" && addr != strings.ToLower(base.Host) {
		ws = "/r/" + addr
	}
	link := func(path string) string { return strings.TrimRight(base.String(), "/") + ws + path }
	envelope["app_url"] = link("/")
	parts := strings.Split(strings.Trim(op.Path, "/"), "/")
	route, known := mcpBrowserRoutes[op.App+"/"+parts[0]]
	if !known {
		route.page = "/"
	}
	source := link(route.page)
	parent := ""
	if len(parts) > 1 && strings.HasPrefix(parts[1], "{") {
		id := mcpScalar(input[strings.Trim(parts[1], "{}")])
		if route.item != "" && (mcpLinkID.MatchString(id) || op.App == "projects" && mcpIssueIdent.MatchString(id)) {
			parent = link(strings.ReplaceAll(route.item, "{id}", id))
			source = parent
		}
		if op.App == "movement" && parts[0] == "day" && mcpLinkDate.MatchString(id) {
			source = link("/timeline/" + id)
		}
		if parts[0] == "days" && mcpLinkDate.MatchString(id) {
			if op.App == "day" {
				source = link("/day/" + id)
			}
			if op.App == "habits" {
				source = link("/today/" + id)
			}
		}
	}
	// Pocket Weather: a forecast of one of the person's places opens that place's page (home is /).
	if op.App == "weather" && parts[0] == "forecast" {
		if q, ok := input["query"].(map[string]any); ok && mcpLinkID.MatchString(mcpScalar(q["location"])) {
			source = link("/places/" + mcpScalar(q["location"]))
		}
	}
	envelope["source_url"] = source
	// Dashboard collections have different record kinds; decorate only their
	// verified item fields, never arbitrary nested author or comment IDs.
	itemLink := func(record map[string]any, field, route string) {
		id := mcpScalar(record[field])
		if mcpLinkID.MatchString(id) {
			record["item_url"] = link(strings.ReplaceAll(route, "{id}", id))
		}
	}
	if data, ok := envelope["result"].(map[string]any); ok {
		if op.App == "tasks" && op.Path == "/dashboard" {
			if rows, ok := data["next"].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/inbox?task={id}")
					}
				}
			}
		}
		if op.App == "pr-review" {
			if rows, ok := data["pulls"].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/pr/{id}")
					}
				}
			}
		}
		if op.App == "review" && op.Path == "/dashboard" {
			for _, key := range []string{"todo", "waiting", "tasks", "live"} {
				if rows, ok := data[key].([]any); ok {
					for _, row := range rows {
						if m, ok := row.(map[string]any); ok {
							if review, ok := m["review"].(map[string]any); ok {
								itemLink(review, "id", "/reviews/{id}")
							} else {
								itemLink(m, "review_id", "/reviews/{id}")
							}
						}
					}
				}
			}
		}
	}
	// Pocket Home's overview, due list and search mix record kinds: each list has its own page.
	// Pocket Renewals: every list in the overview and the due list holds renewals; history rows point at theirs.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "renewals" {
		for _, key := range []string{"expired", "soon", "check", "later", "renewals", "history"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/r/{id}")
					}
				}
			}
		}
		for _, key := range []string{"recent", "travel"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "renewal_id", "/r/{id}")
					}
				}
			}
		}
		if r, ok := data["renewal"].(map[string]any); ok {
			itemLink(r, "id", "/r/{id}")
		}
	}
	// Pocket Wealth: assets, debts and what needs a look link to the asset; holdings and activity to theirs.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "wealth" {
		for _, key := range []string{"assets", "debts", "stale", "maturing", "linked_errors", "items"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/a/{id}")
					}
				}
			}
		}
		for _, key := range []string{"positions", "activity"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "asset_id", "/a/{id}")
					}
				}
			}
		}
		if r, ok := data["asset"].(map[string]any); ok {
			itemLink(r, "id", "/a/{id}")
		}
	}
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "home" && (op.Path == "/overview" || op.Path == "/due" || op.Path == "/search") {
		routes := map[string]string{"attention": "/upkeep/{id}", "upcoming": "/upkeep/{id}", "overdue": "/upkeep/{id}", "due": "/upkeep/{id}", "soon": "/upkeep/{id}",
			"recent": "/log/{id}", "jobs": "/upkeep/{id}", "records": "/log/{id}", "things": "/things/{id}", "homes": "/homes/{id}", "pros": "/pros/{id}", "meters": "/meters/{id}"}
		for key, r := range routes {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", r)
					}
				}
			}
		}
		if rows, ok := data["warranties"].([]any); ok {
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					itemLink(m, "thing_id", "/things/{id}")
				}
			}
		}
	}
	// Pocket Pets' overview, due list and search mix record kinds too.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "pets" && (op.Path == "/overview" || op.Path == "/due" || op.Path == "/search") {
		routes := map[string]string{"attention": "/care/{id}", "upcoming": "/care/{id}", "overdue": "/care/{id}", "due": "/care/{id}", "soon": "/care/{id}",
			"recent": "/log/{id}", "care": "/care/{id}", "records": "/log/{id}", "pets": "/pets/{id}", "pros": "/pros/{id}"}
		for key, r := range routes {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", r)
					}
				}
			}
		}
		for _, key := range []string{"ending", "birthdays"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "pet_id", "/pets/{id}")
					}
				}
			}
		}
	}
	// Pocket Places: places link to their page; visits, plans and "how was it?" link to their place's page.
	// Pocket Weather: places (the list, an added place, a summary's or forecast's place) link to their page, home to /; warnings to their place.
	if op.App == "weather" {
		placeLink := func(m map[string]any, field string) {
			if home, _ := m["is_home"].(bool); home && field == "id" {
				m["item_url"] = link("/")
				return
			}
			itemLink(m, field, "/places/{id}")
		}
		rows, _ := envelope["result"].([]any)
		data, _ := envelope["result"].(map[string]any)
		if data != nil {
			if _, ok := data["lat"]; ok && op.Path == "/locations" {
				placeLink(data, "id")
			}
			for _, key := range []string{"place", "location"} {
				if m, ok := data[key].(map[string]any); ok {
					placeLink(m, "id")
				}
			}
			for _, key := range []string{"warnings", "active", "history"} {
				if list, ok := data[key].([]any); ok {
					for _, row := range list {
						if m, ok := row.(map[string]any); ok {
							placeLink(m, "location_id")
						}
					}
				}
			}
		}
		if op.Path == "/locations" {
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					placeLink(m, "id")
				}
			}
		}
	}
	// Pocket Fitness: workouts link to their page (a logged one opens there), planned sessions and the plan to their routine,
	// records to their exercise, an exercise's history to its workouts. Pocket Health's runs in a week have no page here.
	if op.App == "fitness" {
		each := func(list any, field, route string) {
			if rows, ok := list.([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						if health, _ := m["health"].(bool); !health {
							itemLink(m, field, route)
						}
					}
				}
			}
		}
		one := func(v any, field, route string) {
			if m, ok := v.(map[string]any); ok {
				itemLink(m, field, route)
			}
		}
		if data, ok := envelope["result"].(map[string]any); ok {
			for _, key := range []string{"last_workout", "in_progress", "workout"} {
				one(data[key], "id", "/workouts/{id}")
			}
			one(data["routine"], "id", "/routines/{id}")
			one(data["exercise"], "id", "/exercises/{id}")
			one(data["today"], "routine_id", "/routines/{id}")
			one(data["planned"], "routine_id", "/routines/{id}")
			each(data["workouts"], "id", "/workouts/{id}")
			each(data["records"], "exercise_id", "/exercises/{id}")
			each(data["recent"], "exercise_id", "/exercises/{id}")
			each(data["history"], "workout_id", "/workouts/{id}")
			if days, ok := data["days"].([]any); ok {
				for _, d := range days {
					if m, ok := d.(map[string]any); ok {
						each(m["items"], "id", "/workouts/{id}")
						one(m["planned"], "routine_id", "/routines/{id}")
					}
				}
			}
			if w, ok := data["workout"].(map[string]any); ok && op.Path == "/workouts/log" {
				if u, ok := w["item_url"].(string); ok {
					envelope["source_url"] = u
				}
			}
		}
		if op.Path == "/plan" {
			each(envelope["result"], "routine_id", "/routines/{id}")
		}
	}
	// Pocket Nutrition: the diary is one page per day (/?date=YYYY-MM-DD). A day, a week, the entries of a day and what was
	// just logged open that day; entries and the days of a week or trends link to their day, foods (and the foods in saved
	// meals) to /foods/{id}. Built-in foods (id 0) have no page.
	if op.App == "nutrition" {
		dayURL := func(date any) string {
			if d, ok := date.(string); ok && mcpLinkDate.MatchString(d) {
				return link("/?date=" + d)
			}
			return ""
		}
		days := func(list any) {
			if rows, ok := list.([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						if u := dayURL(m["date"]); u != "" {
							m["item_url"] = u
						}
					}
				}
			}
		}
		foods := func(list any, field string) {
			if rows, ok := list.([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, field, "/foods/{id}")
					}
				}
			}
		}
		if parts[0] == "day" || parts[0] == "water" {
			if u := dayURL(input["date"]); u != "" {
				envelope["source_url"] = u
			}
		}
		if q, ok := input["query"].(map[string]any); ok && (parts[0] == "week" || parts[0] == "entries") {
			if u := dayURL(q["date"]); u != "" {
				envelope["source_url"] = u
			}
		}
		switch data := envelope["result"].(type) {
		case []any:
			if op.Path == "/meals" {
				for _, row := range data {
					if m, ok := row.(map[string]any); ok {
						foods(m["items"], "food_id")
					}
				}
			} else {
				days(data)
			}
		case map[string]any:
			days(data["days"])
			if w, ok := data["week"].(map[string]any); ok {
				days(w["days"])
			}
			if meals, ok := data["meals"].([]any); ok {
				for _, meal := range meals {
					if m, ok := meal.(map[string]any); ok {
						foods(m["entries"], "food_id")
					}
				}
			}
			if op.Path == "/entries/log" {
				days(data["entries"])
				if rows, ok := data["entries"].([]any); ok && len(rows) > 0 {
					if m, ok := rows[0].(map[string]any); ok {
						if u := dayURL(m["date"]); u != "" {
							envelope["source_url"] = u
						}
					}
				}
			}
		}
	}
	// Pocket Wallet: cards, passes and IDs in a summary or a day link to their page.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "wallet" {
		for _, key := range []string{"today", "expiring", "gift_cards", "cards", "ids", "next", "passes"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/cards/{id}")
					}
				}
			}
		}
		if m, ok := data["next"].(map[string]any); ok {
			itemLink(m, "id", "/cards/{id}")
		}
	}
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "places" {
		for _, key := range []string{"favourites", "want", "places", "nearby", "been", "added"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/places/{id}")
					}
				}
			}
		}
		for _, key := range []string{"recent", "planned", "past_plans", "visits", "items", "inbox"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "place_id", "/places/{id}")
					}
				}
			}
		}
		if rows, ok := data["lists"].([]any); ok {
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					itemLink(m, "id", "/lists/{id}")
				}
			}
		}
		if m, ok := data["last_visit"].(map[string]any); ok {
			itemLink(m, "place_id", "/places/{id}")
		}
	}
	// Pocket Chat: a conversation opens at /c/{id}; a message (search, saved, a conversation's messages, mentions) opens its conversation.
	if op.App == "chat" {
		var list []any
		switch r := envelope["result"].(type) {
		case []any:
			list = r
		case map[string]any:
			for _, key := range []string{"results", "waiting", "mentions"} {
				if rows, ok := r[key].([]any); ok {
					list = append(list, rows...)
				}
			}
			if _, isMsg := r["conversation_id"]; isMsg {
				itemLink(r, "conversation_id", "/c/{id}")
			}
		}
		for _, row := range list {
			if m, ok := row.(map[string]any); ok {
				if _, isMsg := m["conversation_id"]; isMsg {
					itemLink(m, "conversation_id", "/c/{id}")
				} else {
					itemLink(m, "id", "/c/{id}")
				}
			}
		}
	}
	// Pocket Forms: answers in a list, the summary's latest and a day link to the answer; forms (a list, the summary's list, one form) to the form's page.
	if op.App == "forms" {
		switch r := envelope["result"].(type) {
		case []any:
			if op.Path == "/forms" {
				for _, row := range r {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/forms/{id}")
					}
				}
			}
		case map[string]any:
			if op.Path == "/responses" || op.Path == "/summary" || op.Path == "/day/{date}" {
				for _, key := range []string{"items", "latest", "responses"} {
					if rows, ok := r[key].([]any); ok {
						for _, row := range rows {
							if m, ok := row.(map[string]any); ok {
								itemLink(m, "id", "/answers/{id}")
							}
						}
					}
				}
			}
			if rows, ok := r["list"].([]any); ok && op.Path == "/summary" {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/forms/{id}")
					}
				}
			}
			if resp, ok := r["response"].(map[string]any); ok {
				itemLink(resp, "id", "/answers/{id}")
			}
			if _, isForm := r["handle"]; isForm {
				itemLink(r, "id", "/forms/{id}")
			}
		}
	}
	// Pocket CRM: every deal in a list (deals, search, lookup, the day's due, each Today list) opens at /deals/{id};
	// so do a deal read or added, the day's timeline lines and a line written on a deal (their deal_id).
	if r, ok := envelope["result"].(map[string]any); ok && op.App == "crm" {
		deals := func(rows any, field string) {
			if list, ok := rows.([]any); ok {
				for _, row := range list {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, field, "/deals/{id}")
					}
				}
			}
		}
		for _, key := range []string{"items", "deals", "due"} {
			deals(r[key], "id")
		}
		deals(r["activities"], "deal_id")
		if lists, ok := r["lists"].(map[string]any); ok && op.Path == "/today" {
			for _, rows := range lists {
				deals(rows, "id")
			}
		}
		if d, ok := r["deal"].(map[string]any); ok {
			itemLink(d, "id", "/deals/{id}")
		}
		if _, isDeal := r["stage_id"]; isDeal {
			itemLink(r, "id", "/deals/{id}")
		}
		if _, isLine := r["kind"]; isLine && op.Path == "/deals/{id}/activities" {
			itemLink(r, "deal_id", "/deals/{id}")
		}
	}
	// Pocket League: games open at /games/{id}, groups at /groups/{id}; a result opens its game (else its group's Results tab),
	// a table row the player's page in that group. A group's table and an answer open their own page. Tournaments open at
	// /tournaments/{id} (one group's list at its Tournaments tab); a tournament's "groups" are its pools, not League groups.
	if data, ok := envelope["result"]; ok && op.App == "league" {
		each := func(rows any, fn func(map[string]any)) {
			if list, ok := rows.([]any); ok {
				for _, row := range list {
					if m, ok := row.(map[string]any); ok {
						fn(m)
					}
				}
			}
		}
		game := func(m map[string]any) { itemLink(m, "id", "/games/{id}") }
		result := func(m map[string]any) {
			if mt, ok := m["match"].(map[string]any); ok {
				if mcpLinkID.MatchString(mcpScalar(mt["game_id"])) {
					itemLink(mt, "game_id", "/games/{id}")
				} else {
					itemLink(mt, "group_id", "/groups/{id}?tab=results")
				}
			}
		}
		gid := mcpScalar(input["id"])
		if !mcpLinkID.MatchString(gid) {
			gid = ""
		}
		switch r := data.(type) {
		case []any:
			if op.Path == "/matches" {
				each(r, result)
			}
			if op.Path == "/groups" {
				each(r, func(m map[string]any) {
					if n, ok := m["next_game"].(map[string]any); ok {
						game(n)
					}
				})
			}
			if op.Path == "/tournaments" {
				each(r, func(m map[string]any) { itemLink(m, "id", "/tournaments/{id}") })
				if g := mcpScalar(input["group_id"]); mcpLinkID.MatchString(g) {
					envelope["source_url"] = link("/groups/" + g + "?tab=tournaments")
				}
			}
		case map[string]any:
			for _, key := range []string{"next", "unanswered", "voting", "upcoming", "games"} {
				each(r[key], game)
			}
			each(r["confirm"], result)
			each(r["recent"], result)
			if n, ok := r["next_game"].(map[string]any); ok {
				game(n)
			}
			if g, ok := r["game"].(map[string]any); ok {
				game(g)
			}
			if g, ok := r["group"].(map[string]any); ok {
				itemLink(g, "id", "/groups/{id}")
			}
			if t, ok := r["tournament"].(map[string]any); ok && op.Path == "/tournaments/{id}" {
				itemLink(t, "id", "/tournaments/{id}")
			} else {
				each(r["groups"], func(m map[string]any) { itemLink(m, "id", "/groups/{id}") })
			}
			if op.Path == "/search" {
				each(r["results"], func(m map[string]any) {
					switch m["kind"] {
					case "group":
						itemLink(m, "id", "/groups/{id}")
					case "game":
						game(m)
					}
				})
			}
			if gid != "" && op.Path == "/groups/{id}/table" {
				each(r["rows"], func(m map[string]any) { itemLink(m, "user_id", "/groups/"+gid+"/players/{id}") })
				envelope["source_url"] = link("/groups/" + gid + "?tab=table")
			}
			if uid := mcpScalar(input["uid"]); gid != "" && op.Path == "/groups/{id}/players/{uid}" && mcpLinkID.MatchString(uid) {
				envelope["source_url"] = link("/groups/" + gid + "/players/" + uid)
			}
		}
	}
	// Pocket Tables: rows open at /r/{id}, tables at /t/{id}, bases at /b/{id}: every row in a list (rows, search, lookup,
	// a day, the summary's and Today's lists), the bases with their tables, a table read with its base, a row read or added.
	if data, ok := envelope["result"]; ok && op.App == "tables" {
		each := func(rows any, route string) {
			if list, ok := rows.([]any); ok {
				for _, row := range list {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", route)
					}
				}
			}
		}
		switch r := data.(type) {
		case []any:
			if op.Path == "/bases" {
				each(r, "/b/{id}")
				for _, b := range r {
					if m, ok := b.(map[string]any); ok {
						each(m["tables"], "/t/{id}")
					}
				}
			}
		case map[string]any:
			for _, key := range []string{"items", "records", "recent", "soon", "late", "today"} {
				each(r[key], "/r/{id}")
			}
			if op.Path == "/search" {
				each(r["tables"], "/t/{id}")
			}
			if t, ok := r["table"].(map[string]any); ok {
				itemLink(t, "id", "/t/{id}")
			}
			if b, ok := r["base"].(map[string]any); ok {
				itemLink(b, "id", "/b/{id}")
			}
			if rec, ok := r["record"].(map[string]any); ok {
				itemLink(rec, "id", "/r/{id}")
			}
			if _, isRow := r["number"]; isRow && op.Path == "/tables/{id}/records" {
				itemLink(r, "id", "/r/{id}")
			}
		}
	}
	// Pocket Rentals: leases open at /leases/{id}, places at /properties/{id}, tenants at /tenants/{id}, repairs (the API's requests)
	// at /repairs/{id}, inspections at /inspections/{id}. A rent line (Today's late and due, a month's items) opens its lease; an empty
	// unit its place; a search hit by its kind; a line written on a timeline the repair, lease or place it is on (and opens there, as
	// a repair reported and a tenant looked up open theirs). Plain lists get their links from the browser routes above.
	if r, ok := envelope["result"].(map[string]any); ok && op.App == "rentals" {
		each := func(rows any, field, route string) {
			if list, ok := rows.([]any); ok {
				for _, row := range list {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, field, route)
					}
				}
			}
		}
		for _, key := range []string{"late", "due"} {
			each(r[key], "lease_id", "/leases/{id}")
		}
		if op.Path == "/rent" {
			each(r["items"], "lease_id", "/leases/{id}")
		}
		for _, key := range []string{"ending", "drafts", "leases"} {
			each(r[key], "id", "/leases/{id}")
		}
		for _, key := range []string{"repairs", "requests"} {
			each(r[key], "id", "/repairs/{id}")
		}
		each(r["inspections"], "id", "/inspections/{id}")
		each(r["vacant"], "property_id", "/properties/{id}")
		for key, route := range map[string]string{"lease": "/leases/{id}", "property": "/properties/{id}", "tenant": "/tenants/{id}", "request": "/repairs/{id}", "inspection": "/inspections/{id}"} {
			if m, ok := r[key].(map[string]any); ok {
				itemLink(m, "id", route)
			}
		}
		if op.Path == "/search" {
			kinds := map[string]string{"property": "/properties/{id}", "lease": "/leases/{id}", "tenant": "/tenants/{id}", "repair": "/repairs/{id}"}
			if list, ok := r["results"].([]any); ok {
				for _, row := range list {
					if m, ok := row.(map[string]any); ok {
						if route, ok := kinds[mcpScalar(m["kind"])]; ok {
							itemLink(m, "id", route)
						}
					}
				}
			}
		}
		if op.Path == "/entries" {
			itemLink(r, "property_id", "/properties/{id}")
			itemLink(r, "lease_id", "/leases/{id}")
			itemLink(r, "request_id", "/repairs/{id}")
			if u, ok := r["item_url"].(string); ok {
				envelope["source_url"] = u
			}
		}
		for key, path := range map[string]string{"request": "/requests", "tenant": "/lookup"} {
			if m, ok := r[key].(map[string]any); ok && op.Path == path {
				if u, ok := m["item_url"].(string); ok {
					envelope["source_url"] = u
				}
			}
		}
	}
	// Pocket Trails: outings open at /outings/{id}, routes at /routes/{id}, gear and packing lists on /gear. An outing's page (one
	// outing, Today's current, a plan or a status change) holds {outing, route}; Today's and the summary's lists hold outings, Today's
	// ideas routes; a year's longest is an outing; a route's page lists its outings; search hits go by their type (a spot opens its
	// route). A planned outing and a status change open the outing.
	if r, ok := envelope["result"].(map[string]any); ok && op.App == "trails" {
		each := func(rows any, route string) {
			if list, ok := rows.([]any); ok {
				for _, row := range list {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", route)
					}
				}
			}
		}
		page := func(p map[string]any) {
			if o, ok := p["outing"].(map[string]any); ok {
				itemLink(o, "id", "/outings/{id}")
			}
			if rt, ok := p["route"].(map[string]any); ok {
				itemLink(rt, "id", "/routes/{id}")
			}
		}
		page(r)
		if cur, ok := r["current"].(map[string]any); ok {
			page(cur)
		}
		for _, key := range []string{"upcoming", "recent", "unmarked", "out", "outings"} {
			each(r[key], "/outings/{id}")
		}
		each(r["ideas"], "/routes/{id}")
		if n, ok := r["next"].(map[string]any); ok {
			itemLink(n, "id", "/outings/{id}")
		}
		for _, key := range []string{"year", "previous"} {
			if y, ok := r[key].(map[string]any); ok {
				if l, ok := y["longest"].(map[string]any); ok {
					itemLink(l, "id", "/outings/{id}")
				}
			}
		}
		if op.Path == "/search" {
			if list, ok := r["results"].([]any); ok {
				for _, row := range list {
					m, ok := row.(map[string]any)
					if !ok {
						continue
					}
					switch mcpScalar(m["type"]) {
					case "route":
						itemLink(m, "id", "/routes/{id}")
					case "outing":
						itemLink(m, "id", "/outings/{id}")
					case "gear":
						m["item_url"] = link("/gear")
					case "spot":
						if p := mcpScalar(m["path"]); mcpTrailsRoutePath.MatchString(p) {
							m["item_url"] = link(p)
						}
					}
				}
			}
		}
		if op.Path == "/outings" { // a list is an array: this is a planned outing's page
			if o, ok := r["outing"].(map[string]any); ok {
				if u, ok := o["item_url"].(string); ok {
					envelope["source_url"] = u
				}
			}
		}
	}
	// Pocket Blog: posts in lists, search results, the summary's drafts and a day link to their editor page; so does a post itself.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "blog" {
		for _, key := range []string{"items", "results", "drafts", "scheduled", "recent", "published", "written"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/posts/{id}")
					}
				}
			}
		}
		if _, isPost := data["slug"]; isPost {
			itemLink(data, "id", "/posts/{id}")
		}
	}
	// Pocket Voice: memos in a list, search, the summary's inbox and a day link to their page; so does the memo a send returns.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "voice" {
		for _, key := range []string{"items", "latest_inbox", "memos"} {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/memos/{id}")
					}
				}
			}
		}
		if m, ok := data["memo"].(map[string]any); ok {
			itemLink(m, "id", "/memos/{id}")
		}
	}
	// Pocket Parcels: every list in the summary, overview, search and a day holds parcels; a day's steps point at theirs.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "parcels" && (op.Path == "/summary" || op.Path == "/overview" || op.Path == "/search" || op.Path == "/day/{date}") {
		lists := []any{}
		for _, key := range []string{"needs_you", "on_the_way", "problems", "recent", "returns", "delivered", "sent", "added", "expected"} {
			lists = append(lists, data[key])
		}
		switch ps := data["parcels"].(type) {
		case []any:
			lists = append(lists, ps)
		case map[string]any:
			for _, l := range ps {
				lists = append(lists, l)
			}
		}
		for _, l := range lists {
			if rows, ok := l.([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						itemLink(m, "id", "/parcels/{id}")
					}
				}
			}
		}
		if rows, ok := data["steps"].([]any); ok {
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					itemLink(m, "parcel_id", "/parcels/{id}")
				}
			}
		}
	}
	// Pocket Sign: documents link to their page, what waits for the person to their signing page (/inbox/<recipient>), history lines to their document.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "sign" && (op.Path == "/summary" || op.Path == "/overview" || op.Path == "/inbox" || op.Path == "/search" || op.Path == "/day/{date}") {
		each := func(key string, fn func(map[string]any)) {
			if rows, ok := data[key].([]any); ok {
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok {
						fn(m)
					}
				}
			}
		}
		for _, key := range []string{"waiting", "drafts", "recent", "documents", "sent", "completed"} {
			if op.Path == "/inbox" {
				break
			}
			each(key, func(m map[string]any) { itemLink(m, "id", "/documents/{id}") })
		}
		for _, key := range []string{"to_sign", "signed_by_me"} {
			each(key, func(m map[string]any) { itemLink(m, "recipient_id", "/inbox/{id}") })
		}
		if op.Path == "/inbox" {
			for _, key := range []string{"waiting", "done"} {
				each(key, func(m map[string]any) { itemLink(m, "recipient_id", "/inbox/{id}") })
			}
		}
		for _, key := range []string{"activity", "signed"} {
			each(key, func(m map[string]any) { itemLink(m, "document_id", "/documents/{id}") })
		}
	}
	// A draft made from a template opens on its own page (send it from there), not the template's.
	if data, ok := envelope["result"].(map[string]any); ok && op.App == "sign" && op.Path == "/templates/{id}/use" {
		itemLink(data, "id", "/documents/{id}")
		if u, ok := data["item_url"].(string); ok {
			envelope["source_url"] = u
		}
	}
	if op.App == "garage" && op.Path == "/reminders" {
		if rows, ok := envelope["result"].([]any); ok {
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					itemLink(m, "vehicle_id", "/vehicles/{id}/reminders")
				}
			}
		}
	}

	// Only decorate result records or known pagination wrappers. Do not mistake
	// nested comment/author IDs for review IDs or invoice line IDs for invoices.
	childRecords := len(parts) > 2
	decorate := func(record map[string]any) {
		if childRecords {
			return
		}
		if route.item != "" {
			id := mcpScalar(record["id"])
			if mcpLinkID.MatchString(id) {
				record["item_url"] = link(strings.ReplaceAll(route.item, "{id}", id))
			}
		}
	}
	decorateList := func(list []any) {
		for _, item := range list {
			if record, ok := item.(map[string]any); ok {
				decorate(record)
			}
		}
	}
	switch v := envelope["result"].(type) {
	case []any:
		decorateList(v)
	case map[string]any:
		decorate(v)
		if !mcpLinkID.MatchString(mcpScalar(v["id"])) {
			for _, key := range []string{"items", "results", parts[0]} {
				if list, ok := v[key].([]any); ok {
					decorateList(list)
				}
			}
		}
		if itemURL, ok := v["item_url"].(string); ok && parent == "" && !childRecords && route.item != "" && mcpLinkID.MatchString(mcpScalar(v["id"])) {
			envelope["source_url"] = itemURL
		}
	}
	return mcpAppResult(envelope, false)
}
