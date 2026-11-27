package server

// MCP app catalog, everyday apps: tasks, notes, calendar, habits, day, clips, contacts, parcels.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) tasksSummary() {
	b.read("tasks", "activity", "Read recent task activity (newest first), including task IDs and created/updated events. Useful for detecting newly created tasks.", "/activity", "task_id limit")
	b.read("tasks", "summary", "Get task counts, overdue/due work and next tasks in one call.", "/dashboard", "")
}

func (b *mcpCatalogBuilder) notes() {
	b.read("notes", "list", "List your notes.", "/notes", "category tag sort limit since")
	b.read("notes", "search", "Search note titles and content.", "/search", "q limit")
	b.read("notes", "get", "Read a note's content and current etag.", "/notes/{id}", "")
	b.read("notes", "folders", "List existing note folder paths. Use the exact folder name as category when creating or updating a note.", "/folders", "")
	note := textFields("title content category etag").booleans("favorite pinned")
	note["category"].(map[string]any)["description"] = "Folder path from pocket_notes_folders, e.g. Quick Notes. This is a name/path, not an ID."
	b.write("notes", "create", "Create a personal note.", "POST", "/notes", note, "title content")
	b.write("notes", "update", "Update only the supplied note fields; use its current etag to detect conflicting edits.", "PUT", "/notes/{id}", note, "etag")
	b.write("notes", "append", "Append text to a note without replacing its existing content.", "POST", "/notes/{id}/append", textFields("text"), "text")
}

func (b *mcpCatalogBuilder) tasks() {
	b.read("tasks", "lists", "List the task lists you can use.", "/lists", "")
	b.read("tasks", "list", "Find tasks, including due dates and completion status.", "/tasks", "q list_id status due_from due_to start_from start_to tag view limit offset")
	b.read("tasks", "get", "Read one task.", "/tasks/{id}", "")
	task := textFields("title notes start due location").integers("list_id priority estimate_min").strings("tags").booleans("someday")
	b.write("tasks", "create", "Create a task; dates use the application's local date/time format.", "POST", "/tasks", task, "title")
	b.write("tasks", "update", "Update supplied task fields, preserving omitted fields.", "PUT", "/tasks/{id}", task, "")
	b.write("tasks", "complete", "Mark a task done or reopen it. Completing a recurring task may create its next occurrence.", "POST", "/tasks/{id}/complete", mcpFields{}.booleans("done"), "done")
}

func (b *mcpCatalogBuilder) calendar() {
	b.read("calendar", "calendars", "List calendars and your access to them.", "/calendars", "")
	b.read("calendar", "events", "List events in a date range.", "/events", "from to calendars tz")
	b.read("calendar", "search", "Search calendar events by text.", "/search", "q tz")
	b.read("calendar", "get_event", "Read an event.", "/events/{id}", "")
	event := textFields("title description location start end tz rrule").integers("calendar_id").booleans("all_day busy").strings("categories")
	b.write("calendar", "create_event", "Create a calendar event without adding invitees. Specify timezone and start/end.", "POST", "/events", event, "calendar_id title start end")
	b.write("calendar", "update_event", "Update the supplied event fields. For a recurring event this edits the series; occurrence-specific edits are not exposed.", "PUT", "/events/{id}", event, "")
}

func (b *mcpCatalogBuilder) habits() {
	b.read("habits", "list", "List your habits and their IDs.", "/habits", "")
	b.read("habits", "today", "Read today's habits, trackers and journal.", "/today", "")
	b.read("habits", "day", "Read a day's habit check-ins.", "/days/{date}", "")
	b.write("habits", "mark", "Record a habit as done (1) or missed (-1) for a date.", "PUT", "/habits/{id}/entries/{date}", mcpFields{"status": map[string]any{"type": "integer", "enum": []int{1, -1}}}, "status")
	// Check-in creates a journal entry or updates existing text; it does not clear check-ins.
	b.write("habits", "journal", "Create or update journal text for a day.", "POST", "/checkin", textFields("date journal journal_bad important"), "date journal")
}

func (b *mcpCatalogBuilder) day() {
	b.read("day", "entries", "Search your journal entries.", "/entries", "q from to tag limit offset")
	b.read("day", "get", "Read a day and its journal.", "/days/{date}", "")
	b.read("day", "moments", "List your journal moments.", "/moments", "from to limit")
	b.write("day", "append", "Append a journal entry, optionally setting its highlight and mood (1–5).", "POST", "/entries", textFields("date highlight text tags").integers("mood"), "text")
	b.write("day", "update", "Update supplied journal fields for a day.", "PUT", "/days/{date}", textFields("highlight text tags").integers("mood").booleans("pinned"), "")
}

func (b *mcpCatalogBuilder) clips() {
	b.read("clips", "list", "Search your clipboard history.", "/clips", "q kind limit offset")
	b.read("clips", "get", "Read a saved clip.", "/clips/{id}", "")
	b.read("clips", "snippets", "List your reusable snippets.", "/snippets", "q folder")
	b.write("clips", "create", "Save a text clip.", "POST", "/clips", textFields("title content").enum("kind", "text"), "content")
	b.write("clips", "update", "Rename or pin a clip.", "PUT", "/clips/{id}", textFields("title").booleans("pinned"), "")
}

func (b *mcpCatalogBuilder) contacts() {
	b.read("contacts", "addressbooks", "List your address books.", "/addressbooks", "")
	b.read("contacts", "list", "Search contacts you can read.", "/contacts", "q category limit offset")
	b.read("contacts", "get", "Read a contact card.", "/contacts/{id}", "")
	contactField := objectSchema(textFields("label value"), "value")
	card := textFields("fn given family nickname org title birthday note").field("emails", arraySchema(contactField)).field("phones", arraySchema(contactField)).strings("categories")
	b.write("contacts", "create", "Create a contact in an accessible address book.", "POST", "/contacts", mcpFields{}.integers("addressbook_id").field("card", objectSchema(card, "fn")).booleans("starred"), "addressbook_id card")
	b.write("contacts", "label", "Star a contact or add categories without replacing its card.", "PATCH", "/contacts/{id}", mcpFields{}.booleans("starred").strings("add_categories"), "")
}

func (b *mcpCatalogBuilder) parcels() {
	// Pocket Parcels: deliveries on their way (in) and parcels sent (out), with each one's tracking timeline. Prices are minor units of currency.
	b.read("parcels", "summary", "Get what needs attention about parcels in ONE call: what arrives today or is out for delivery, what waits at a pick-up point, problems (failed attempts, customs, no news for days), what is on the way, return deadlines in the next 7 days and what was delivered today, plus how many shipping emails wait to be added.", "/summary", "")
	b.read("parcels", "overview", "Get the parcels home screen: needs_you (out for delivery, ready to pick up, expected today), on_the_way, problems, recent deliveries, returns and counts.", "/overview", "")
	b.read("parcels", "list", "List parcels. view: active (default, on their way), delivered, archived, returns (a return deadline that is still open), problems, all. Each has status (pending, info_received, in_transit, out_for_delivery, pickup, delivered, exception, returned, expired), status_text (latest step), location, expected_on/expected_until, carrier_name and tracking_url.", "/parcels", "view q shop carrier direction status limit offset")
	b.read("parcels", "get", "Read one parcel with its whole tracking timeline (events, newest first), papers and links to the payment, wish, inventory item and tasks in the other Pocket apps.", "/parcels/{id}", "")
	b.read("parcels", "search", "Search parcels by name, tracking number, shop, order number, recipient or latest step.", "/search", "q limit")
	b.read("parcels", "day", "Read one day of parcels: delivered, sent, added, expected that day, and every tracking step of the day.", "/day/{date}", "")
	b.read("parcels", "stats", "Get parcels per month, how many days deliveries took (overall, per shop and per carrier) and what orders and extra costs (customs, postage) came to per currency. Prices can overlap Pocket Money: a linked payment carries money_tx_id; never add Parcels and Money totals without verifying it.", "/stats", "months")
	parcel := func() mcpFields {
		f := textFields("tracking_number carrier title shop order_number ordered_on currency expected_on expected_until return_by notes contact_name").
			integers("price extra_cost").enum("direction", "in", "out")
		f["tracking_number"].(map[string]any)["description"] = "As printed; spaces are fine. The carrier is recognised from it when it can be (China Post, UPS, DHL, Cainiao…)."
		f["carrier"].(map[string]any)["description"] = "Carrier key when the number alone does not say: dhl-express, dhl-paket, ups, fedex, dpd, gls, hermes, amazon, postnl, inpost, cainiao, yunexpress, 4px, euroexpress, x-express, overseas, post-<country code> (post-ba, post-de…) or other."
		f["price"].(map[string]any)["description"] = "What the order cost, minor units of currency (4999 = 49.99)."
		f["direction"].(map[string]any)["description"] = "in = coming to you (default), out = a parcel you sent."
		return f
	}
	b.write("parcels", "add", "Add a parcel to follow: a tracking number, or at least a title for a delivery without one. Parcels then follows it (17TRACK with the person's key, or Cainiao for AliExpress parcels) and sends pushes when it is out for delivery, ready to pick up or delivered.", "POST", "/parcels", parcel(), "")
	b.write("parcels", "update", "Update supplied parcel fields (title, shop, expected day, return deadline, notes, archived…). return_state: keeping, returning (a task to send it back is added when the person wants that) or returned.", "PUT", "/parcels/{id}", parcel().booleans("archived").
		enum("status", "pending", "info_received", "in_transit", "out_for_delivery", "pickup", "delivered", "exception", "returned", "expired").enum("return_state", "", "keeping", "returning", "returned"), "")
	b.write("parcels", "add_step", "Add a step to a parcel's timeline (a call from the courier, picked up from the neighbour, customs charged). The status follows from the words unless given.", "POST", "/parcels/{id}/events",
		textFields("description location at").enum("status", "info_received", "in_transit", "out_for_delivery", "pickup", "delivered", "exception", "returned"), "description")
	b.write("parcels", "mark_delivered", "Mark a parcel delivered (it arrived). at: when, YYYY-MM-DD or a full time; send today's date when unsure.", "POST", "/parcels/{id}/delivered", textFields("at"), "")
	b.write("parcels", "check", "Ask the parcel's tracking service for news now (normally checked by itself every few hours).", "POST", "/parcels/{id}/check", nil, "")
}
