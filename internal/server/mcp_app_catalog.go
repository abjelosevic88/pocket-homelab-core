package server

import (
	"regexp"
	"strings"
)

// This is a reviewed capability catalog, not an HTTP proxy or an automatic
// export of app routes. No tool controls credentials, sharing, delivery,
// deletion, arbitrary HTTP methods, or service administration. One sharing
// exception: pocket_projects_share_note copies the person's own note onto a
// Projects issue for its team (approved per call; see mcpSharingAllowed).
// pocket_voice_send_memo (/memos/{id}/send) is not delivery to anyone else: it
// files a memo into the person's own Notes/Tasks/Calendar/Money, approved per call.
// pocket_booking_invite makes a booking link for one named person (returned, not sent),
// and pocket_booking_book/cancel/move/confirm may email that booking's guest: each approved per call.
var mcpAppCatalog = buildMCPAppCatalog()

type mcpFields map[string]any

func textFields(names string) mcpFields {
	out := mcpFields{}
	for _, name := range strings.Fields(names) {
		out[name] = map[string]any{"type": "string", "maxLength": 30000}
	}
	return out
}
func (f mcpFields) integers(names string) mcpFields {
	for _, name := range strings.Fields(names) {
		f[name] = map[string]any{"type": "integer"}
	}
	return f
}
func (f mcpFields) numbers(names string) mcpFields {
	for _, name := range strings.Fields(names) {
		f[name] = map[string]any{"type": "number"}
	}
	return f
}
func (f mcpFields) booleans(names string) mcpFields {
	for _, name := range strings.Fields(names) {
		f[name] = map[string]any{"type": "boolean"}
	}
	return f
}
func (f mcpFields) strings(names string) mcpFields {
	for _, name := range strings.Fields(names) {
		f[name] = arraySchema(map[string]any{"type": "string", "maxLength": 1000})
	}
	return f
}
func (f mcpFields) enum(name string, values ...string) mcpFields {
	f[name] = map[string]any{"type": "string", "enum": values}
	return f
}
func (f mcpFields) field(name string, schema any) mcpFields { f[name] = schema; return f }
func objectSchema(fields mcpFields, required string) map[string]any {
	s := map[string]any{"type": "object", "properties": fields, "additionalProperties": false}
	if required != "" {
		s["required"] = strings.Fields(required)
	}
	return s
}
func arraySchema(item any) map[string]any {
	return map[string]any{"type": "array", "items": item, "maxItems": 100}
}

// POSTs that act on or overwrite an existing item are changes, not additions;
// clients may auto-approve additions per app, so this list errs toward "change".
var mcpChangePosts = map[string]bool{"printfarm_weigh_spool": true, "tasks_complete": true, "photolab_apply": true, "family_take_run": true, "family_done_chore": true, "errors_update_issue": true, "inventory_adjust": true, "bookshelf_progress": true, "files_save_text": true, "files_upload_link": true,
	"office_rename": true, "projects_share_note": true, "projects_relate": true, "podcasts_favorite": true, "subtitles_nudge": true, "subtitles_pin": true, "home_complete_job": true, "home_plant_done": true, "home_water_plants": true, "pets_complete_care": true, "places_rate_noticed": true, "renewals_renew": true, "wealth_update_value": true,
	"parcels_add_step": true, "parcels_mark_delivered": true, "parcels_check": true, "wallet_spend_gift_card": true, "weather_add_place": true,
	"fitness_log_workout": true, "nutrition_log_food": true, "nutrition_add_water": true, "voice_tidy_memo": true, "voice_send_memo": true, "blog_publish": true, "blog_unpublish": true, "blog_reply_comment": true, "forms_open_form": true, "forms_close_form": true, // log_workout also goes to Pocket Health and ticks a habit, log_food and add_water reach Pocket Health too; send_memo creates items in the person's other apps: always ask
	"sign_remind": true, "sign_void": true, "cameras_review_events": true, // the first two email people who had a link; review_events marks many events at once: always ask
	"booking_book": true, "booking_confirm": true, "booking_cancel": true, "booking_move": true, "booking_invite": true, // these email a guest or make a link for one: always ask
	"trails_outing_status": true, // "I'm back" may email the person's safety contact: always ask
	"league_answer":        true, // the group sees it and may move someone off the waiting list: always ask
	"ai_create_schedule":   true} // a new schedule runs an agent unattended: always ask, like a change

// mcpOpKind is read, add (creates something new) or change (alters what exists).
func mcpOpKind(name, method string) string {
	switch {
	case method == "GET":
		return "read"
	case method == "POST" && !mcpChangePosts[name]:
		return "add"
	}
	return "change"
}

var mcpPathParams = regexp.MustCompile(`\{([a-z_]+)\}`)

// mcpCatalogBuilder collects the tools as they are declared. Each app's tools
// are declared by one of its methods in a mcp_catalog_<domain>.go file.
type mcpCatalogBuilder struct{ out []*mcpAppTool }

// buildMCPAppCatalog declares every tool. The order of the calls is the order
// the tools are listed in to an assistant, so it stays as it is; that is why
// some apps are declared in two parts.
func buildMCPAppCatalog() []*mcpAppTool {
	b := &mcpCatalogBuilder{out: []*mcpAppTool{}}
	b.tasksSummary()
	b.reviewSummary()
	b.garageSummary()
	b.moneyBills()
	b.notes()
	b.tasks()
	b.calendar()
	b.habits()
	b.day()
	b.inventory()
	b.bookshelf()
	b.clips()
	b.contacts()
	b.documents()
	b.files()
	b.garage()
	b.kitchen()
	b.home()
	b.homePlants()
	b.pets()
	b.places()
	b.wallet()
	b.weather()
	b.fitness()
	b.nutrition()
	b.voice()
	b.blog()
	b.forms()
	b.crm()
	b.league()
	b.tables()
	b.rentals()
	b.trails()
	b.mapApp()
	b.errors()
	b.wardrobe()
	b.jobs()
	b.photos()
	b.photolab()
	b.rooms()
	b.puzzlecraft()
	b.forum()
	b.capsule()
	b.vault()
	b.slides()
	b.math()
	b.karaoke()
	b.printfarm()
	b.gamebuilder()
	b.jobsLookupAndWrites()
	b.family()
	b.chat()
	b.renewals()
	b.wealth()
	b.parcels()
	b.sign()
	b.money()
	b.split()
	b.invoicing()
	b.review()
	b.travel()
	b.office()
	b.podcasts()
	b.subtitles()
	b.hub()
	b.monitoring()
	b.backups()
	b.projects()
	b.movement()
	b.search()
	b.prReview()
	b.ai()
	b.booking()
	b.cameras()
	b.analytics()
	b.camerasWrites()
	return b.out
}

func (b *mcpCatalogBuilder) add(app, name, description, method, path string, fields mcpFields, required, query string) *mcpAppTool {
	root := mcpFields{}
	req := []string{}
	for _, p := range mcpPathParams.FindAllStringSubmatch(path, -1) {
		root[p[1]] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)}
		if app == "photos" && p[1] == "id" {
			root[p[1]] = map[string]any{"type": "string", "pattern": `^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`, "description": "The photo's asset id."}
		}
		if app == "ai" && p[1] == "id" {
			root[p[1]] = map[string]any{"type": "string", "pattern": `^[a-f0-9-]{36}$`}
		}
		if app == "calendar" {
			root[p[1]] = map[string]any{"anyOf": []any{root[p[1]], map[string]any{"type": "string", "pattern": `^[1-9][0-9]{0,15}$`}}, "description": "Numeric event/series ID, not an occurrence ID."}
		}
		if app == "projects" && p[1] == "id" && strings.HasPrefix(path, "/issues/") {
			// the identifier names one issue whichever workspace; a bare number needs the workspace said
			root[p[1]] = map[string]any{"anyOf": []any{root[p[1]], map[string]any{"type": "string", "pattern": `^[A-Za-z][A-Za-z0-9]{0,6}-[1-9][0-9]{0,9}$`}},
				"description": "The issue's identifier like WEB-12 (preferred) or its numeric id."}
		}
		if p[1] == "team" {
			root[p[1]] = map[string]any{"type": "string", "pattern": `^([A-Za-z][A-Za-z0-9]{0,6}|[1-9][0-9]{0,15})$`, "description": "Team key, e.g. WEB (or the team's numeric id)."}
		}
		if p[1] == "date" {
			root[p[1]] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
		}
		req = append(req, p[1])
	}
	if query != "" {
		q := textFields(query)
		for _, key := range []string{"limit", "offset"} {
			if _, ok := q[key]; ok {
				q[key] = map[string]any{"type": "integer", "minimum": 0, "maximum": 10000}
				if key == "limit" {
					q[key] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
				}
			}
		}
		root["query"] = objectSchema(q, "")
	}
	if app == "projects" {
		// a Pocket Projects instance the person connected (Settings → Connected instances), reached through theirs
		root["workspace"] = map[string]any{"type": "string", "pattern": `^[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`,
			"description": "The address of the Pocket Projects instance the call is for, as pocket_projects_workspaces lists them (e.g. projects.example.com): your own or one you connected, as you there. Once workspaces are connected every change needs it, and only the workspace the person has open in Pocket Projects is reachable."}
	}
	if method != "GET" {
		root["request_id"] = map[string]any{"type": "string", "pattern": `^[A-Za-z0-9_-]{16,80}$`, "description": "A unique operation ID (for example a UUID). Reuse only for an identical retry."}
		req = append(req, "request_id")
		if fields == nil {
			fields = mcpFields{}
		}
		root["data"] = objectSchema(fields, required)
		req = append(req, "data")
		if len(fields) > 0 {
			root["data"].(map[string]any)["minProperties"] = 1
		}
	}
	op := &mcpAppTool{App: app, Name: "pocket_" + app + "_" + name, Description: description, Method: method, Path: path, Kind: mcpOpKind(app+"_"+name, method), Schema: objectSchema(root, strings.Join(req, " "))}
	b.out = append(b.out, compileMCPAppTool(op))
	return op
}

func (b *mcpCatalogBuilder) read(app, name, description, path, query string) *mcpAppTool {
	return b.add(app, name, description, "GET", path, nil, "", query)
}

func (b *mcpCatalogBuilder) write(app, name, description, method, path string, fields mcpFields, required string) *mcpAppTool {
	return b.add(app, name, description, method, path, fields, required, "")
}
