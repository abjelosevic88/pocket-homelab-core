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
	"travel/trips":      {"/trips", "/trips/{id}"},
	"podcasts/podcasts": {"/podcasts", "/podcasts/{id}"}, "podcasts/episodes": {"/episodes", "/episodes/{id}"}, "podcasts/queue": {"/queue", "/episodes/{id}"},
	"subtitles/library": {"/library", ""}, "subtitles/files": {"/library", "/files/{id}"},
	"monitoring/servers": {"/", "/server/{id}"}, "monitoring/alerts": {"/alerts", ""},
	"ai/threads": {"/", "/t/{id}"}, "ai/ask-context": {"/", "/t/{id}"},
	"projects/dashboard": {"/my-issues", ""}, "projects/issues": {"/my-issues", "/issue/{id}"}, "projects/projects": {"/projects", "/project/{id}"},
	"projects/teams": {"/teams", ""}, "projects/cycles": {"/projects", ""}, "projects/calendar": {"/calendar", ""}, "projects/search": {"/search", ""},
	"projects/notifications": {"/inbox", ""},
	"movement/summary":       {"/timeline", ""}, "movement/where": {"/timeline", ""}, "movement/now": {"/", ""}, "movement/day": {"/timeline", ""},
	"movement/insights": {"/insights", ""}, "movement/countries": {"/insights/countries", ""}, "movement/places": {"/places", "/places/{id}"},
	"movement/visits": {"/timeline", ""}, "movement/search": {"/places", "/places/{id}"},
}

var mcpLinkID = regexp.MustCompile(`^(?:[1-9][0-9]*|[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})$`)
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
	link := func(path string) string { return strings.TrimRight(base.String(), "/") + path }
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
		if route.item != "" && mcpLinkID.MatchString(id) {
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
