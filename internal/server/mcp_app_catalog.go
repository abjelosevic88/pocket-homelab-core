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
var mcpChangePosts = map[string]bool{"tasks_complete": true, "inventory_adjust": true, "bookshelf_progress": true, "files_save_text": true, "files_upload_link": true,
	"office_rename": true, "projects_share_note": true, "podcasts_favorite": true, "subtitles_nudge": true, "subtitles_pin": true, "home_complete_job": true, "home_plant_done": true, "home_water_plants": true, "pets_complete_care": true, "places_rate_noticed": true, "renewals_renew": true, "wealth_update_value": true,
	"parcels_add_step": true, "parcels_mark_delivered": true, "parcels_check": true, "wallet_spend_gift_card": true, "weather_add_place": true,
	"fitness_log_workout": true, "nutrition_log_food": true, "nutrition_add_water": true, "voice_tidy_memo": true, "voice_send_memo": true, "blog_publish": true, "blog_unpublish": true, "blog_reply_comment": true, "forms_open_form": true, "forms_close_form": true, // log_workout also goes to Pocket Health and ticks a habit, log_food and add_water reach Pocket Health too; send_memo creates items in the person's other apps: always ask
	"sign_remind": true, "sign_void": true, // these email people who had a link: always ask
	"league_answer":      true, // the group sees it and may move someone off the waiting list: always ask
	"ai_create_schedule": true} // a new schedule runs an agent unattended: always ask, like a change

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

func buildMCPAppCatalog() []*mcpAppTool {
	out := []*mcpAppTool{}
	add := func(app, name, description, method, path string, fields mcpFields, required, query string) *mcpAppTool {
		root := mcpFields{}
		req := []string{}
		for _, p := range mcpPathParams.FindAllStringSubmatch(path, -1) {
			root[p[1]] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)}
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
		out = append(out, compileMCPAppTool(op))
		return op
	}
	read := func(app, name, description, path, query string) *mcpAppTool {
		return add(app, name, description, "GET", path, nil, "", query)
	}
	write := func(app, name, description, method, path string, fields mcpFields, required string) *mcpAppTool {
		return add(app, name, description, method, path, fields, required, "")
	}

	read("tasks", "activity", "Read recent task activity (newest first), including task IDs and created/updated events. Useful for detecting newly created tasks.", "/activity", "task_id limit")
	read("tasks", "summary", "Get task counts, overdue/due work and next tasks in one call.", "/dashboard", "")
	read("review", "summary", "Get review dashboard counts and pending review work in one call.", "/dashboard", "")
	read("garage", "summary", "Get a vehicle overview, recorded costs, fuel and active maintenance reminders in one call. Costs can overlap Money: never add app totals without verifying shared transaction IDs. Similar dates/amounts are only possible duplicates.", "/vehicles/{id}/overview", "")
	read("garage", "reports", "Get a vehicle's cost totals by year, month and category. State the range and currency, and separate maintenance costs from fuel. Avoid double counting linked Money transactions.", "/vehicles/{id}/reports", "months")
	read("garage", "reminders", "Get maintenance reminders across accessible vehicles.", "/reminders", "")
	read("money", "bills", "Get your subscriptions and recurring bills; due dates are estimates until confirmed by a transaction.", "/subscriptions", "")
	read("notes", "list", "List your notes.", "/notes", "category tag sort limit since")
	read("notes", "search", "Search note titles and content.", "/search", "q limit")
	read("notes", "get", "Read a note's content and current etag.", "/notes/{id}", "")
	read("notes", "folders", "List existing note folder paths. Use the exact folder name as category when creating or updating a note.", "/folders", "")
	note := textFields("title content category etag").booleans("favorite pinned")
	note["category"].(map[string]any)["description"] = "Folder path from pocket_notes_folders, e.g. Quick Notes. This is a name/path, not an ID."
	write("notes", "create", "Create a personal note.", "POST", "/notes", note, "title content")
	write("notes", "update", "Update only the supplied note fields; use its current etag to detect conflicting edits.", "PUT", "/notes/{id}", note, "etag")
	write("notes", "append", "Append text to a note without replacing its existing content.", "POST", "/notes/{id}/append", textFields("text"), "text")

	read("tasks", "lists", "List the task lists you can use.", "/lists", "")
	read("tasks", "list", "Find tasks, including due dates and completion status.", "/tasks", "q list_id status due_from due_to start_from start_to tag view limit offset")
	read("tasks", "get", "Read one task.", "/tasks/{id}", "")
	task := textFields("title notes start due location").integers("list_id priority estimate_min").strings("tags").booleans("someday")
	write("tasks", "create", "Create a task; dates use the application's local date/time format.", "POST", "/tasks", task, "title")
	write("tasks", "update", "Update supplied task fields, preserving omitted fields.", "PUT", "/tasks/{id}", task, "")
	write("tasks", "complete", "Mark a task done or reopen it. Completing a recurring task may create its next occurrence.", "POST", "/tasks/{id}/complete", mcpFields{}.booleans("done"), "done")

	read("calendar", "calendars", "List calendars and your access to them.", "/calendars", "")
	read("calendar", "events", "List events in a date range.", "/events", "from to calendars tz")
	read("calendar", "search", "Search calendar events by text.", "/search", "q tz")
	read("calendar", "get_event", "Read an event.", "/events/{id}", "")
	event := textFields("title description location start end tz rrule").integers("calendar_id").booleans("all_day busy").strings("categories")
	write("calendar", "create_event", "Create a calendar event without adding invitees. Specify timezone and start/end.", "POST", "/events", event, "calendar_id title start end")
	write("calendar", "update_event", "Update the supplied event fields. For a recurring event this edits the series; occurrence-specific edits are not exposed.", "PUT", "/events/{id}", event, "")

	read("habits", "list", "List your habits and their IDs.", "/habits", "")
	read("habits", "today", "Read today's habits, trackers and journal.", "/today", "")
	read("habits", "day", "Read a day's habit check-ins.", "/days/{date}", "")
	write("habits", "mark", "Record a habit as done (1) or missed (-1) for a date.", "PUT", "/habits/{id}/entries/{date}", mcpFields{"status": map[string]any{"type": "integer", "enum": []int{1, -1}}}, "status")
	// Check-in creates a journal entry or updates existing text; it does not clear check-ins.
	write("habits", "journal", "Create or update journal text for a day.", "POST", "/checkin", textFields("date journal journal_bad important"), "date journal")

	read("day", "entries", "Search your journal entries.", "/entries", "q from to tag limit offset")
	read("day", "get", "Read a day and its journal.", "/days/{date}", "")
	read("day", "moments", "List your journal moments.", "/moments", "from to limit")
	write("day", "append", "Append a journal entry, optionally setting its highlight and mood (1–5).", "POST", "/entries", textFields("date highlight text tags").integers("mood"), "text")
	write("day", "update", "Update supplied journal fields for a day.", "PUT", "/days/{date}", textFields("highlight text tags").integers("mood").booleans("pinned"), "")

	read("inventory", "list", "Search inventory items, quantities and locations.", "/items", "q location category tag limit")
	read("inventory", "get", "Read an inventory item.", "/items/{id}", "")
	read("inventory", "locations", "List inventory locations.", "/locations", "")
	inventory := textFields("name notes location category unit barcode brand model serial purchase_date expiry_date tags").numbers("quantity min_quantity price").integers("location_id category_id")
	write("inventory", "create", "Add an inventory item.", "POST", "/items", inventory, "name")
	write("inventory", "update", "Update supplied inventory item fields.", "PUT", "/items/{id}", inventory, "")
	write("inventory", "adjust", "Adjust quantity by delta; negative delta records consumption.", "POST", "/items/{id}/adjust", textFields("note").numbers("delta"), "delta")

	read("bookshelf", "list", "Search books on your shelf.", "/books", "q status tag limit")
	read("bookshelf", "get", "Read a book, review and reading progress.", "/books/{id}", "")
	read("bookshelf", "reading", "Read what you are reading now: progress, pages read today, pace, days left and when each book will be finished.", "/reading/today", "")
	book := textFields("title authors isbn13 isbn10 description tags review").integers("pages current_page").numbers("rating").enum("status", "want", "reading", "read", "abandoned")
	write("bookshelf", "create", "Add a book to your shelf.", "POST", "/books", book, "title")
	write("bookshelf", "update", "Update supplied book details, rating or review.", "PUT", "/books/{id}", book, "")
	write("bookshelf", "progress", "Record your current page. This may change want-to-read to reading.", "POST", "/books/{id}/progress", mcpFields{}.integers("page"), "page")

	read("clips", "list", "Search your clipboard history.", "/clips", "q kind limit offset")
	read("clips", "get", "Read a saved clip.", "/clips/{id}", "")
	read("clips", "snippets", "List your reusable snippets.", "/snippets", "q folder")
	write("clips", "create", "Save a text clip.", "POST", "/clips", textFields("title content").enum("kind", "text"), "content")
	write("clips", "update", "Rename or pin a clip.", "PUT", "/clips/{id}", textFields("title").booleans("pinned"), "")

	read("contacts", "addressbooks", "List your address books.", "/addressbooks", "")
	read("contacts", "list", "Search contacts you can read.", "/contacts", "q category limit offset")
	read("contacts", "get", "Read a contact card.", "/contacts/{id}", "")
	contactField := objectSchema(textFields("label value"), "value")
	card := textFields("fn given family nickname org title birthday note").field("emails", arraySchema(contactField)).field("phones", arraySchema(contactField)).strings("categories")
	write("contacts", "create", "Create a contact in an accessible address book.", "POST", "/contacts", mcpFields{}.integers("addressbook_id").field("card", objectSchema(card, "fn")).booleans("starred"), "addressbook_id card")
	write("contacts", "label", "Star a contact or add categories without replacing its card.", "PATCH", "/contacts/{id}", mcpFields{}.booleans("starred").strings("add_categories"), "")

	read("documents", "list", "Search document metadata and OCR matches.", "/documents", "q created_from created_to correspondent document_type sort limit offset")
	read("documents", "get", "Read document metadata.", "/documents/{id}", "")
	read("documents", "text", "Read extracted document text; large documents may exceed the response limit.", "/documents/{id}/text", "").TextResponse = true
	read("documents", "notes", "Read annotations on a document.", "/documents/{id}/notes", "")
	write("documents", "annotate", "Add an annotation to a document.", "POST", "/documents/{id}/notes", textFields("text"), "text")
	write("documents", "update", "Update the document title or creation date, preserving other fields.", "PATCH", "/documents/{id}", textFields("title created"), "")

	read("files", "spaces", "List file spaces available to you.", "/spaces", "")
	read("files", "list", "List files in an accessible space and relative path.", "/fs/list", "space path")
	read("files", "stat", "Read file metadata.", "/fs/stat", "space path")
	read("files", "search", "Search your accessible files.", "/fs/search", "space path q")
	read("files", "read_text", "Read a UTF-8 text file (up to 512 KiB); binary files are not supported.", "/fs/raw", "space path").TextResponse = true
	write("files", "mkdir", "Create a folder within an accessible file space.", "POST", "/fs/mkdir", textFields("space path"), "space path")
	write("files", "save_text", "Create or replace a text file's complete contents. Read and preserve existing contents when needed.", "POST", "/fs/text", textFields("space path content"), "space path content")
	// The bytes never pass through the tool call: an app build, a photo or a video can't fit in JSON. Files hands out a link that
	// writes only this path, once, within an hour, as the person; the agent then PUTs the file there. A change (always asked):
	// what gets written is only known when it arrives.
	write("files", "upload_link", "Get a one-time link to upload one file of any type or size (an app build, a photo, a PDF, a video) to path in an accessible space, then send the file's bytes to the returned url with a PUT, e.g. curl -T <file> '<url>'. The link writes only that path, works once and expires after an hour. overwrite = true replaces a file already there; otherwise the new file is kept beside it under a new name. Create the folder first with pocket_files_mkdir.", "POST", "/fs/upload-link", textFields("space path").booleans("overwrite"), "space path")

	read("garage", "vehicles", "List your vehicles.", "/vehicles", "")
	read("garage", "get_vehicle", "Read a vehicle's details.", "/vehicles/{id}/", "")
	read("garage", "fuel", "Read fuel entries and their linked Money transaction IDs. Amounts are minor units; distinguish confirmed links from possible duplicate amounts.", "/vehicles/{vehicle_id}/fuel", "q")
	read("garage", "records", "Read maintenance records for a vehicle.", "/vehicles/{vehicle_id}/records", "q kind from to limit")
	read("garage", "notes", "Read a vehicle's notes.", "/vehicles/{vehicle_id}/notes", "")
	vehicle := textFields("name make model plate vin notes").integers("year").numbers("initial_odometer")
	write("garage", "create_vehicle", "Create a vehicle record.", "POST", "/vehicles", vehicle, "name")
	write("garage", "update_vehicle", "Update supplied vehicle details.", "PUT", "/vehicles/{id}/", textFields("name make model plate vin notes").integers("year"), "")
	write("garage", "add_note", "Add a maintenance or ownership note to a vehicle.", "POST", "/vehicles/{vehicle_id}/notes", textFields("title body").booleans("pinned"), "body")

	read("kitchen", "recipes", "Search your recipe collection.", "/recipes", "q course cuisine tag limit")
	read("kitchen", "get_recipe", "Read a recipe and its ingredients.", "/recipes/{id}", "")
	read("kitchen", "lists", "List shopping lists.", "/lists", "")
	read("kitchen", "get_list", "Read a shopping list and its items.", "/lists/{id}", "")
	read("kitchen", "pantry", "Read pantry stock.", "/pantry", "q location category low expiring")
	ingredient := objectSchema(textFields("name unit note").numbers("qty"), "name")
	recipe := textFields("title description course cuisine tags notes").integers("servings prep_minutes cook_minutes").field("ingredients", arraySchema(ingredient)).strings("steps")
	write("kitchen", "create_recipe", "Create a recipe with ingredients and preparation steps.", "POST", "/recipes", recipe, "title")
	write("kitchen", "update_recipe", "Update supplied recipe fields. Supplied ingredient/step arrays replace those arrays.", "PUT", "/recipes/{id}", recipe, "")
	write("kitchen", "add_shopping_item", "Add an item to a shopping list.", "POST", "/lists/{list_id}/items", textFields("name unit note aisle").numbers("qty"), "name")
	write("kitchen", "update_shopping_item", "Update or check off a shopping item.", "PUT", "/lists/{list_id}/items/{id}", textFields("name unit note").numbers("qty").booleans("checked"), "")

	// Pocket Home: homes, appliances, upkeep (jobs), the log (records), meters and pros. Costs are minor units of the home's currency.
	read("home", "summary", "Get the home overview in one call: counts of overdue, due and soon upkeep, what needs doing, the next two months, recent log entries, warranties ending and meters needing a reading, and spending this year vs last year to date. Log costs can overlap Money: confirmed bookings carry money_tx_id; never add Home and Money totals without verifying it.", "/overview", "home_id")
	read("home", "due", "Get upkeep that is overdue, due today, due soon and coming in the next days (default 30) across your homes, each with status and days_left.", "/due", "days home_id")
	read("home", "homes", "List your homes with their appliance counts and open, overdue and soon upkeep.", "/homes", "")
	read("home", "things", "List appliances and parts of the house (boiler, roof, washing machine) with room, model, warranty and next upkeep. Filter by home, category, room or text.", "/things", "home_id category room q")
	read("home", "get_thing", "Read one appliance or part: details, warranty state, its upkeep, log and attachments.", "/things/{id}", "")
	read("home", "jobs", "List upkeep (recurring or one-off maintenance) with status (overdue, due, soon, ok, paused, done), due_on and how often it repeats.", "/jobs", "home_id thing_id q all")
	read("home", "get_job", "Read one piece of upkeep and the log entries written when it was done.", "/jobs/{id}", "")
	read("home", "records", "Read the log of repairs, maintenance, improvements, checks and purchases, newest first. cost is minor units of currency; money_tx_id is the confirmed Pocket Money booking.", "/records", "home_id thing_id pro_id kind from to q limit offset")
	read("home", "spending", "Get what the log cost per month, per kind, per appliance and per year. State the range and currency; avoid double counting linked Money transactions.", "/spending", "months home_id")
	read("home", "meters", "List electricity, water, gas and other meters with their last reading.", "/meters", "home_id")
	read("home", "get_meter", "Read a meter with every reading, use per calendar month (spread evenly between readings) and a yearly summary.", "/meters/{id}", "")
	read("home", "pros", "List the pros who work on the house (plumber, electrician) with their jobs and what they cost.", "/pros", "q")
	read("home", "search", "Search homes, appliances, upkeep, log entries, pros and meters.", "/search", "q limit")
	homeRecord := textFields("title date notes").integers("home_id thing_id job_id pro_id cost").booleans("money").enum("kind", "maintenance", "repair", "improvement", "inspection", "purchase", "other")
	homeRecord["cost"].(map[string]any)["description"] = "Minor units of the home's currency (8900 = 89.00)."
	homeRecord["money"].(map[string]any)["description"] = "Also create a Pocket Money transaction when the person has Money connected (default: their setting)."
	write("home", "log_work", "Add a log entry (a repair, service, improvement, check or purchase). Use thing_id for the appliance; the home follows from it.", "POST", "/records", homeRecord, "title")
	write("home", "complete_job", "Mark upkeep done: writes a log entry (log: false to skip) and moves recurring upkeep to its next date, counted from date. Send at least the date (YYYY-MM-DD).", "POST", "/jobs/{id}/done", textFields("date notes").integers("cost pro_id").booleans("log money"), "")
	write("home", "add_reading", "Add a meter reading. A value below the last reading needs reset (the meter was replaced).", "POST", "/meters/{id}/readings", textFields("read_on note").numbers("value").booleans("reset"), "value")
	homeJob := func() mcpFields {
		f := textFields("title notes due_on last_done").integers("home_id thing_id pro_id every_n soon_days est_cost").enum("every_unit", "day", "week", "month", "year")
		f["every_n"].(map[string]any)["description"] = "Repeat every N units; 0 = once."
		return f
	}
	write("home", "add_job", "Add upkeep: something to do every N days, weeks, months or years (or once, every_n 0), first due on due_on.", "POST", "/jobs", homeJob(), "title")
	write("home", "update_job", "Update supplied upkeep fields, or pause it.", "PUT", "/jobs/{id}", homeJob().booleans("paused"), "")
	homeThing := func() mcpFields {
		return textFields("name room brand model serial installed_on purchased_on warranty_until vendor manual_url notes").integers("home_id price").
			enum("category", "heating", "cooling", "water", "electrical", "appliance", "kitchen", "structure", "outdoor", "safety", "other")
	}
	write("home", "add_thing", "Add an appliance or part of the house to a home.", "POST", "/things", homeThing(), "name")
	write("home", "update_thing", "Update supplied appliance fields.", "PUT", "/things/{id}", homeThing().booleans("archived"), "")

	// Pocket Home's house plants: a plant's care is upkeep (jobs with plant_id and care), done care is a log entry of kind plant.
	read("home", "plants", "List house plants with their care (water, fertilise, mist, rotate, prune, pests, repot), what is due and when each was last watered. Gone plants only with archived=1.", "/plants", "home_id room q archived")
	read("home", "get_plant", "Read one house plant: details, its care schedule and status, and its care log.", "/plants/{id}", "")
	write("home", "plant_done", "Mark one care done for one plant (default care water: \"I watered the monstera\"). Moves that care to its next date and writes the log; care not on the schedule is only logged.", "POST", "/plants/{id}/done",
		textFields("date notes title").integers("cost").enum("care", "water", "fertilise", "mist", "rotate", "prune", "pests", "repot"), "")
	write("home", "water_plants", "Mark care done for several plants at once (default water). With no plant_ids it does every plant whose care is due today or overdue (\"I watered everything\").", "POST", "/plants/done",
		textFields("date").integers("home_id").field("plant_ids", arraySchema(map[string]any{"type": "integer"})).enum("care", "water", "fertilise", "mist", "rotate", "prune", "pests", "repot"), "")

	// Pocket Pets: pets, their care (vaccines, worming, flea and tick, nails, medicine), the health log, weights, vets and sitters. Costs are minor units of the pet's currency.
	read("pets", "summary", "Get the pets overview in one call: counts of overdue, due and soon care, what needs doing, the next two months, recent log entries, vaccines and insurance that run out, birthdays, coming trips with the care due while away, and spending this year vs last year to date. Log costs can overlap Money: confirmed bookings carry money_tx_id; never add Pets and Money totals without verifying it.", "/overview", "pet_id")
	read("pets", "due", "Get care that is overdue, due today, due soon and coming in the next days (default 30) across your pets, each with status and days_left.", "/due", "days pet_id")
	read("pets", "pets", "List your pets with species, breed, age, latest weight, vet, insurance end date and open, overdue and soon care.", "/pets", "")
	read("pets", "get_pet", "Read one pet: profile (microchip, passport, food, allergies, health, vet, insurance), its care, health log, weights, the latest vaccine of each name with how long it holds, and papers.", "/pets/{id}", "")
	read("pets", "care", "List care (vaccines, worming, flea and tick, check-ups, nails, medicine) with status (overdue, due, soon, ok, paused, done), due_on and how often it repeats.", "/care", "pet_id kind q all")
	read("pets", "get_care", "Read one piece of care and the log entries written when it was done.", "/care/{id}", "")
	read("pets", "records", "Read the health log (vet visits, vaccines with valid_until, treatments, medicine, grooming, symptoms, tests, surgery, food and supplies), newest first. cost is minor units; money_tx_id is the confirmed Pocket Money booking.", "/records", "pet_id pro_id kind from to q limit offset")
	read("pets", "weights", "Read weighings, newest first, each with the change in kg since the one before.", "/weights", "pet_id limit")
	read("pets", "spending", "Get what the pets cost per month, per kind, per pet and per year. State the range and currency; avoid double counting linked Money transactions.", "/spending", "months pet_id")
	read("pets", "pros", "List vets (with 24/7 emergency clinics), groomers, sitters, walkers and boarding with their visits and what they cost.", "/pros", "q")
	read("pets", "search", "Search pets, care, health log entries and vets/sitters.", "/search", "q limit")
	petRecord := textFields("title date notes valid_until").integers("pet_id care_id pro_id cost").booleans("money").enum("kind", "vet_visit", "vaccine", "treatment", "medication", "grooming", "symptom", "test", "surgery", "purchase", "other")
	petRecord["cost"].(map[string]any)["description"] = "Minor units of the pet's currency (4500 = 45.00)."
	petRecord["valid_until"].(map[string]any)["description"] = "For a vaccine or treatment: until when it holds (YYYY-MM-DD)."
	petRecord["money"].(map[string]any)["description"] = "Also create a Pocket Money transaction when the person has Money connected (default: their setting)."
	write("pets", "log_visit", "Add to a pet's health log: a vet visit, a vaccine (with valid_until), a treatment, medicine, grooming, a symptom, a test, surgery or a purchase.", "POST", "/records", petRecord, "title")
	write("pets", "complete_care", "Mark care done (\"Luna got her rabies shot\"): writes a log entry (log: false to skip) and moves recurring care to its next date, counted from date. A vaccine holds until the next due date unless valid_until says otherwise.", "POST", "/care/{id}/done",
		textFields("date notes valid_until").integers("cost pro_id").booleans("log money"), "")
	write("pets", "add_weight", "Add a weighing for a pet, in kilograms.", "POST", "/pets/{id}/weights", textFields("weighed_on note").numbers("kg"), "kg")
	petCare := func() mcpFields {
		f := textFields("title notes due_on last_done").integers("pet_id pro_id every_n soon_days est_cost").enum("every_unit", "day", "week", "month", "year").
			enum("kind", "vaccine", "worming", "flea_tick", "checkup", "dental", "grooming", "nails", "medication", "other")
		f["every_n"].(map[string]any)["description"] = "Repeat every N units; 0 = once."
		return f
	}
	write("pets", "add_care", "Add care for a pet: something to do every N days, weeks, months or years (or once, every_n 0), first due on due_on.", "POST", "/care", petCare(), "title")
	write("pets", "update_care", "Update supplied care fields, or pause it.", "PUT", "/care/{id}", petCare().booleans("paused"), "")
	petFields := func() mcpFields {
		return textFields("name breed born_on color microchip passport_no food allergies conditions insurer policy_no insured_until notes passed_on").integers("vet_id").booleans("neutered").
			enum("species", "dog", "cat", "rabbit", "rodent", "bird", "fish", "reptile", "horse", "other").enum("sex", "male", "female", "")
	}
	write("pets", "add_pet", "Add a pet.", "POST", "/pets", petFields(), "name")
	write("pets", "update_pet", "Update supplied pet fields (passed_on stops its reminders).", "PUT", "/pets/{id}", petFields().booleans("archived"), "")

	// Pocket Places: restaurants, cafés, bars, sights, parks… you love or want to try, your visits with ratings and what they cost, plans, lists, and visits Movement/Money noticed. Costs are minor units.
	read("places", "summary", "Get a short overview of your places: how many (want to try, been, favourites), how many visits wait for a rating, the next plans, visits and spending this month and the last visit.", "/summary", "")
	read("places", "overview", "Get the places overview: visits to rate (noticed in Pocket Movement and Pocket Money) and plans whose day passed, coming plans, recent visits, favourites, places waiting to be tried, coming trips with your places at the destination, visits and spending this year, top towns and lists. Visit costs can overlap Money: linked payments carry money_tx_id; never add Places and Money totals without verifying it.", "/overview", "")
	read("places", "places", "List or filter your places. status want|been; group eat|see|shop|stay|other; kind e.g. restaurant, cafe, bar, bakery, museum, park, viewpoint; sort name|rating|recent|added|visits|near; near=lat,lng with radius_km for places around a spot (each then has distance_m); q searches name, cuisine, town, tags and notes, accents ignored. Each place has rating, price (1-4), visits, last_visit, next_plan, spent and lists.", "/places", "status kind group city country tag q list_id favourite open sort near radius_km limit")
	read("places", "get_place", "Read one place: details (address, hours, website, notes, who recommended it), every visit with rating, who came and what it cost, plans, the lists it is on, your places close by, open \"how was it?\" and how often Pocket Movement saw you there.", "/places/{id}", "")
	read("places", "visits", "Read visits, newest first (planned=false for real ones, planned=true for plans), each with the place, rating, with_whom, minutes, spent and currency (minor units) and money_tx_id when linked to Pocket Money.", "/visits", "place_id from to planned trip_id q limit offset")
	read("places", "lists", "List your lists of places (date night, coffee, a trip) with how many places and how many visited.", "/lists", "")
	read("places", "get_list", "Read one list with its places and the note kept for each.", "/lists/{id}", "status")
	read("places", "to_rate", "Read what waits for an answer: stays (Pocket Movement) and payments (Pocket Money) at your places, one per place and day, and plans whose day passed (\"did you go?\").", "/inbox", "")
	read("places", "search", "Search places, visits and lists (accents ignored).", "/search", "q limit")
	read("places", "day", "Read one day: visits, plans and places added that day.", "/day/{date}", "")
	read("places", "spending", "Get what visits cost per currency and per month between two days. State the range and currency; avoid double counting linked Money payments.", "/spending", "from to")
	read("places", "trips", "List Pocket Travel trips (coming, and with back=N days past ones) with how many of your places are at each destination (want and been) and the lists made for it.", "/trips", "days back")
	placeFields := func() mcpFields {
		f := textFields("name cuisine address city country phone website map_url hours tags notes recommended_by currency").numbers("rating lat lng").integers("price").
			booleans("favourite closed").enum("status", "want", "been").
			enum("kind", "restaurant", "cafe", "bar", "bakery", "street_food", "dessert", "market", "shop", "museum", "sight", "park", "viewpoint", "beach", "nature", "activity", "hotel", "other")
		f["price"].(map[string]any)["description"] = "1 cheap to 4 expensive, 0 unknown."
		f["rating"].(map[string]any)["description"] = "1 to 5 (halves allowed), 0 none; rating a place marks it been."
		f["country"].(map[string]any)["description"] = "ISO 3166 two-letter code, e.g. BA, AT."
		return f
	}
	write("places", "add_place", "Save a place to your list (\"save Kibe Mahala, the restaurant with the view\"): want to go by default, or been with a rating.", "POST", "/places", placeFields().field("list_ids", arraySchema(map[string]any{"type": "integer"})), "name")
	write("places", "update_place", "Update supplied place fields (favourite, closed for good, a new address, notes).", "PUT", "/places/{id}", placeFields(), "")
	visitFields := func() mcpFields {
		f := textFields("date time notes with_whom currency").integers("place_id minutes spent").numbers("rating").booleans("planned money calendar")
		f["spent"].(map[string]any)["description"] = "What it cost, minor units (2400 = 24.00)."
		f["planned"].(map[string]any)["description"] = "A plan rather than a visit (default: true for a day in the future)."
		f["money"].(map[string]any)["description"] = "Also book the cost as a Pocket Money transaction (default: the person's setting; leave off when the card payment comes into Money anyway)."
		f["calendar"].(map[string]any)["description"] = "Put a plan on Pocket Calendar (default: the person's setting)."
		return f
	}
	write("places", "log_visit", "Log a visit to one of your places (\"we had dinner at Dveri, 5 stars, 64 KM\") or plan one (planned true, a day and a time: it goes on the calendar).", "POST", "/visits", visitFields(), "place_id")
	write("places", "update_visit", "Update a visit or a plan: rate it, add what it cost or who came, or turn a past plan into a visit (planned false).", "PUT", "/visits/{id}", visitFields(), "")
	write("places", "rate_noticed", "Answer a \"how was it?\" from to_rate: logs that visit with its time, how long and the payment linked, and the rating.", "POST", "/inbox/{id}/accept", textFields("notes with_whom").numbers("rating").integers("spent"), "")
	write("places", "add_list", "Make a new list of places, optionally with place_ids.", "POST", "/lists", textFields("name emoji description trip_name").integers("trip_id").field("place_ids", arraySchema(map[string]any{"type": "integer"})), "name")
	write("places", "add_to_list", "Put one of your places on one of your lists (id = the list).", "POST", "/lists/{id}/places", textFields("note").integers("place_id"), "place_id")

	// Pocket Voice: voice memos recorded on the phone, written out on the server (whisper.cpp) and tidied by Pocket AI (title, summary, cleaned text, kind and actions:
	// tasks, events, journal lines and expenses found in the memo), then sent to Pocket Notes, Tasks, Calendar or Money. Expense amounts here are major units (12.5 = 12.50).
	// Pocket Wallet: loyalty, membership and gift cards, tickets and boarding passes, ID papers (ID card, passport, driving licence, insurance). Pass times are local where they happen; amounts are minor units.
	read("wallet", "summary", "Get a short overview of the wallet: how many cards, passes and IDs, today's tickets and flights, the next pass, what runs out soon (IDs, memberships, gift cards, with days_left) and gift cards with money left.", "/summary", "")
	read("wallet", "overview", "Get the wallet home page: cards in the person's order, ID papers, the next passes, what runs out in 60 days, counts.", "/overview", "")
	read("wallet", "cards", "List or filter the wallet. group cards|passes|ids; kind loyalty|membership|gift|ticket|boarding|id|passport|licence|insurance|other; q searches name, shop, number, holder, place, booking reference; archived only|all; from/to filter passes by day; trip_id a Pocket Travel trip; expiring=YYYY-MM-DD runs out on or before; sort used|name|recent|starts|expires. Each card has number, holder, code_format/code_value (the barcode), seat, gate, ref (flight or booking), starts_at, venue, destination, expires_on, balance and points.", "/cards", "group kind q tag archived from to trip_id expiring sort limit")
	read("wallet", "get_card", "Read one card, pass or ID with everything on it.", "/cards/{id}", "")
	read("wallet", "passes", "List tickets and boarding passes: from now on by default, or between from and to, or a Pocket Travel trip's (trip_id).", "/passes", "from to trip_id limit")
	read("wallet", "expiring", "List what runs out within days (default the person's reminder days), with days_left; group ids for documents only.", "/expiring", "days group")
	read("wallet", "search", "Search the wallet by name, shop, number, holder, place or booking reference.", "/search", "q limit")
	read("wallet", "day", "Read one day's tickets and flights.", "/day/{date}", "")
	read("wallet", "balance", "Read a gift card's history: what was spent or added and what was left.", "/cards/{id}/balance", "")
	cardFields := func() mcpFields {
		f := textFields("name issuer number holder code_value color note tags valid_from expires_on starts_at ends_at venue destination seat gate ref currency source_ref").
			integers("balance points trip_id").booleans("pinned archived").
			enum("kind", "loyalty", "membership", "gift", "ticket", "boarding", "id", "passport", "licence", "insurance", "other").
			enum("code_format", "", "qr", "code128", "ean13", "aztec", "pdf417", "datamatrix", "ean8", "upca", "upce", "code39", "code93", "itf", "codabar")
		f["starts_at"].(map[string]any)["description"] = "Local time where it happens, YYYY-MM-DDTHH:MM (or YYYY-MM-DD)."
		f["code_value"].(map[string]any)["description"] = "What the barcode carries; defaults to the number when a code_format is given."
		f["balance"].(map[string]any)["description"] = "Gift cards: what is on it, minor units (5000 = 50.00)."
		f["venue"].(map[string]any)["description"] = "Where it is, or where a flight or train leaves from (\"SJJ Sarajevo\")."
		f["ref"].(map[string]any)["description"] = "Flight or train number, or booking reference."
		return f
	}
	write("wallet", "add_card", "Add a card, ticket, boarding pass or ID to the wallet (\"add my Lidl card 4012345\", \"my flight OS 784 on 1 Dec at 06:40, seat 14C\"). Passes go on the calendar and with their trip by the person's settings.", "POST", "/cards", cardFields(), "kind")
	write("wallet", "update_card", "Update supplied fields of a card this assistant added (a new gate, seat or end date).", "PUT", "/cards/{id}", cardFields(), "")
	write("wallet", "spend_gift_card", "Record spending from a gift card (delta negative, minor units: -1250 = 12.50 spent) or money added (positive).", "POST", "/cards/{id}/balance", textFields("note").integers("delta"), "delta")
	// Pocket Weather: the weather of the person's places and any spot (Open-Meteo): now, 48 hours, 10 days, air quality and pollen, warnings, and a diary of past days.
	// Units are metric: °C, mm of rain, cm of snow, km/h of wind; convert when the person uses °F, inches or mph. Times are local to the place.
	read("weather", "summary", "Get the weather at the person's home place: now (temp and feels in °C, humidity %, wind and gust km/h, uv, condition), today and tomorrow (t_min/t_max °C, precip mm, snow cm, precip_prob %, sunrise, sunset), active warnings, a one-line text (\"Cloudy, 3 °C to 9 °C; rain from 15:00.\") and air quality (aqi, band, pollen by day). place is null until the person adds one in Pocket Weather. location is a place id from pocket_weather_places for another place.", "/summary", "location")
	read("weather", "forecast", "Get the full forecast of one place (location = a place id from pocket_weather_places; home by default) or of any spot (lat and lon in degrees): now, hours ahead (48 by default; hours up to 240) with temp °C, precip_prob %, precip mm, snow cm, wind km/h, and days from yesterday to 10 days ahead (t_min/t_max °C, precip, rain mm, snow cm, wind, gust km/h, uv, sunshine_h, sunrise, sunset), plus the place's warnings and air quality.", "/forecast", "location lat lon hours")
	read("weather", "warnings", "Get the person's active weather warnings across their places: kind frost|ice|heat|wind|rain|snow|storm|uv|pollen|air, level advisory|warning, day, starts_at, ends_at, title, detail, value and place. history=1 adds the last 30 days.", "/warnings", "history")
	read("weather", "day", "Get what the weather was or will be on one date at a place (location = a place id; home by default) or any spot (lat and lon): the day's condition, t_min/t_max °C, precip mm, snow cm, wind km/h, sunshine and a one-line text, with source diary (saved), forecast or archive. Past dates come from the diary or the weather archive; dates more than 10 days ahead are not known yet.", "/day/{date}", "location lat lon")
	read("weather", "places", "List the places the person follows, with id (for location in the other weather tools), name, region, country, lat, lon, timezone, is_home and whether warnings are on.", "/locations", "")
	read("weather", "diary", "Get a month of the weather diary at a place (location = a place id; home by default; month YYYY-MM, this month by default): each day's code, t_min/t_max °C, precip mm, snow cm, wind and gust km/h, uv, sunshine_h, aqi, pollen and note, with the month's stats and the same month last year.", "/diary", "location month")
	weatherPlace := textFields("name country timezone source_ref").numbers("lat lon")
	weatherPlace["lat"].(map[string]any)["description"] = "Latitude in degrees, -90 to 90."
	weatherPlace["lon"].(map[string]any)["description"] = "Longitude in degrees, -180 to 180."
	weatherPlace["timezone"].(map[string]any)["description"] = "IANA timezone like Europe/Vienna; found from the spot when left out."
	weatherPlace["source_ref"].(map[string]any)["description"] = "Optional: your own reference for this place; adding the same one again updates that place instead of adding another."
	// a change, not an addition (mcpChangePosts): the same source_ref again updates the place it added
	write("weather", "add_place", "Add a place whose weather the person follows (a trip's city, a parent's town). Give its name and coordinates; it then shows in Pocket Weather with its forecast and warnings.", "POST", "/locations", weatherPlace, "name lat lon")
	// Pocket Fitness: the person's training log (gym workouts with exercises and sets, routines, a weekly plan, personal records); Pocket Health's runs and rides count toward the week.
	// Units are metric: weight in kg, distance_m in metres, duration_s in seconds; convert when the person uses lb or miles. started_at is UTC, date is the local day.
	read("fitness", "summary", "Get a short overview of training: week_done of week_goal workouts this week (from week_start, Pocket Health's runs count), streak_weeks, today's planned session (routine, time, exercises, minutes, done; null on a rest day), a workout in progress, last_workout (name, date, duration_s, set_count, volume kg, records) and the latest records, with a one-line text.", "/summary", "")
	read("fitness", "week", "Get one week (the week date YYYY-MM-DD is in; this week by default): each day's workouts (Pocket Health's runs and rides marked health true, with distance_m) and what was planned, upcoming days, done against goal.", "/week", "date")
	read("fitness", "day", "Get one date's training: workouts with each exercise's sets as text (\"80 kg × 8, 80 kg × 8\"), duration_min, sets, volume_kg and records; other (Pocket Health's runs and rides); planned (the routine planned that weekday); and a one-line text.", "/day/{date}", "")
	read("fitness", "workouts", "List finished workouts, newest first: from and to YYYY-MM-DD; routine = a routine id; exercise = an exercise id (only workouts with it); sets=1 adds every set (exercise_id, type normal|warmup|drop|failure, weight kg, reps, duration_s, distance_m, rpe). Each has name, date, duration_s, exercises (count), set_count, volume (kg lifted), reps, distance_m and records (how many new). Exercise names: pocket_fitness_get_workout or pocket_fitness_exercises.", "/workouts", "from to routine exercise sets limit offset")
	read("fitness", "get_workout", "Read one workout with every set (weight kg, reps, duration_s, distance_m, type, rpe, pr = the records a set set), exercises (by id, with name, muscle, kind), previous (each exercise's sets the time before) and the routine it came from.", "/workouts/{id}", "")
	read("fitness", "records", "Get personal records: records = the best per exercise and kind (weight = heaviest kg, e1rm = estimated one-rep max kg, volume = most kg × reps in a set, reps, duration, distance) with the set, workout_id and date; recent = the latest sets that set one, with a text. exercise = an exercise id for one exercise.", "/records", "exercise")
	read("fitness", "progress", "Get progress over weeks (weeks, 12 by default): per week workouts, Pocket Health workouts, volume kg, sets and minutes; sets per muscle in the last 30 days; body weight from Pocket Health; recent records; the streak, the weekly goal and totals (workouts, hours, volume since the first).", "/progress", "weeks")
	read("fitness", "routines", "List the person's routines (Push day, Leg day…) with id, notes, outdoors, the weekdays they are planned on (1 = Monday), how often done and last_done, and their exercises with the aim: sets, reps_min–reps_max, weight kg, duration_s, distance_m, rest_s.", "/routines", "")
	read("fitness", "plan", "Get the weekly plan: [{weekday (1 = Monday … 7 = Sunday), routine_id, routine, time}]; days left out are rest days.", "/plan", "")
	read("fitness", "exercises", "List the exercises (built-in and the person's own): id, name, muscle, secondary muscles, equipment, kind (weight = kg × reps, reps = bodyweight reps, time = duration_s, cardio = distance_m + duration_s). archived=1 includes hidden ones.", "/exercises", "archived")
	read("fitness", "get_exercise", "Read one exercise with its records, its history (each workout's sets with it, newest first) and chart points per day (best kg, e1rm, volume).", "/exercises/{id}", "")
	fitnessSet := mcpFields{}.numbers("weight distance_m rpe").integers("reps duration_s").enum("type", "normal", "warmup", "drop", "failure").field("note", map[string]any{"type": "string", "maxLength": 500})
	fitnessSet["weight"].(map[string]any)["description"] = "Kilograms (convert lb: 1 lb = 0.4536 kg); for a bodyweight exercise the extra weight, if any."
	fitnessSet["distance_m"].(map[string]any)["description"] = "Metres (5 km = 5000)."
	fitnessSet["duration_s"].(map[string]any)["description"] = "Seconds (a 60 s plank = 60; 26 min = 1560)."
	fitnessSet["type"].(map[string]any)["description"] = "normal (default), warmup (never counts toward totals or records), drop or failure."
	fitnessSet["rpe"].(map[string]any)["description"] = "How hard, 1 to 10 (optional)."
	fitnessExercise := mcpFields{}.integers("exercise_id").field("name", map[string]any{"type": "string", "maxLength": 200}).enum("kind", "weight", "reps", "time", "cardio").
		field("sets", map[string]any{"type": "array", "items": objectSchema(fitnessSet, ""), "minItems": 1, "maxItems": 100})
	fitnessExercise["name"].(map[string]any)["description"] = "The exercise's name (\"bench\" finds Bench Press (Barbell)); matched to the person's exercises or the built-in list, an unknown name is added as their own. Or give exercise_id."
	fitnessExercise["kind"].(map[string]any)["description"] = "Only for a name that is new: weight, reps (bodyweight), time or cardio."
	fitnessLog := textFields("name date started_at notes source_ref").numbers("duration_min").integers("routine_id").
		field("feel", map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "How it felt, 1 to 5 (optional)."}).
		field("exercises", map[string]any{"type": "array", "items": objectSchema(fitnessExercise, "sets"), "minItems": 1, "maxItems": 50})
	fitnessLog["date"].(map[string]any)["description"] = "The local day YYYY-MM-DD (today by default)."
	fitnessLog["started_at"].(map[string]any)["description"] = "Local start time YYYY-MM-DDTHH:MM (optional)."
	fitnessLog["duration_min"].(map[string]any)["description"] = "How long it took in minutes (optional)."
	fitnessLog["routine_id"].(map[string]any)["description"] = "The routine it followed, an id from pocket_fitness_routines (optional)."
	fitnessLog["source_ref"].(map[string]any)["description"] = "Optional: your own reference; logging the same one again updates that workout instead of adding another."
	// a change, not an addition (mcpChangePosts): it also goes to Pocket Health and ticks a Pocket Habits habit, and the same source_ref again updates
	write("fitness", "log_workout", "Log a finished workout (\"bench 3x8 at 80, then pull-ups 10, 8\"). One entry in exercises per exercise, one entry in sets per set: weight is ALWAYS kg, so \"3x8 at 80\" is three sets {\"weight\": 80, \"reps\": 8}; bodyweight reps are {\"reps\": 10}; a plank {\"duration_s\": 60}; a 5 km run in 26 min {\"distance_m\": 5000, \"duration_s\": 1560}. Records are worked out, Only workouts that happened (not in the future). The workout goes to Pocket Health, ticks the habit and counts toward the week; the answer has the workout, new records (with text) and the week (done of goal).", "POST", "/workouts/log", fitnessLog, "exercises")
	// Pocket Nutrition: the person's food diary (what they ate each day, by meal, with calories and macros), water, goals, their foods and saved meals.
	// Units: energy in kcal; protein, carbs, fat, fiber, sugar and sat_fat in grams; sodium in milligrams; water in millilitres. date is the local day YYYY-MM-DD; meal is breakfast, lunch, dinner or snacks.
	read("nutrition", "summary", "Get today's food in short: totals (kcal; protein, carbs, fat, fiber, sugar, sat_fat in g; sodium in mg; the last four null when unknown), entries (how many foods logged), water_ml, goals (kcal, protein g, carbs g, fat g, fiber g, water ml, burned = kcal burned by exercise per Pocket Health, budget = the kcal goal plus burned when the person counts exercise), remaining (kcal, g and ml left to the goals; negative = over), this week (days logged, on goal, average kcal), streak (days in a row logged) and a one-line text.", "/summary", "")
	read("nutrition", "day", "Get one date's food diary: meals [{meal, label, totals, entries (each food with name, qty × serving = amount in its unit g|ml|serving, kcal, protein g, carbs g, fat g, sodium mg)}], the day's totals, entries (count), water_ml, goals, remaining (left to the goals; negative = over), the day's note and a text (\"1,850 kcal of 2,300; protein 112 g, carbs 190 g, fat 64 g. Breakfast: oats, banana…\").", "/day/{date}", "")
	read("nutrition", "week", "Get the week a date (YYYY-MM-DD, today by default) is in: days [{date, entries, kcal, protein g, carbs g, fat g, …, water_ml}], logged (days with food logged), on_goal (days within 10% under to 5% over the kcal goal with the protein reached), avg_kcal, avg_protein, avg_carbs, avg_fat (g, over logged days) and goal (kcal).", "/week", "date")
	read("nutrition", "trends", "Get the last days (days, 30 by default, up to 366) day by day (kcal, macros g, water_ml), with average (kcal, protein/carbs/fat/fiber/sugar g, sodium mg, water ml over logged days), split (% of energy from protein, carbs and fat), top (the foods that brought the most kcal, with times and share %), by_meal (average kcal per meal), weight (kg, from Pocket Health), on_goal, goals and streak.", "/trends", "days")
	read("nutrition", "entries", "List the diary's entries in order (day, meal, position); the last 7 days by default. date = one day, or from and to YYYY-MM-DD; meal breakfast|lunch|dinner|snacks. Each entry: id, date, meal, name, food_id, qty × serving = amount in unit (g|ml|serving), kcal, protein g, carbs g, fat g, fiber g, sugar g, sat_fat g, sodium mg (null when unknown), note and source (the app that logged it).", "/entries", "date from to meal limit")
	read("nutrition", "foods_search", "Find foods to log by name: the person's own foods first (most logged, favourites), then the built-in list (id 0, builtin = its key). Without q, the foods the person logs most at that meal. Each food has its numbers per 100 g (unit g), per 100 ml (unit ml) or per serving (unit serving): kcal, protein/carbs/fat g, sodium mg, and servings [{label (\"1 egg\"), amount in the unit}].", "/foods/search", "q meal")
	read("nutrition", "meals", "List the person's saved meals (foods often eaten together, like \"usual breakfast\"): id, name, meal, notes, items (food, qty, serving, amount and numbers: kcal, macros g) and totals.", "/meals", "")
	nutritionNumbers := []string{"kcal", "protein", "carbs", "fat", "fiber", "sugar"}
	nutritionItem := mcpFields{}.field("name", map[string]any{"type": "string", "maxLength": 200}).integers("food_id").field("builtin", map[string]any{"type": "string", "maxLength": 100}).
		field("qty", map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1000}).field("serving", map[string]any{"type": "string", "maxLength": 100}).
		field("amount", map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 100000}).enum("unit", "g", "ml", "serving").
		enum("meal", "breakfast", "lunch", "dinner", "snacks").field("note", map[string]any{"type": "string", "maxLength": 500}).booleans("save")
	for _, n := range nutritionNumbers {
		nutritionItem[n] = map[string]any{"type": "number", "minimum": 0, "maximum": 20000}
	}
	nutritionItem["name"].(map[string]any)["description"] = "The food, one per item (\"egg\", \"white bread\", \"oats\"); matched to the person's foods, then the built-in list. Or give food_id / builtin from pocket_nutrition_foods_search."
	nutritionItem["qty"].(map[string]any)["description"] = "How many servings: two eggs = {\"name\": \"egg\", \"qty\": 2}; the food's first serving when none is named."
	nutritionItem["serving"].(map[string]any)["description"] = "Which serving qty counts, a label of the food's servings (\"1 slice\", \"1 cup\"); optional."
	nutritionItem["amount"].(map[string]any)["description"] = "An amount in grams or millilitres instead of qty, with unit: 60 g of oats = {\"name\": \"oats\", \"amount\": 60, \"unit\": \"g\"}."
	nutritionItem["unit"].(map[string]any)["description"] = "The unit of amount: g, ml or serving."
	nutritionItem["meal"].(map[string]any)["description"] = "Only when this item is in another meal than the rest."
	nutritionItem["kcal"].(map[string]any)["description"] = "Only for a food that is not known (an error says so): the kcal of the whole item as eaten. Then protein, carbs, fat, fiber and sugar in grams, for the whole item, if known."
	nutritionItem["save"].(map[string]any)["description"] = "With kcal: also keep it as one of the person's foods for next time."
	nutritionLog := mcpFields{}.field("date", map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "The local day YYYY-MM-DD it was eaten (today by default; not in the future)."}).
		enum("meal", "breakfast", "lunch", "dinner", "snacks").field("ref", map[string]any{"type": "string", "maxLength": 200}).
		field("items", map[string]any{"type": "array", "items": objectSchema(nutritionItem, ""), "minItems": 1, "maxItems": 40})
	nutritionLog["meal"].(map[string]any)["description"] = "breakfast, lunch, dinner or snacks (by the time of day when left out)."
	nutritionLog["ref"].(map[string]any)["description"] = "Optional: your own reference; logging the same one again changes nothing (no double entries on a retry)."
	// a change, not an addition (mcpChangePosts): the day's totals also go to Pocket Health
	write("nutrition", "log_food", "Log what the person ate in the food diary (\"two eggs and toast for breakfast\"). One item per food: {\"name\": \"egg\", \"qty\": 2}, {\"name\": \"toast\", \"qty\": 1}, with meal \"breakfast\". qty counts servings (2 eggs, 1 slice, 1 cup); amount + unit gives grams or millilitres instead (\"200 g of yoghurt\" = {\"name\": \"yoghurt\", \"amount\": 200, \"unit\": \"g\"}, \"300 ml milk\" = amount 300, unit ml). Known foods are worked out from the person's foods and the built-in list; a food not known (a dish, a restaurant meal) needs kcal for the whole item (and protein, carbs, fat in g if you can estimate them), else it is refused with a message saying so. The day's totals go to Pocket Health; the answer has the entries logged and the day (totals, goals, remaining kcal and macros, text).", "POST", "/entries/log", nutritionLog, "items")
	nutritionWater := mcpFields{}.field("ml", map[string]any{"type": "integer", "minimum": -5000, "maximum": 5000, "description": "Millilitres to add (a glass ≈ 250, a bottle 500); a negative amount takes some off."})
	// a change (mcpChangePosts): it alters the day's water total, which also goes to Pocket Health
	write("nutrition", "add_water", "Add water the person drank to a day (date YYYY-MM-DD, usually today) in millilitres: \"two glasses\" ≈ 500, \"a litre\" = 1000. The answer is the day's total water in ml.", "POST", "/water/{date}/add", nutritionWater, "ml")
	read("voice", "summary", "Get a short overview of your voice memos: how many (all, in the inbox waiting to be filed, starred, still being written out, failed), minutes recorded, how many today, and the latest inbox memos with their summary and how many suggested actions wait to be sent.", "/summary", "")
	read("voice", "memos", "List or filter your voice memos, newest first (oldest=1 reverses). q searches titles and words (accents ignored); kind note|todo|idea|journal|meeting|shopping|reminder; tag; inbox=1 only memos not filed yet; starred=1; status queued|working|done|failed; from and to YYYY-MM-DD (the day it was recorded). Each memo has title, recorded_at, duration_ms, status, summary, tidy (the cleaned text), transcript, kind, tags, actions (what the AI found, with sent_to once sent) and sent (what was made from it in other apps); counts has the totals.", "/memos", "q kind tag inbox starred status from to limit offset oldest")
	read("voice", "get_memo", "Read one voice memo: transcript (the words as written out, with timed segments), tidy (the cleaned text), summary, kind, tags, language, status (queued|working|done|failed, with error), actions the AI found (a list: type task|event|journal|expense with title, due, start, end, location, text, amount in major units, currency, date and notes; sent_to and sent_url once sent, skipped when dismissed) and sent (every item made from it in Notes, Tasks, Calendar or Money).", "/memos/{id}", "")
	read("voice", "search", "Search your voice memos' titles and words (1 to 200 characters, accents ignored).", "/search", "q limit")
	read("voice", "day", "Read one day's voice memos, oldest first, each with its time, length, summary, text and where it was sent: what Pocket Day puts in the journal.", "/day/{date}", "")
	read("voice", "tags", "List the tags on your voice memos with how many memos have each.", "/tags", "")
	memoFields := func(text string) mcpFields {
		f := textFields("title tags recorded_at "+text).enum("kind", "note", "todo", "idea", "journal", "meeting", "shopping", "reminder")
		f["tags"].(map[string]any)["description"] = "Comma-separated, e.g. \"work, ideas\"; replaces all tags."
		f["recorded_at"].(map[string]any)["description"] = "RFC 3339 time, e.g. 2026-11-05T09:30:00+01:00."
		return f
	}
	write("voice", "add_memo", "Keep a typed or dictated text as a voice memo (\"keep this as a memo: …\"). text is required; title, kind and tags are optional (recorded_at defaults to now). Pocket AI tidies it in the background when the person has that on: title, summary, kind and suggested actions.", "POST", "/memos",
		memoFields("text").field("language", map[string]any{"type": "string", "pattern": `^[a-z]{2,3}$`, "description": "Language code like bs or en (default: the person's setting)."}), "text")
	write("voice", "update_memo", "Update supplied memo fields: title, transcript (fix the words as written out; then pocket_voice_tidy_memo to tidy again), tidy (the cleaned text), summary, kind, tags, starred, filed (true takes it out of the inbox), recorded_at.", "PUT", "/memos/{id}",
		memoFields("transcript tidy summary").booleans("starred filed"), "")
	write("voice", "tidy_memo", "Run the Pocket AI tidy on one memo now and return it: rewrites summary, tidy text and the suggested actions not sent yet (so action indexes change), and title, kind and tags unless the person set them. Needs an AI connected in Pocket Voice.", "POST", "/memos/{id}/tidy", nil, "")
	send := textFields("title text folder due notes start end location currency date").enum("to", "note", "daily", "task", "event", "expense").integers("list_id calendar_id account_id category_id").
		field("action", map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "Index (0-based) in the memo's actions of the suggestion being sent; it is then marked sent and cannot be sent twice. Its fields are not filled in for you: pass them too."}).
		field("amount", map[string]any{"type": "number", "minimum": 0.01, "maximum": 10000000, "description": "Expense amount in MAJOR units (12.5 = 12.50), not minor units."})
	send["due"].(map[string]any)["description"] = "Task due day, YYYY-MM-DD."
	send["start"].(map[string]any)["description"] = "Event start, YYYY-MM-DD or YYYY-MM-DDTHH:MM (local time)."
	send["date"].(map[string]any)["description"] = "Expense day, YYYY-MM-DD (default today)."
	send["folder"].(map[string]any)["description"] = "Pocket Notes folder for a note (default: the person's Voice setting)."
	write("voice", "send_memo", "Send a memo, or one action found in it, into the person's own Pocket apps through Voice's links: to note (a new Pocket Notes note with the tidy text; title, text and folder optional), daily (a line in today's daily note; a journal action goes here with its text), task (Pocket Tasks: title required; due, notes, list_id), event (Pocket Calendar: title and start required; end, location, calendar_id) or expense (Pocket Money: title and amount required, amount in major units here; currency, date, account_id, category_id). Prefer the actions the AI already found (pocket_voice_get_memo): pass action = its index with its fields. Lists, calendars and accounts default to the person's Voice settings. This creates things in other apps: always ask first. Returns sent (app, kind, title, url) and the memo.", "POST", "/memos/{id}/send", send, "to")

	// Pocket Blog: the person's own blogs (several per person), posts and pages in Markdown, drafts until published, comments readers wrote and readers counted per day.
	read("blog", "summary", "Get a short overview of your blogs: each blog with its public address, published posts, drafts and comments waiting; readers in the last 7 days; the latest drafts, scheduled posts and recently published posts with their readers.", "/summary", "")
	read("blog", "blogs", "List your blogs with their public address (url), look (theme, accent), language, who can read them (visibility public|unlisted|private), comment mode and counts.", "/blogs", "")
	read("blog", "posts", "List your posts or pages, drafts first then newest. blog = a blog id (default all); kind post|page; status draft|scheduled|published; tag; q searches titles and text (accents ignored); sort updated|oldest|views. Each has title, slug, status, published_at or publish_at, excerpt, tags, words, views, comments_count and url (the public address; a draft's url carries its private preview link).", "/posts", "blog kind status tag q limit offset sort")
	read("blog", "get_post", "Read one post or page with its full Markdown body, excerpt, tags, status and dates, cover picture, readers, comments and where it came from (source: another Pocket app or an import).", "/posts/{id}", "")
	read("blog", "search", "Search your posts and pages (drafts too) by their words; each result has a snippet, status, date and url.", "/search", "q limit")
	read("blog", "comments", "List the comments readers wrote under your posts, newest first: status pending (waiting for your OK), approved or spam (default: all but spam); blog and post narrow it. Each has name, body, post_title and post_url.", "/comments", "blog post status")
	read("blog", "stats", "Readers of your blog: total in the last days (default 30) and the period before, readers per day, the most read posts and the number of comments. Counted without cookies, once a day per reader per post.", "/stats", "blog days")
	read("blog", "day", "Read what happened on your blog on one day: posts published and posts written, readers and comments.", "/day/{date}", "")
	read("blog", "tags", "List the tags on a blog's posts with how many posts have each (blog = a blog id; default your first blog).", "/tags", "blog")
	read("blog", "ideas", "Things the person did lately in their other Pocket apps that would make a good post (a trip they came back from, a book they finished, a place they loved, a recipe they cooked, a note tagged #blog), with why.", "/ideas", "")
	postFields := func() mcpFields {
		f := textFields("title body excerpt tags seo_description").enum("kind", "post", "page").booleans("featured comments in_menu")
		f["body"].(map[string]any)["description"] = "The post in Markdown: ## headings, paragraphs, - lists, **bold**, > quotes, links, ![alt](url) pictures; a YouTube or Vimeo link alone on a line becomes the video. No HTML (it is not shown). No # title line: the title is separate."
		f["tags"].(map[string]any)["description"] = "Comma-separated, e.g. \"travel, bosnia\"; replaces all tags."
		f["excerpt"].(map[string]any)["description"] = "One or two sentences shown in lists and when shared (empty: the start of the post)."
		return f
	}
	write("blog", "create_draft", "Start a post (or page) as a DRAFT in the person's blog, for them to finish and publish in Pocket Blog: nothing is public. Write it in the blog's language (pocket_blog_blogs) and the person's voice, from what they told you or from their other apps (a trip, a book, a place, a recipe); never invent facts about them, leave [placeholders] instead. blog_id picks the blog (default the first). Returns the post with its private preview url.", "POST", "/posts",
		postFields().integers("blog_id"), "title")
	write("blog", "update_post", "Change supplied fields of one of the person's posts: title, body (the whole Markdown text), excerpt, tags, seo_description, featured, comments, in_menu. A published post changes for readers at once: ask first.", "PUT", "/posts/{id}", postFields(), "")
	write("blog", "publish", "Publish a post on the blog now, or schedule it: at = an RFC 3339 time or YYYY-MM-DDTHH:MM local (empty = now). This makes it public to everyone who can read the blog: always ask first, naming the post and the blog.", "POST", "/posts/{id}/publish", textFields("at"), "")
	write("blog", "unpublish", "Take a published or scheduled post off the blog; it becomes a draft again. Readers no longer see it: always ask first.", "POST", "/posts/{id}/unpublish", nil, "")
	write("blog", "reply_comment", "Answer a reader's comment (id = the comment) as the blog's author; a waiting comment is approved with it. The answer is public: always ask first and show the text.", "POST", "/comments/{id}/reply", textFields("body"), "body")

	// Pocket Forms: the person's forms (public pages anyone can answer without an account) and the answers people sent; answers go on to Contacts, Projects, Tasks, Calendar, Notes, Mail and Sign from inside Pocket Forms.
	read("forms", "summary", "Get the state of your forms: how many answers wait (new), this week, forms taking answers and drafts, the latest answers (who, which form, a one-line preview) and each form with its answers, new answers and how many opened it.", "/summary", "")
	read("forms", "forms", "List your forms: title, status draft|open|closed, taking (takes answers now) and why not (draft, closed, date = its last day passed, full = its answer limit reached), responses, new, views, close_at, max_answers, the public url people answer at.", "/forms", "status")
	read("forms", "get_form", "Read one form with its questions (fields: id, type, label, help, required, options, role = what the answer means: name, email, phone, company, website, subject, message, budget, deadline, date), its words after sending, limits and what it does by itself with each answer (auto).", "/forms/{id}", "")
	read("forms", "answers", "List the answers people sent, newest first (default: all but spam): form = a form id; status new|seen|done|spam|open (new and seen); starred=1; q searches the answers and names; email = one person's answers; day = YYYY-MM-DD. Each has name, email, preview, status, form_title, created_at, and what was made from it in other apps (made).", "/responses", "form status starred q email day limit offset")
	read("forms", "get_answer", "Read one answer in full: every question with its answer (items), who sent it, its status and your note, and what was made from it in your other apps.", "/responses/{id}", "")
	read("forms", "stats", "Numbers of one form: people who opened it and answers per day (last 30 days), totals, and per question the counted choices, average stars or numbers, and the latest written answers.", "/forms/{id}/stats", "")
	read("forms", "search", "Search your forms and the answers people sent (names, emails, every answer).", "/search", "q limit")
	read("forms", "day", "Read one day in your forms: the answers that came in (time, who, which form, a one-line preview) and forms opened that day.", "/day/{date}", "")
	read("forms", "templates", "List the ready-made forms a new form can start from (key, name, what it is for, its questions): project (a client brief), contact, quote, appointment, feedback, event (sign-ups), rsvp, survey, blank.", "/templates", "")
	formFields := textFields("title description emoji template language thanks submit_label close_at").integers("max_answers").booleans("one_per_email")
	formFields["template"].(map[string]any)["description"] = "A ready-made form to start from (pocket_forms_templates): project, contact, quote, appointment, feedback, event, rsvp, survey or blank. Its questions are used unless you give fields."
	formFields["language"].(map[string]any)["description"] = "The words the page says itself: en, bs, hr, sr or de."
	formFields["close_at"].(map[string]any)["description"] = "The last day it takes answers, YYYY-MM-DD (empty: until closed)."
	formFields["fields"] = map[string]any{"type": "array", "maxItems": 60, "description": "The questions, in order; leave out to use the template's.", "items": map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"type", "label"}, "properties": map[string]any{
			"id":       map[string]any{"type": "string", "pattern": `^[a-z0-9_]{1,32}$`},
			"type":     map[string]any{"type": "string", "enum": []any{"text", "long", "email", "phone", "number", "url", "date", "time", "choice", "checkboxes", "dropdown", "yesno", "rating", "consent", "section"}},
			"label":    map[string]any{"type": "string", "maxLength": 300},
			"help":     map[string]any{"type": "string", "maxLength": 1000},
			"required": map[string]any{"type": "boolean"},
			"options":  map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "maxLength": 200}},
			"other":    map[string]any{"type": "boolean"},
			"role":     map[string]any{"type": "string", "enum": []any{"", "name", "email", "phone", "company", "website", "subject", "message", "budget", "deadline", "date"}},
		}}}
	write("forms", "create_form", "Make a form as a DRAFT for the person to check and open in Pocket Forms: nobody can answer it until they do. Start from a template (pocket_forms_templates) or give the questions; plain, friendly words in the person's language; ask only what is needed, and mark which question is the person's name and email (role) so answers can go on to their contacts. Returns the form with its url.", "POST", "/forms", formFields, "")
	write("forms", "open_form", "Open one of the person's forms so anyone with its link can answer (a form you made as a draft). Always ask first, naming the form.", "POST", "/forms/{id}/open", nil, "")
	write("forms", "close_form", "Close one of the person's forms: its page says it takes no more answers. Always ask first, naming the form.", "POST", "/forms/{id}/close", nil, "")

	// Pocket CRM: the person's deals (possible work for clients) on a pipeline by stage, each with its next step, due day and timeline.
	// Money: every deal has value in CENTS (450000 = 4,500.00) and the same as amount in currency units (4500), with its currency.
	read("crm", "summary", "Get the state of your sales: open deals and their value, the weighted value (value × chance), steps due today and overdue, deals with no next step and gone quiet, won this month and this year with their value, lost this year, win rate, closing soon. Every *_value is {currency: cents}: 450000 is 4,500.00.", "/summary", "")
	read("crm", "today", "Read the Today page: deals whose next step is overdue, due today and this week, with no next step, gone quiet (stale), closing soon and won lately; plus the summary. Each deal has amount (currency units), value (cents), currency, next_action, next_due, stage_name, contact and company.", "/today", "")
	read("crm", "deals", "List your deals: status open|won|lost|closed; stage = a stage id (pocket_crm_stages); q searches titles, people, companies and notes; due overdue|today|week|none|any (the next step); company; email = one person's deals; starred=1; tag; source; sort stage|due|value|updated|created|close|expected. Each deal: title, stage_name, status, amount in currency units and value in cents, currency, chance, weighted (cents), contact_name/email/phone, company, next_action, next_due, expected_close, overdue, stale, days_in_stage.", "/deals", "status stage q due company email starred tag source sort limit offset")
	read("crm", "get_deal", "Read one deal in full: the deal (amount in currency units, value in cents), its timeline (activities: notes, calls, meetings, emails, steps done, stage moves, newest first) and what it has in your other apps (made: contacts, tasks, events, invoices, projects, signings).", "/deals/{id}", "")
	read("crm", "search", "Search your deals (titles, people, emails, companies, notes); each deal comes with its link.", "/search", "q limit")
	read("crm", "people", "List who you sell to: deals grouped by company (else by person) with email, phone, how many deals are open, won and lost, open_value and won_value as {currency: cents}, the last activity and the deal ids. q filters by name or email.", "/people", "q")
	read("crm", "lookup", "Find the deals with one person (email) or company: for a contact from another app. Give email, company or both.", "/lookup", "email company")
	read("crm", "day", "Read one day in your sales: the timeline lines written that day (notes, calls, meetings, steps done, deals added, moved, won and lost, each with its deal) and the deals whose next step is due that day.", "/day/{date}", "")
	read("crm", "reports", "Get sales reports for the last months (default 12, up to 36): per month deals added, won, lost and won value (cents, the person's currency); the open pipeline per stage (funnel), why deals were lost, where deals came from (sources), days to win and the average won deal.", "/reports", "months")
	read("crm", "stages", "List the pipeline's stages in order: id, name, kind open|won|lost, probability (% chance a deal there is won), how many deals and their value (cents).", "/stages", "")
	lead := textFields("title currency contact_name contact_email contact_phone company description next_action next_due expected_close stage note").numbers("amount").
		enum("source", "", "referral", "website", "form", "email", "event", "social", "repeat", "cold", "other")
	lead["amount"].(map[string]any)["description"] = "What the work is worth in currency units (4500 = 4,500.00), not cents. Leave out when nobody said."
	lead["amount"].(map[string]any)["minimum"] = 0
	lead["currency"] = map[string]any{"type": "string", "pattern": `^[A-Za-z]{3}$`, "description": "Three letters (EUR, BAM, USD); leave out for the person's own currency."}
	for _, day := range []string{"next_due", "expected_close"} {
		lead[day] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	}
	lead["next_due"].(map[string]any)["description"] = "The day the next step is due, YYYY-MM-DD."
	lead["expected_close"].(map[string]any)["description"] = "When the person expects to win or lose it, YYYY-MM-DD."
	lead["stage"].(map[string]any)["description"] = "The stage by its name (pocket_crm_stages); leave out for the first stage."
	lead["next_action"].(map[string]any)["description"] = "The next thing to do, in a few words (\"Call Ana about the quote\")."
	lead["note"].(map[string]any)["description"] = "A first line for the deal's timeline: where it came from, what was said."
	write("crm", "add_lead", "Add a deal (possible work for a client) to the person's pipeline: a short title (\"Website for Bistro Sara\"), who it is with and the company, what it is worth (amount, in currency units), the next step and its day. It goes to the first stage unless stage names another. When the person has it on, Pocket CRM also puts the next step in Pocket Tasks. Returns the deal with its id.", "POST", "/deals", lead, "")
	activity := textFields("title body").enum("kind", "note", "call", "meeting", "email", "step")
	activity["kind"].(map[string]any)["description"] = "note (default), call, meeting, email (one that was sent or came in) or step (something done)."
	write("crm", "add_note", "Write a line on one deal's timeline (id = the deal): a note, or a call, meeting or email that happened, or a step done, with a short title and what was said in body. It does not move the deal or change its next step.", "POST", "/deals/{id}/activities", activity, "")

	// Pocket League: sport with friends. Groups per sport (tennis, 5-a-side, padel…), games (planned at a time, or voting on times),
	// each person's answer in|maybe|out (past the player limit "in" becomes waiting), results, a table with ratings, a chat.
	// Reads only, plus answering for the person themself: the AI never writes in the chat, enters or confirms results, plans or cancels games.
	read("league", "summary", "Get the person's sport in one call: the next planned game (when, where, which group, how many are in of the minimum, the person's own answer), their groups with unread chat counts, how many games are coming and this month's played and won.", "/summary", "")
	read("league", "today", "Read the League home page: the next games (next), the coming games the person has not answered yet (unanswered), games still voting on a time (voting), results others entered that wait for the person to confirm (confirm) and the person's last results (recent, W/D/L as won); unread chat messages and groups counted.", "/today", "")
	read("league", "upcoming", "List the coming games in all the person's groups, soonest first: id, group_name, sport, status planned|voting, starts_at (local time; empty while voting, then vote_until), venue, min/max players, in/maybe/out/waiting counts, my_answer (the person's own: in, maybe, out, waiting or empty = not answered), cost_cents and currency, comments.", "/upcoming", "")
	read("league", "games", "List games: group = a group id; upcoming=1 or past=1; from and to (YYYY-MM-DD or YYYY-MM-DDTHH:MM); limit. Same fields as pocket_league_upcoming, cancelled games too (cancel_reason).", "/games", "group upcoming past from to limit")
	read("league", "get_game", "Read one game in full: the game (when, where, status, counts, my_answer), its group, people (each with their answer in|maybe|out|waiting or none yet, note, side and rating), the times to vote on (options) while voting, its results (matches), player-of-the-game votes and each player's share of the cost (share_cost, cents).", "/games/{id}", "")
	read("league", "groups", "List the person's groups: id, name, sport (sport_label), venue, usual weekday and time, members, unread chat count and the next planned game (next_game). archived=1 includes archived groups.", "/groups", "archived")
	read("league", "get_group", "Read one group: the group, its members (name, role organiser|player) and its coming games (upcoming).", "/groups/{id}", "")
	read("league", "table", "Read a group's league table: rows in position order with name, played, won, drawn, lost, points, for/against (games for racket sports, with sets_won/sets_lost), rating (Elo-like, starts at 1000) and its last change, form (the last five, W/D/L, oldest first), streak and player-of-the-game count (mvp). period = all (default), year, season, month or 90d.", "/groups/{id}/table", "period")
	read("league", "player", "Read one player in a group (id = the group, uid = the player's user_id from the table or members): their line in the table, rating over time, head-to-head against each other player and their last matches.", "/groups/{id}/players/{uid}", "")
	read("league", "results", "List results (matches), newest first: group = a group id, game = a game id, player = a user id, from and to (YYYY-MM-DD), limit. Each has the match (played_at, players with side a|b or place, score, text like \"6–4 4–6 7–5\", winner a|b|draw, state confirmed|waiting|disputed, names by user id), group_name and emoji.", "/matches", "group game player from to limit")
	read("league", "search", "Search the person's groups, players, games (title, venue, notes) and chat lines; each hit has kind group|player|game|message, title, subtitle and its link.", "/search", "q limit")
	read("league", "lookup", "Find whether someone (email) plays with the person, in which groups, and their record against each other there (played, won, drawn, lost, together, last played): for a contact from another app.", "/lookup", "email")
	read("league", "day", "Read one day in the person's sport: the games that day (time, group, venue, status, my_answer, how many in) and the results they played (text, outcome W/D/L).", "/day/{date}", "")
	// Tournaments (a cup, an Americano evening, a ladder…): reads only. Entering or confirming results, signing up or dropping out stay in the app.
	tournaments := read("league", "tournaments", "List the tournaments in the person's groups, the ones being played first (status live), then open for sign-up (signup), then finished or cancelled: id, name, format (knockout, double_elim, round_robin, groups_knockout, swiss, americano, mexicano, kotc, team_ties, ranking = scored rounds like golf, ladder, box_league, arena) with format_name in words, status, entries (how many are in) and entry_kind player|pair|team, starts_at and ends_at (local time), venue_name, capacity, signup_until, group_id, group_name, sport, emoji, rated (counts in the group's table), winner (the winner's name once finished) and mine (the person plays in it). group_id = only that group's tournaments. For who leads, the draw or the next match, read the tournament with pocket_league_get_tournament.", "/tournaments", "")
	tournaments.Within = "/groups/{group_id}/tournaments"
	tournaments.Schema["properties"].(mcpFields)["group_id"] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": "A group id (pocket_league_groups): only that group's tournaments. Leave out for all of them."}
	compileMCPAppTool(tournaments)
	read("league", "get_tournament", "Read one tournament in full (id = the tournament). tournament: name, format, status signup|live|finished|cancelled, starts_at, venue, winner. entries: each entry (a player, pair or team) with id, name, status in|withdrawn|disqualified, seed, rating, members (user_id; names maps user ids to names). mine: the person's own entry ids. stages in order (id, kind, name, status live|done; groups then knockout has a groups stage, then the knockout): tables = standings per pool, keyed by the pool id in groups (rows in rank order: id = the entry, rank, played, won, drawn, lost, points, for/against, sets, decided_by = the tie-break that split them), or leaderboard for scored rounds (golf, bowling: rank, total, gross, rounds). matches: id, stage_id, round, label (\"Final\", \"Semi-final 1\"…), a_entry and b_entry (entry ids; sides.a/sides.b for formats where partners change, like Americano), court_id (courts), scheduled_at, status pending (waiting for who plays) | ready | called (to a court now) | reported (waiting for the other side to confirm) | done | void, winner a|b|draw, score and a_total/b_total (points, Americano). podium once done: rank 1-3 with entry_id. A ladder also has challenges (challenger, defender, status, play_by). For \"who is leading\" read the table or leaderboard of the live stage; for \"my next match\" the first match not done with one of mine as a side; for \"who won\" the podium or tournament.winner.", "/tournaments/{id}", "")
	answer := mcpFields{}.enum("answer", "in", "maybe", "out", "")
	answer["answer"].(map[string]any)["description"] = "in, maybe or out; an empty string takes the answer back. When the game is full, in puts the person on the waiting list (the reply's result says waiting)."
	answer["note"] = map[string]any{"type": "string", "maxLength": 120, "description": "A few words next to the answer that the group sees (\"10 min late\"); leave out when the person said none."}
	write("league", "answer", "Answer for the person (only themself) whether they are coming to a planned game: in, maybe or out (id = the game). The group sees it and, when a place frees up, the first one waiting moves up. A game still voting on times or cancelled refuses. Always ask first, naming the game and its day. Returns the game with result = what the answer became (in or waiting).", "POST", "/games/{id}/answer", answer, "answer")

	// Pocket Tables: the person's own small databases, like Airtable. A base holds tables; a table has fields (columns, each with a type),
	// views (saved filters and sorts: grid, board, calendar, gallery) and rows. Rows come with named = their values as text by field name.
	read("tables", "summary", "Get the state of the person's tables: how many bases, tables and rows; the rows with a reminder date due today (due_today), in the next week (soon) and overdue (late), and the 10 rows changed last (recent). Each row: id, title, table_id, table, base, field and date (the date field and its day).", "/summary", "")
	read("tables", "today", "Read the Tables home page: the rows whose reminder date is today (today), overdue in the last 30 days (late) and in the next week (soon), each with id, title, table_id, table, base, field and date.", "/today", "")
	read("tables", "list", "List the person's bases, each with its tables: base id, name, icon, description, and per table id, name, icon and records (how many rows). Start here to find a table's id.", "/bases", "")
	read("tables", "table", "Read one table's layout (id = the table, from pocket_tables_list): the table, its base, fields (id, name, type text|longtext|number|currency|percent|rating|checkbox|date|select|multiselect|email|phone|url|link|attachment|pocket|lookup|rollup|formula|created|updated|autonumber, options such as select choices, primary = the row's title) and views (id, name, type, config with filters and sorts). Read it before adding a row, to know the field names and choices.", "/tables/{id}", "")
	rows := read("tables", "rows", "List one table's rows (id = the table): view = a view id (pocket_tables_table) to get only its rows in its order, q = words to find in any field, limit and offset. Each row: id, number, title, named (its values as text by field name — use these), values (the same keyed by field id), comments (how many), created_at, updated_at; plus total.", "/tables/{id}/records", "view q limit offset")
	rows.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)["view"] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": "A view id from pocket_tables_table; leave out for all rows."}
	rows.Query = map[string]string{"named": "1"}
	compileMCPAppTool(rows)
	read("tables", "get_row", "Read one row in full (id = the row): record (title, named = values as text by field name, values by field id), its table and fields, history (who changed what, comments) and made (tasks, notes, events made from it in other apps).", "/records/{id}", "")
	read("tables", "search", "Search every table: rows with the words in any field (items: id, title, table_id, table, base, the field that matched and a snippet) and tables whose name matches (tables).", "/search", "q limit")
	read("tables", "lookup", "Find the rows about one person: email = a row with that address in an email field; name = a row whose text field is exactly that name or that links that contact. For a contact from another app.", "/lookup", "email name")
	read("tables", "day", "Read one day in the person's tables: the rows with a date on that day (any date field), each with title, table, base, field and date.", "/day/{date}", "")
	row := mcpFields{"values": map[string]any{"type": "object", "minProperties": 1, "maxProperties": 200,
		"additionalProperties": map[string]any{"anyOf": []any{map[string]any{"type": "string", "maxLength": 30000}, map[string]any{"type": "number"}, map[string]any{"type": "boolean"},
			arraySchema(map[string]any{"anyOf": []any{map[string]any{"type": "string", "maxLength": 1000}, map[string]any{"type": "integer"}}})}},
		"description": "The row's values by field name (as pocket_tables_table lists them) or field id: text as text, numbers as numbers (\"12,50\" is read too), checkbox true/false, a date as YYYY-MM-DD (or YYYY-MM-DDTHH:MM), a select by its choice name (a new name becomes a new choice), a multiselect as a list of names, a link as a list of row ids in the other table. Worked-out fields (formula, lookup, rollup, created, autonumber) cannot be set."}}
	addRow := write("tables", "add_row", "Add a row to one of the person's tables (id = the table): values by field name. Read the table first (pocket_tables_table) for its field names, types and select choices. Returns the row with its id.", "POST", "/tables/{id}/records", row, "values")
	addRow.Body = map[string]any{"typecast": true}

	// Pocket Chat: the person's conversations (direct, groups and "Me", their own space), shared with the people in them; messages can be text, cards from other apps, polls, to-do lists, voice messages (with their words) and Pocket AI's answers.
	read("chat", "summary", "Get what waits in your chats: unread counts, the conversations with unread messages (name, how many, the last message), the messages that mention you, and today's activity per conversation.", "/summary", "")
	read("chat", "conversations", "List your conversations: id, name as you see it (the other person in a direct chat, \"Me\" for your own space), kind direct|group|self, members, unread and mentioned counts, the last message, muted/pinned/archived.", "/conversations", "")
	read("chat", "messages", "Read a conversation's messages, oldest first (default the latest 60): author_name, body, kind (text, card, poll, todo, voice, ai, app, system), created_at, data (a card's title, a poll's question and options with votes, a to-do list's items, a voice message's words as data.text), reactions. before / after = a message id; around = a message id to read the messages near it.", "/conversations/{id}/messages", "before after around limit")
	read("chat", "search", "Search the messages in your conversations (accents ignored), newest first: each has snippet, author_name, conversation_title, conversation_id, created_at. conversation = an id to search one; from = a person's user id; kind photos|files|links|cards|polls|todos|voice; since/until YYYY-MM-DD.", "/search", "q conversation from kind since until limit")
	read("chat", "unread", "Count what you have not read: messages, conversations and mentions.", "/unread", "")
	read("chat", "day", "Read one day in your chats: messages per conversation, plans people suggested (with day and time), cards shared from other apps, photos and voice messages.", "/day/{date}", "")
	read("chat", "people", "List the people on this Pocket Chat (id, name, email, online now) — who the person can write to.", "/people", "")
	read("chat", "saved", "List the messages the person saved (starred), newest first, with their conversation.", "/saved", "")

	// Pocket Renewals: what runs out (passports, insurance, warranties, domains, subscriptions) and when. Costs are minor units.
	read("renewals", "summary", "Get the renewals overview in one call: counts (expired, coming up, found automatically and waiting for a check), what has expired, what is coming up, what was found in other apps, trips a passport does not cover, recent renewals and the yearly cost. Renewal costs can overlap Money: confirmed bookings carry money_tx_id in history; never add Renewals and Money totals without verifying it.", "/overview", "")
	read("renewals", "due", "List what runs out within the next days (default 30) and what has already run out, soonest first, each with status and days_left.", "/due", "days")
	read("renewals", "list", "List renewals (passports, ID cards, insurance, vehicle registration, warranties, domains, certificates, subscriptions, contracts), soonest first. status: expired, soon, ok, undated, archived or check (found automatically). group: identity, insurance, vehicle, warranty, online, subscription, contract, health, other.", "/renewals", "q kind group holder status source before after archived")
	read("renewals", "get", "Read one renewal: dates, reminders, the suggested next end date, its renewal history and travel warnings.", "/renewals/{id}", "")
	read("renewals", "history", "Read renewals done, newest first, with the old and new date and what each cost (minor units).", "/history", "renewal_id limit")
	read("renewals", "search", "Search renewals by name, whose, provider, number or notes.", "/search", "q")
	read("renewals", "travel", "Check coming trips in Pocket Travel against passports, ID cards and visas: which run out before or soon after a trip.", "/travel", "")
	renewal := func() mcpFields {
		f := textFields("title holder provider number issued_on expires_on remind_days currency renew_url notes").integers("renew_n lead_days cost").booleans("auto_renews").
			enum("kind", "passport", "id_card", "driving_licence", "visa", "insurance", "vehicle", "warranty", "domain", "certificate", "subscription", "membership", "contract", "health", "licence", "other").
			enum("renew_unit", "day", "week", "month", "year")
		f["expires_on"].(map[string]any)["description"] = "The day it runs out, YYYY-MM-DD; leave empty when unknown."
		f["cost"].(map[string]any)["description"] = "What a renewal costs, minor units (5500 = 55.00)."
		f["remind_days"].(map[string]any)["description"] = "Days before the date to remind, e.g. \"90,30,7\"; empty = the kind's usual reminders."
		return f
	}
	write("renewals", "add", "Add something that runs out (a passport, an insurance policy, a warranty, a domain, a subscription). The usual term and reminders follow from the kind.", "POST", "/renewals", renewal(), "title")
	write("renewals", "update", "Update supplied fields of a renewal this assistant added, or archive it.", "PUT", "/renewals/{id}", renewal().booleans("archived"), "")
	write("renewals", "renew", "Mark a renewal renewed: the new end date (default one usual term on), what it cost and a note. Optionally books the cost in Pocket Money.", "POST", "/renewals/{id}/renew", textFields("renewed_on expires_on note number issued_on").integers("cost").booleans("money"), "")

	// Pocket Wealth: everything owned and owed, net worth over time, investments, debts, the plan. Money is in minor units of the currency it is in.
	read("wealth", "summary", "Get net worth in one call: what you own and owe, cash at hand, the change this month and this year, the groups (cash, investments, pension, property, vehicles, valuables, debts) and what needs an update or matures soon, in the person's currency. Linked Pocket Money balances are included once: never add Wealth and Money totals together.", "/summary", "")
	read("wealth", "history", "Get net worth at each month end (months back, default 24) with what was owned and owed.", "/history", "months")
	read("wealth", "assets", "List assets and debts with their value (whole and the person's share, own currency and the base currency), the change this month, when it was last updated, the gain, and whether it is linked to another app. group: cash, investments, pension, property, vehicles, valuables, other, debts.", "/assets", "q group closed")
	read("wealth", "get", "Read one asset or debt: its values over time, holdings, activity, statements kept with it and, for a debt, when it is paid off.", "/assets/{id}", "")
	read("wealth", "positions", "List investment holdings (funds, shares, bonds, coins) with units, price, value, cost and gain, the mix by kind and income from dividends and interest in the last 12 months.", "/positions", "")
	read("wealth", "activity", "List buys, sells, dividends, interest, deposits and withdrawals, newest first.", "/activity", "asset kind from to limit")
	read("wealth", "debts", "List debts with what is owed, the rate, the monthly payment and when each is paid off (with extra = minor units more a month: how much sooner and the interest saved).", "/debts", "extra")
	read("wealth", "plan", "Get the projection: net worth year by year at the current saving and expected growth, the financial independence number, when it is reached (and at what age), what it would pay a month, how long cash at hand lasts, and the milestones.", "/plan", "")
	read("wealth", "search", "Search assets, debts and holdings by name, bank, symbol or notes.", "/search", "q")
	read("wealth", "maturing", "List deposits that mature, fixed rates that end and money due back, with the date and days left.", "/maturing", "days")
	write("wealth", "add", "Add something owned or owed (a savings account, a pension, a flat, a car, a loan) with its current value. kind decides the group; value is minor units of currency; debts are what is still owed.", "POST", "/assets",
		textFields("name currency institution number notes matures_on bought_on date").integers("value payment bought_price invested").numbers("rate share growth").booleans("include").
			enum("kind", "cash", "savings", "deposit", "investment", "pension", "crypto", "property", "vehicle", "valuables", "business", "receivable", "other", "mortgage", "loan", "credit_card", "debt").
			enum("value_mode", "manual", "positions", "estimate"), "name kind")
	write("wealth", "update_value", "Type in what an asset is worth (or a debt still owes) on a day, from a statement: the whole of it in minor units of its currency; a second value the same day replaces the first.", "POST", "/assets/{id}/valuations", textFields("date note").integers("value"), "value")
	write("wealth", "record", "Record a buy, sell, dividend, interest, deposit, withdrawal or fee on an asset. A buy or sell needs position_id and units and updates that holding; a deposit or withdrawal updates what was put in. amount is minor units, always positive.", "POST", "/activity",
		textFields("date note currency").integers("asset_id position_id amount").numbers("units").enum("kind", "buy", "sell", "dividend", "interest", "deposit", "withdrawal", "fee"), "asset_id kind amount")

	// Pocket Parcels: deliveries on their way (in) and parcels sent (out), with each one's tracking timeline. Prices are minor units of currency.
	read("parcels", "summary", "Get what needs attention about parcels in ONE call: what arrives today or is out for delivery, what waits at a pick-up point, problems (failed attempts, customs, no news for days), what is on the way, return deadlines in the next 7 days and what was delivered today, plus how many shipping emails wait to be added.", "/summary", "")
	read("parcels", "overview", "Get the parcels home screen: needs_you (out for delivery, ready to pick up, expected today), on_the_way, problems, recent deliveries, returns and counts.", "/overview", "")
	read("parcels", "list", "List parcels. view: active (default, on their way), delivered, archived, returns (a return deadline that is still open), problems, all. Each has status (pending, info_received, in_transit, out_for_delivery, pickup, delivered, exception, returned, expired), status_text (latest step), location, expected_on/expected_until, carrier_name and tracking_url.", "/parcels", "view q shop carrier direction status limit offset")
	read("parcels", "get", "Read one parcel with its whole tracking timeline (events, newest first), papers and links to the payment, wish, inventory item and tasks in the other Pocket apps.", "/parcels/{id}", "")
	read("parcels", "search", "Search parcels by name, tracking number, shop, order number, recipient or latest step.", "/search", "q limit")
	read("parcels", "day", "Read one day of parcels: delivered, sent, added, expected that day, and every tracking step of the day.", "/day/{date}", "")
	read("parcels", "stats", "Get parcels per month, how many days deliveries took (overall, per shop and per carrier) and what orders and extra costs (customs, postage) came to per currency. Prices can overlap Pocket Money: a linked payment carries money_tx_id; never add Parcels and Money totals without verifying it.", "/stats", "months")
	parcel := func() mcpFields {
		f := textFields("tracking_number carrier title shop order_number ordered_on currency expected_on expected_until return_by notes contact_name").
			integers("price extra_cost").enum("direction", "in", "out")
		f["tracking_number"].(map[string]any)["description"] = "As printed; spaces are fine. The carrier is recognised from it when it can be (China Post, UPS, DHL, Cainiao…)."
		f["carrier"].(map[string]any)["description"] = "Carrier key when the number alone does not say: dhl-express, dhl-paket, ups, fedex, dpd, gls, hermes, amazon, postnl, inpost, cainiao, yunexpress, 4px, euroexpress, x-express, overseas, post-<country code> (post-ba, post-de…) or other."
		f["price"].(map[string]any)["description"] = "What the order cost, minor units of currency (4999 = 49.99)."
		f["direction"].(map[string]any)["description"] = "in = coming to you (default), out = a parcel you sent."
		return f
	}
	write("parcels", "add", "Add a parcel to follow: a tracking number, or at least a title for a delivery without one. Parcels then follows it (17TRACK with the person's key, or Cainiao for AliExpress parcels) and sends pushes when it is out for delivery, ready to pick up or delivered.", "POST", "/parcels", parcel(), "")
	write("parcels", "update", "Update supplied parcel fields (title, shop, expected day, return deadline, notes, archived…). return_state: keeping, returning (a task to send it back is added when the person wants that) or returned.", "PUT", "/parcels/{id}", parcel().booleans("archived").
		enum("status", "pending", "info_received", "in_transit", "out_for_delivery", "pickup", "delivered", "exception", "returned", "expired").enum("return_state", "", "keeping", "returning", "returned"), "")
	write("parcels", "add_step", "Add a step to a parcel's timeline (a call from the courier, picked up from the neighbour, customs charged). The status follows from the words unless given.", "POST", "/parcels/{id}/events",
		textFields("description location at").enum("status", "info_received", "in_transit", "out_for_delivery", "pickup", "delivered", "exception", "returned"), "description")
	write("parcels", "mark_delivered", "Mark a parcel delivered (it arrived). at: when, YYYY-MM-DD or a full time; send today's date when unsure.", "POST", "/parcels/{id}/delivered", textFields("at"), "")
	write("parcels", "check", "Ask the parcel's tracking service for news now (normally checked by itself every few hours).", "POST", "/parcels/{id}/check", nil, "")

	// Pocket Sign: PDFs sent to people to sign (contracts, quotes, consent forms) and what others sent the person to sign.
	read("sign", "summary", "Get what needs attention about signing in ONE call: documents waiting for the person's own signature (to_sign, sent by someone on this box), documents waiting for others (waiting: who it waits for in next, progress done/total), drafts, recently completed and the latest activity, plus counts per status.", "/summary", "")
	read("sign", "overview", "Get the Pocket Sign home screen: to_sign (waiting for the person), waiting (sent, waiting for others), drafts, recent (completed) and activity (the history across documents), with counts.", "/overview", "")
	read("sign", "list", "List documents sent for signing (templates are separate). status: draft, pending (sent, waiting for people), completed, declined, expired, voided, ended (any finished one) or all. Each has its people (recipients with role signer/approver/viewer/cc and status draft/waiting/sent/opened/done/declined), progress, next (who it waits for) and sent_at/completed_at/expires_at.", "/documents", "status q limit offset")
	read("sign", "get", "Read one document: its people (with ids, status, when they opened and signed, and why someone declined), the boxes to fill in, the message, expiry and reminders, the signed PDF if done, and the AI summary when one was made.", "/documents/{id}", "")
	read("sign", "audit", "Read a document's history, oldest first: created, sent, emailed, opened, signed, approved, declined, reminded, completed, voided, with who, when and from which IP. This is what the signing certificate prints.", "/documents/{id}/audit", "")
	read("sign", "templates", "List the person's templates (reusable PDFs with people and boxes placed): each has roles (recipient_id, role_name like Client or Tenant, role, and a fixed name/email if any), how often it was used and its public link if switched on.", "/templates", "")
	read("sign", "to_sign", "List what others on this box sent the person to sign: waiting (their turn now) and done (already signed), with title, who sent it, role and expiry. The person signs in Pocket Sign under To sign.", "/inbox", "")
	read("sign", "search", "Search documents and templates by title, people's names or emails, and the words in the PDF.", "/search", "q")
	read("sign", "day", "Read one day of signing: documents sent, completed, signed by others (signed) and signed by the person (signed_by_me).", "/day/{date}", "")
	read("sign", "stats", "Get documents sent, completed and declined per month, the completion rate and the average hours from sending to the last signature.", "/stats", "months")
	// No tool sends a document out (that publishes it to other people, like /send and /share): use_template makes a draft the person sends in Pocket Sign.
	write("sign", "remind", "Email one person a reminder to sign now (only someone whose turn it is: status sent or opened). recipient_id comes from pocket_sign_get. Pocket Sign also reminds by itself every few days when the document says so.", "POST", "/documents/{id}/remind",
		mcpFields{}.integers("recipient_id"), "recipient_id")
	write("sign", "void", "Cancel a document that is still waiting for signatures. The links stop working and the people who had one are told by email. reason: a short plain reason they will see.", "POST", "/documents/{id}/void", textFields("reason"), "reason")
	write("sign", "use_template", "Make a DRAFT document from a template and fill in its people: people lists each role of the template (recipient_id from pocket_sign_templates roles) with the name and email of who takes it. Nothing is sent: give the person the draft's link (item_url) so they check it and press Send in Pocket Sign. title and message are optional (the template's own are used).", "POST", "/templates/{id}/use",
		textFields("title message").field("people", arraySchema(objectSchema(mcpFields{}.integers("recipient_id").field("name", map[string]any{"type": "string", "maxLength": 200}).field("email", map[string]any{"type": "string", "maxLength": 320}), "recipient_id email"))), "people")

	read("money", "accounts", "List financial accounts available to you.", "/accounts", "")
	read("money", "categories", "List transaction categories.", "/categories", "")
	read("money", "summary", "Get complete spending/income totals, category breakdowns, previous-period comparison and top expenses in ONE call. Prefer this for how much did I spend, monthly summaries and spending by category; do not fetch every transaction or query each category separately. All monetary values are integer minor units of the returned home currency (divide by 100 for EUR). With no dates, uses the current budget month; month=YYYY-MM follows the configured payday. Explicit from/to (inclusive YYYY-MM-DD) select calendar/custom ranges.", "/reports/overview", "from to month year account")
	read("money", "transactions", "Search individual financial transactions. For aggregate spending/income or category totals, prefer pocket_money_summary. Category filters require numeric IDs, not category names.", "/transactions", "q account kind from to category limit offset")
	read("money", "get_transaction", "Read one transaction.", "/transactions/{id}", "")
	tx := textFields("date description payee notes currency").integers("account_id amount category_id").strings("tags").enum("kind", "expense", "income")
	write("money", "create_transaction", "Record an expense or income. Amount is an integer in minor currency units (for example 1250 = 12.50 EUR). This records bookkeeping; it does not transfer funds.", "POST", "/transactions", tx, "account_id amount kind date description")
	write("money", "update_transaction", "Update supplied transaction fields. Amount uses minor currency units.", "PUT", "/transactions/{id}", tx, "")

	read("split", "groups", "List expense-sharing groups you belong to.", "/groups", "")
	read("split", "get_group", "Read a group's members and details.", "/groups/{id}/", "")
	read("split", "expenses", "Read expenses in a group.", "/groups/{group_id}/expenses", "q from to limit offset")
	read("split", "balances", "Read a group's balances.", "/groups/{group_id}/balances", "")
	write("split", "create_group", "Create an expense-sharing group; does not invite other users.", "POST", "/groups", textFields("name description currency"), "name currency")
	share := objectSchema(mcpFields{}.integers("member_id share"), "member_id share")
	write("split", "create_expense", "Record a shared expense. Amount and fixed shares use minor currency units; percentage shares total 10000; include every participant in shares (share=1 for even); paid_by is a group member ID. This does not move money.", "POST", "/groups/{group_id}/expenses", textFields("title date currency category notes").integers("amount paid_by").enum("split_mode", "even", "amount", "percent", "shares").field("shares", arraySchema(share)), "title amount paid_by date split_mode shares")

	read("invoicing", "clients", "List your billing clients.", "/clients", "q archived")
	read("invoicing", "invoices", "List your invoices.", "/invoices", "q status client_id from to limit offset")
	read("invoicing", "get_invoice", "Read an invoice and line items.", "/invoices/{id}", "")
	write("invoicing", "create_client", "Create a billing client record without sending messages.", "POST", "/clients", textFields("name contact_name email phone address1 city postal_code country currency notes"), "name")
	write("invoicing", "update_client", "Update supplied billing client details without sending messages.", "PUT", "/clients/{id}", textFields("name contact_name email phone address1 city postal_code country currency notes"), "")
	line := objectSchema(textFields("description unit").numbers("quantity unit_price tax_rate"), "description quantity unit_price")
	write("invoicing", "create_draft", "Create a draft invoice without sending it. Line prices use major currency units, for example 12.50 EUR.", "POST", "/invoices", textFields("issue_date due_date currency notes terms").integers("client_id").field("items", arraySchema(line)), "client_id issue_date due_date items")

	read("review", "list", "List reviews you participate in.", "/reviews", "q status role")
	read("review", "get", "Read a review and your role.", "/reviews/{id}/", "")
	read("review", "tasks", "Read tasks on a review.", "/reviews/{review_id}/tasks", "")
	read("review", "comments", "Read review comments.", "/reviews/{review_id}/comments", "")
	write("review", "create", "Create a review workspace without inviting participants.", "POST", "/reviews", textFields("title description due_date"), "title")
	write("review", "create_task", "Create a review task without assigning or messaging another person.", "POST", "/reviews/{review_id}/tasks", textFields("title notes due_date"), "title")

	write("review", "update_task", "Update a review task or mark it done, preserving omitted fields.", "PUT", "/reviews/{review_id}/tasks/{task_id}", textFields("title notes due_date").enum("status", "open", "done"), "")

	read("travel", "trips", "List trips you can access.", "/trips", "q archived")
	read("travel", "get_trip", "Read a trip's details.", "/trips/{id}/", "")
	read("travel", "journal", "Read a trip journal.", "/trips/{trip_id}/journal", "")
	read("travel", "checklists", "Read packing and travel checklists.", "/trips/{trip_id}/checklists", "")
	trip := textFields("name destination description start_date end_date currency notes")
	write("travel", "create_trip", "Create a trip plan without inviting other users.", "POST", "/trips", trip, "name")
	write("travel", "update_trip", "Update supplied trip details.", "PUT", "/trips/{id}/", trip, "")
	write("travel", "add_journal", "Add a trip journal entry.", "POST", "/trips/{trip_id}/journal", textFields("date title body"), "body")

	read("office", "sources", "List document sources available to you; credentials are omitted.", "/sources", "")
	read("office", "list", "List documents in an accessible source folder.", "/sources/{source_id}/ls", "path")
	read("office", "read_text", "Read bounded text from DOCX, XLSX, PPTX, OpenDocument or UTF-8 files. Images, formatting and live formulas are not interpreted.", "/sources/{source_id}/text", "path")
	read("office", "recent", "List recently opened office documents.", "/recent", "")
	write("office", "create", "Create a blank office document, spreadsheet or presentation.", "POST", "/sources/{source_id}/new", textFields("path name").enum("type", "docx", "xlsx", "pptx"), "name type")
	write("office", "rename", "Rename a document within its existing folder.", "POST", "/sources/{source_id}/rename", textFields("from name"), "from name")

	read("podcasts", "subscriptions", "List your podcast subscriptions.", "/podcasts", "q kind sort")
	read("podcasts", "episodes", "Search episodes in your library.", "/episodes", "q podcast status saved queued limit offset")
	read("podcasts", "get_episode", "Read an episode and its show notes.", "/episodes/{id}", "")
	read("podcasts", "queue", "Read your listening queue.", "/queue", "")
	write("podcasts", "subscribe", "Subscribe to a podcast feed URL. The application fetches the feed.", "POST", "/podcasts", textFields("url title"), "url")
	write("podcasts", "favorite", "Set a podcast's favorite flag.", "POST", "/podcasts/{id}/favorite", mcpFields{}.booleans("favorite"), "favorite")
	write("podcasts", "save_episode", "Save an episode to your saved list.", "POST", "/episodes/{id}/save", nil, "")
	write("podcasts", "queue_episode", "Add an episode to your listening queue.", "POST", "/episodes/{id}/queue", nil, "")

	read("subtitles", "titles", "List titles in the subtitle library.", "/library/titles", "q limit offset")
	read("subtitles", "files", "List subtitle files.", "/library/files", "q title lang status library season limit offset")
	read("subtitles", "get_file", "Read a subtitle file's metadata.", "/files/{id}/", "")
	read("subtitles", "text", "Read subtitle text (up to the application's preview limit).", "/files/{id}/text", "").TextResponse = true
	write("subtitles", "nudge", "Shift the timing of one subtitle file by a signed millisecond offset; creates the application's normal backup.", "POST", "/files/{id}/nudge", mcpFields{"ms": map[string]any{"type": "integer", "minimum": -60000, "maximum": 60000}}, "ms")
	write("subtitles", "pin", "Pin a subtitle file with a note.", "POST", "/files/{id}/pin", textFields("note"), "note")

	read("hub", "preferences", "Read your personal Hub layout and preferences.", "/prefs", "")
	space := objectSchema(textFields("id name color").strings("apps"), "id name apps")
	write("hub", "update_preferences", "Create/update personal Hub spaces, favorites or appearance. Supplied arrays replace those arrays; other preferences are preserved. Does not change server configuration.", "PUT", "/prefs", mcpFields{}.enum("theme", "system", "light", "dark").enum("density", "cozy", "compact").strings("order dock hidden").field("spaces", arraySchema(space)), "").Merge = true

	read("monitoring", "servers", "List the servers you can monitor and their current summaries.", "/servers", "")
	read("monitoring", "get_server", "Read one accessible server's status.", "/servers/{id}", "")
	read("monitoring", "snapshot", "Read current metrics for an accessible server.", "/servers/{id}/snapshot", "")
	read("monitoring", "alerts", "Read monitoring alerts visible to you.", "/alerts", "server_id status limit")
	read("backups", "status", "Backup health of the box in one call: healthy or warning, when the last good backup finished, failing jobs, stored size, and (for admins) the attention list with each problem in words.", "/widget", "")
	read("backups", "coverage", "Which apps (running Docker containers, grouped by compose project) are covered by a backup, skipped on purpose (with the reason) or not covered at all; status = uncovered, skipped or covered narrows the list. Admins only. Apps outside Docker are not checked.", "/coverage/summary", "status")
	read("projects", "summary", "Get your open, overdue and due-soon issues, in-progress counts, current cycles and projects that need attention in one call.", "/dashboard", "")
	read("projects", "teams", "List the teams you are on with their workflow states, labels and members (use their names/ids when creating or updating issues).", "/teams", "")
	read("projects", "issues", "Find issues. team = team key (WEB); status = open, done, started, backlog or triage; assignee_id = me or a numeric id; cycle = current or next; due = overdue, today, week or none; q = words or an identifier like WEB-12.", "/issues", "team status assignee_id priority project_id cycle due label_id q sort limit offset")
	read("projects", "get_issue", "Read one issue by numeric id with its sub-issues, relations, attachments, comments and history.", "/issues/{id}", "")
	issue := textFields("team title description state assignee due_date cycle parent_id").integers("priority project_id milestone_id").numbers("estimate").strings("labels")
	issue["team"].(map[string]any)["description"] = "Team key, e.g. WEB (from pocket_projects_teams). Required when you are on several teams."
	issue["state"].(map[string]any)["description"] = "A workflow state name of the team (e.g. In Progress) or a type: backlog, unstarted, started, completed, canceled."
	issue["assignee"].(map[string]any)["description"] = "me, none, an email or an @name of a team member."
	issue["priority"].(map[string]any)["description"] = "0 none, 1 urgent, 2 high, 3 medium, 4 low."
	issue["cycle"].(map[string]any)["description"] = "current, next or none."
	issue["labels"].(map[string]any)["description"] = "Label names; missing ones are created on the team."
	write("projects", "create_issue", "Create an issue in a team (it gets an identifier like WEB-13). Dates are YYYY-MM-DD.", "POST", "/issues", issue, "title")
	upd := textFields("title description state assignee due_date cycle parent_id").integers("priority project_id milestone_id").numbers("estimate").strings("labels").
		field("add_label_ids", arraySchema(map[string]any{"type": "integer"})).field("remove_label_ids", arraySchema(map[string]any{"type": "integer"}))
	write("projects", "update_issue", "Update supplied issue fields (title, description, state, priority, assignee, due date, project, cycle, labels); omitted fields stay as they are.", "PATCH", "/issues/{id}", upd, "")
	write("projects", "comment", "Comment on an issue in Markdown; @name mentions notify team members.", "POST", "/issues/{id}/comments", textFields("body"), "body")
	linkPR := textFields("url title branch repo").integers("number")
	linkPR["url"].(map[string]any)["description"] = "The pull request's address, e.g. https://github.com/owner/repo/pull/12."
	write("projects", "link_pr", "Link a pull request you opened to an issue (numeric id); Pocket Projects then tracks whether it is merged and completes the issue when it is.", "POST", "/issues/{id}/pull-requests", linkPR, "url")
	read("projects", "projects", "List projects you can see with status, health, progress, lead, start and target dates.", "/projects", "team_id status q")
	read("projects", "get_project", "Read a project with milestones, latest status updates, documents, resources and stakeholders.", "/projects/{id}", "")
	newProject := textFields("name summary content start_date target_date").enum("status", "backlog", "planned", "started", "paused", "completed", "canceled").
		field("team_ids", arraySchema(map[string]any{"type": "integer"})).strings("teams")
	newProject["teams"].(map[string]any)["description"] = "Team keys like [\"WEB\"]; required instead of team_ids once workspaces are connected."
	write("projects", "create_project", "Create a project for one or more of your teams.", "POST", "/projects", newProject, "name")
	update := textFields("body project").enum("health", "on_track", "at_risk", "off_track")
	update["project"].(map[string]any)["description"] = "The project's exact name; required once workspaces are connected (checked against the project in the workspace)."
	write("projects", "post_update", "Post a status update on a project; health is on_track, at_risk or off_track.", "POST", "/projects/{id}/updates", update, "body health")
	read("projects", "cycles", "List the cycles (sprints) of your teams with progress; status = current, upcoming or past.", "/cycles", "team_id status")
	read("projects", "calendar", "Dated items between from and to (YYYY-MM-DD): issue due dates, milestones, project starts and targets, cycles. scope = mine (default) or all.", "/calendar", "from to scope")
	read("projects", "search", "Search issues, projects, project documents and comments.", "/search", "q limit")
	remember := textFields("text")
	remember["text"].(map[string]any)["description"] = "One short factual line, e.g. \"Tests run with make test; the web app is in web/\"."
	write("projects", "remember", "Save one short durable fact about a team's code, conventions or environment to the team's AI memory (shown to future Pocket AI conversations for that team). Team = key like AS. No secrets.", "POST", "/teams/{team}/memory", remember, "text")
	read("projects", "shared_notes", "List the team's shared notes on an issue (numeric id): title, who wrote and changed them, updated_at. body = 1 includes their text. Everyone on the team sees them; the person's own notes are in Pocket Notes instead.", "/issues/{id}/shared-notes", "body")
	read("projects", "get_shared_note", "Read one shared note of an issue (issue id, note id) with its text and updated_at.", "/issues/{id}/shared-notes/{note}", "")
	write("projects", "create_shared_note", "Write a new shared note on an issue (numeric id) in Markdown; the whole team sees and can edit it. Only for what the person wants the team to have.", "POST", "/issues/{id}/shared-notes", textFields("title body"), "body")
	sharedEdit := textFields("title body base")
	sharedEdit["base"].(map[string]any)["description"] = "The note's updated_at as you last read it. If a teammate saved since, nothing is written and the error says so: read it again and merge."
	write("projects", "update_shared_note", "Change a shared note's title or text (issue id, note id); the text replaces the whole note.", "PATCH", "/issues/{id}/shared-notes/{note}", sharedEdit, "base")
	write("projects", "share_note", "Copy one of your own Pocket Notes (note = its id in Pocket Notes) onto the issue (numeric id) as a shared note for the team; sharing the same note again replaces that copy. The note itself stays private and unchanged.", "POST", "/issues/{id}/notes/{note}/share", nil, "")
	read("projects", "workspaces", "List your Pocket Projects workspaces (your own and the instances you connected) by address, with active = the one open to Pocket AI now (the person picks it by opening it in Pocket Projects; the others refuse every call). Pass the address as workspace to the other pocket_projects_* tools.", "/workspaces", "")
	read("projects", "inbox", "Read your Pocket Projects inbox: assignments, mentions, comments, status changes and reminders.", "/notifications", "filter limit")
	read("movement", "summary", "What a day or a range (at most 92 days) looked like: distance, journeys with transport modes, the places you stopped at in order, countries. date = YYYY-MM-DD, or from and to.", "/summary", "date from to")
	read("movement", "where", "Where you were at a given time: the visit or journey then and the nearest recorded position. at = local time like 2026-10-20T14:30 (or ISO 8601).", "/where", "at")
	read("movement", "now", "Your newest position: the place or journey it belongs to, each phone's last report and battery, and today so far.", "/now", "")
	read("movement", "day", "One day's timeline in order: visits (place, times, confirmed or suggested) and journeys (mode, distance, duration).", "/day/{date}", "")
	read("movement", "insights", "Report for a year (year = 2026) or a range (from, to as YYYY-MM-DD): distance, days tracked, months, transport modes, countries, cities, top places, records and time at home, work and elsewhere.", "/insights", "year from to")
	read("movement", "countries", "Every country you have been to, with first and last day and the years.", "/countries", "")
	read("movement", "places", "Your places with how often and how long you were there. kind = saved or discovered; q filters by name, address or city; sort = visits, time, recent or name.", "/places", "kind q sort visited limit")
	read("movement", "get_place", "Read one place with its visits and when you usually go there (by hour and weekday).", "/places/{id}", "")
	read("movement", "visits", "List visits between start_at and end_at (ISO 8601); status = suggested, confirmed or declined.", "/visits", "start_at end_at status place_id limit")
	read("movement", "search", "Find your places by name, address or city.", "/search", "q limit")
	visit := textFields("name notes").integers("place_id").enum("status", "suggested", "confirmed", "declined")
	visit["name"].(map[string]any)["description"] = "Names the visit's place, so every visit there shows it."
	write("movement", "update_visit", "Confirm, decline or rename a visit, or move it to another of your places (place_id).", "PATCH", "/visits/{id}", visit, "")
	place := textFields("name category notes address").numbers("lat lng radius").booleans("hidden")
	place["category"].(map[string]any)["description"] = "home, work, school, family, food, shop, sport, health, travel, leisure, nature or other."
	place["radius"].(map[string]any)["description"] = "Metres, 15 to 5000 (default 100)."
	write("movement", "create_place", "Save a place (home, work, the gym…) with a radius: every visit inside it, past ones too, gets its name and is confirmed.", "POST", "/places", place, "lat lng")
	write("movement", "update_place", "Rename, recategorise, move or resize one of your places; omitted fields stay as they are.", "PUT", "/places/{id}", place, "")
	read("search", "web", "Search the web privately through Pocket Search (SearXNG on the person's own server; their engines and site rules apply, nothing is added to their search history). terms = the search; cat = general (default), news, images, videos, it, science; time = day, week, month or year; page = 1-20. Results carry url, title, content (snippet), domain and published date.", "/search", "terms cat time page lang")
	read("search", "read_page", "Read a web page's article text through Pocket Search's reading view: fetched by the person's server, scripts, trackers and adverts removed. url = the page's http(s) address.", "/read", "url")
	read("pr-review", "find", "Find pull requests in Pocket PR Review by number (4205 or #4205), owner/repo#12, a GitHub pull request address, or words of the title, repository, author or branch — open, closed and archived. Returns id for the other pocket_pr-review tools.", "/pulls/find", "q")
	read("pr-review", "list", "List the pull requests in Pocket PR Review: waiting for your review, yours and others you added. state = open (default), closed, merged or all; reason = requested, mine or added; repo = owner/name.", "/pulls", "state reason repo limit")
	read("pr-review", "brief", "Read one pull request (id from pocket_pr-review_find) in one call, without diffs: title, description, branches, size, the walkthrough (one-sentence summary, before and after, a diagram of how data moves, steps in reading order with their files, risks, questions for the author) and every changed file with one sentence about it. Start here for any question about a pull request.", "/pulls/{id}/brief", "")
	read("pr-review", "diff", "Read the unified diff of one changed file of a pull request: path as pocket_pr-review_brief lists it (or just the file name when only one file has it). Large files are cut at 200 KB; search them with pocket_pr-review_grep.", "/pulls/{id}/diff", "path").TextResponse = true
	read("pr-review", "grep", "Search every changed line of a pull request's code (added, removed and the context around them) with file and line numbers. q = words that must all be on the line (any case), or a regular expression with regex = 1; path = part of a file path; side = added, removed or all. Use it to check whether a pull request adds or uses something, e.g. a feature flag (q = flag, or ENABLED), a migration, an endpoint or a setting.", "/pulls/{id}/grep", "q path side regex")
	read("pr-review", "status", "Where a pull request stands on GitHub now: CI checks (passed, failing, running, each with its link), whether it can merge (conflicts, blocked, how many commits behind its base), the review decision with who approved, asked for changes or is still asked, and unresolved conversations.", "/pulls/{id}/status", "")
	read("pr-review", "conversation", "Read the GitHub review threads on a pull request's lines (who said what, on which file and line).", "/pulls/{id}/threads", "")
	read("pr-review", "comments", "Read your own review comments on a pull request in Pocket PR Review (drafts not sent yet and sent ones).", "/pulls/{id}/comments", "")
	read("pr-review", "summary", "Get Pocket PR Review's overview in one call: pull requests waiting for your review, yours, and reviews you sent.", "/dashboard", "")
	read("ai", "threads", "Find your Pocket AI conversations.", "/threads", "q limit")
	read("ai", "conversation", "Read the recent messages in one of your Pocket AI conversations.", "/ask-context/{id}", "")
	read("ai", "schedules", "List your Pocket AI scheduled runs: id, name, the full prompt, timing (spec), machine (host_id, 0 = the Pocket AI server), enabled, next and last run, last status and summary.", "/schedules", "")
	schedule := textFields("name prompt spec").enum("notify", "always", "attention", "never").booleans("enabled")
	schedule["prompt"].(map[string]any)["description"] = "The complete new prompt; it replaces the old one, so send the whole edited text, not a fragment."
	schedule["spec"].(map[string]any)["description"] = "every 6h, daily 08:00, weekdays 08:30, weekly mon 09:00 or a cron line like 0 8 * * *."
	schedule["notify"].(map[string]any)["description"] = "When to notify after a run: always, attention (only when it needs a human) or never."
	newSchedule := textFields("name prompt spec cwd").enum("notify", "always", "attention", "never").booleans("enabled").
		enum("provider", "claude", "codex").enum("effort", "low", "medium", "high", "xhigh", "max").
		// no full-access modes (bypassPermissions, full-access): those are set in Pocket AI only
		enum("mode", "default", "acceptEdits", "plan", "auto", "untrusted", "on-request", "read-only").
		field("host_id", map[string]any{"type": "integer", "minimum": 0, "description": "The machine it runs on: 0 = the Pocket AI server (default), else a host_id as pocket_ai_schedules shows it."})
	newSchedule["prompt"].(map[string]any)["description"] = "Everything the unattended agent must do; it cannot ask anything back, and its final message is saved as the run's summary."
	newSchedule["spec"].(map[string]any)["description"] = schedule["spec"].(map[string]any)["description"]
	newSchedule["notify"].(map[string]any)["description"] = schedule["notify"].(map[string]any)["description"]
	newSchedule["cwd"].(map[string]any)["description"] = "The folder it runs in (absolute or ~/…; on another machine, that machine's folder)."
	newSchedule["mode"].(map[string]any)["description"] = "Permission mode: claude = default, acceptEdits, plan or auto; codex = untrusted, on-request or read-only. Full access is only set in Pocket AI itself."
	write("ai", "create_schedule", "Create a Pocket AI scheduled run: a prompt an agent runs by itself on a timing (spec), on this server or another machine (host_id), in a folder (cwd). enabled defaults to true and notify to attention. Read pocket_ai_schedules first to reuse an existing schedule's machine, folder and agent.", "POST", "/schedules", newSchedule, "name prompt spec")
	write("ai", "update_schedule", "Edit one of your Pocket AI scheduled runs (schedule = id from pocket_ai_schedules): name, prompt, timing, notify or enabled; omitted fields stay as they are. The agent, model, permission mode, machine and folder cannot be changed here.", "PUT", "/schedules/{schedule}", schedule, "")
	return out
}
