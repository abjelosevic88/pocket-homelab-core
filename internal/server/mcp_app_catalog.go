package server

import (
	"regexp"
	"strings"
)

// This is a reviewed capability catalog, not an HTTP proxy or an automatic
// export of app routes. No tool controls credentials, sharing, delivery,
// deletion, arbitrary HTTP methods, or service administration.
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
var mcpChangePosts = map[string]bool{"tasks_complete": true, "inventory_adjust": true, "bookshelf_progress": true, "files_save_text": true,
	"office_rename": true, "podcasts_favorite": true, "subtitles_nudge": true, "subtitles_pin": true}

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
			root["workspace"] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991),
				"description": "Connected workspace id from pocket_projects_workspaces: the call then goes to that other Pocket Projects instance, as you there. Omit for your own instance."}
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
	write("projects", "create_project", "Create a project for one or more of your teams.", "POST", "/projects",
		textFields("name summary content start_date target_date").enum("status", "backlog", "planned", "started", "paused", "completed", "canceled").field("team_ids", arraySchema(map[string]any{"type": "integer"})), "name")
	write("projects", "post_update", "Post a status update on a project; health is on_track, at_risk or off_track.", "POST", "/projects/{id}/updates", textFields("body").enum("health", "on_track", "at_risk", "off_track"), "body health")
	read("projects", "cycles", "List the cycles (sprints) of your teams with progress; status = current, upcoming or past.", "/cycles", "team_id status")
	read("projects", "calendar", "Dated items between from and to (YYYY-MM-DD): issue due dates, milestones, project starts and targets, cycles. scope = mine (default) or all.", "/calendar", "from to scope")
	read("projects", "search", "Search issues, projects, project documents and comments.", "/search", "q limit")
	remember := textFields("text")
	remember["text"].(map[string]any)["description"] = "One short factual line, e.g. \"Tests run with make test; the web app is in web/\"."
	write("projects", "remember", "Save one short durable fact about a team's code, conventions or environment to the team's AI memory (shown to future Pocket AI conversations for that team). Team = key like AS. No secrets.", "POST", "/teams/{team}/memory", remember, "text")
	read("projects", "workspaces", "List the other Pocket Projects instances you connected (Settings → Connected instances): id, name, url. Pass the id as workspace to the other pocket_projects_* tools to read and change issues there.", "/remotes", "")
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
	read("ai", "threads", "Find your Pocket AI conversations.", "/threads", "q limit")
	read("ai", "conversation", "Read the recent messages in one of your Pocket AI conversations.", "/ask-context/{id}", "")
	read("ai", "schedules", "List your Pocket AI scheduled runs: id, name, the full prompt, timing (spec), machine (host_id, 0 = the Pocket AI server), enabled, next and last run, last status and summary.", "/schedules", "")
	schedule := textFields("name prompt spec").enum("notify", "always", "attention", "never").booleans("enabled")
	schedule["prompt"].(map[string]any)["description"] = "The complete new prompt; it replaces the old one, so send the whole edited text, not a fragment."
	schedule["spec"].(map[string]any)["description"] = "every 6h, daily 08:00, weekdays 08:30, weekly mon 09:00 or a cron line like 0 8 * * *."
	schedule["notify"].(map[string]any)["description"] = "When to notify after a run: always, attention (only when it needs a human) or never."
	write("ai", "update_schedule", "Edit one of your Pocket AI scheduled runs (schedule = id from pocket_ai_schedules): name, prompt, timing, notify or enabled; omitted fields stay as they are. The agent, model, permission mode, machine and folder cannot be changed here.", "PUT", "/schedules/{schedule}", schedule, "")
	return out
}
