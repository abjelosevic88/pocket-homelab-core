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

var mcpPathParams = regexp.MustCompile(`\{([a-z_]+)\}`)

func buildMCPAppCatalog() []*mcpAppTool {
	out := []*mcpAppTool{}
	add := func(app, name, description, method, path string, fields mcpFields, required, query string) *mcpAppTool {
		root := mcpFields{}
		req := []string{}
		for _, p := range mcpPathParams.FindAllStringSubmatch(path, -1) {
			root[p[1]] = map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}
			if app == "ai" {
				root[p[1]] = map[string]any{"type": "string", "pattern": `^[a-f0-9-]{36}$`}
			}
			if app == "calendar" {
				root[p[1]] = map[string]any{"anyOf": []any{root[p[1]], map[string]any{"type": "string", "pattern": `^[1-9][0-9]{0,15}$`}}, "description": "Numeric event/series ID, not an occurrence ID."}
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
		op := &mcpAppTool{App: app, Name: "pocket_" + app + "_" + name, Description: description, Method: method, Path: path, Schema: objectSchema(root, strings.Join(req, " "))}
		out = append(out, compileMCPAppTool(op))
		return op
	}
	read := func(app, name, description, path, query string) *mcpAppTool {
		return add(app, name, description, "GET", path, nil, "", query)
	}
	write := func(app, name, description, method, path string, fields mcpFields, required string) *mcpAppTool {
		return add(app, name, description, method, path, fields, required, "")
	}

	read("notes", "list", "List your notes.", "/notes", "category tag sort limit since")
	read("notes", "search", "Search note titles and content.", "/search", "q limit")
	read("notes", "get", "Read a note's content and current etag.", "/notes/{id}", "")
	read("notes", "folders", "List note folders.", "/folders", "")
	note := textFields("title content category etag").booleans("favorite pinned")
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

	read("documents", "list", "Search document metadata and OCR matches.", "/documents", "q created_from created_to correspondent document_type limit offset")
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
	read("money", "transactions", "Search your financial transactions.", "/transactions", "q account kind from to category limit offset")
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

	read("collaborator", "reviews", "List reviews you participate in.", "/reviews", "q status role")
	read("collaborator", "get_review", "Read a review and your role.", "/reviews/{id}/", "")
	read("collaborator", "tasks", "Read tasks on a review.", "/reviews/{review_id}/tasks", "")
	read("collaborator", "comments", "Read review comments.", "/reviews/{review_id}/comments", "")
	write("collaborator", "create_review", "Create a review workspace without inviting participants.", "POST", "/reviews", textFields("title description due_date"), "title")
	write("collaborator", "create_task", "Create a review task without assigning or messaging another person.", "POST", "/reviews/{review_id}/tasks", textFields("title notes due_date"), "title")

	write("collaborator", "update_task", "Update a review task or mark it done, preserving omitted fields.", "PUT", "/reviews/{review_id}/tasks/{task_id}", textFields("title notes due_date").enum("status", "open", "done"), "")

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
	read("ai", "threads", "Find your Pocket AI conversations.", "/threads", "q limit")
	read("ai", "conversation", "Read the recent messages in one of your Pocket AI conversations.", "/ask-context/{id}", "")
	return out
}
