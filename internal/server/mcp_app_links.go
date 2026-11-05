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
	if addr := mcpWorkspaceSaid(op, input); addr != "" && addr != strings.ToLower(base.Host) {
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
