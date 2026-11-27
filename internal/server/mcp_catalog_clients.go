package server

// MCP app catalog, work with clients and the public: forms, crm, booking.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) forms() {
	// Pocket Forms: the person's forms (public pages anyone can answer without an account) and the answers people sent; answers go on to Contacts, Projects, Tasks, Calendar, Notes, Mail and Sign from inside Pocket Forms.
	b.read("forms", "summary", "Get the state of your forms: how many answers wait (new), this week, forms taking answers and drafts, the latest answers (who, which form, a one-line preview) and each form with its answers, new answers and how many opened it.", "/summary", "")
	b.read("forms", "forms", "List your forms: title, status draft|open|closed, taking (takes answers now) and why not (draft, closed, date = its last day passed, full = its answer limit reached), responses, new, views, close_at, max_answers, the public url people answer at.", "/forms", "status")
	b.read("forms", "get_form", "Read one form with its questions (fields: id, type, label, help, required, options, role = what the answer means: name, email, phone, company, website, subject, message, budget, deadline, date), its words after sending, limits and what it does by itself with each answer (auto).", "/forms/{id}", "")
	b.read("forms", "answers", "List the answers people sent, newest first (default: all but spam): form = a form id; status new|seen|done|spam|open (new and seen); starred=1; q searches the answers and names; email = one person's answers; day = YYYY-MM-DD. Each has name, email, preview, status, form_title, created_at, and what was made from it in other apps (made).", "/responses", "form status starred q email day limit offset")
	b.read("forms", "get_answer", "Read one answer in full: every question with its answer (items), who sent it, its status and your note, and what was made from it in your other apps.", "/responses/{id}", "")
	b.read("forms", "stats", "Numbers of one form: people who opened it and answers per day (last 30 days), totals, and per question the counted choices, average stars or numbers, and the latest written answers.", "/forms/{id}/stats", "")
	b.read("forms", "search", "Search your forms and the answers people sent (names, emails, every answer).", "/search", "q limit")
	b.read("forms", "day", "Read one day in your forms: the answers that came in (time, who, which form, a one-line preview) and forms opened that day.", "/day/{date}", "")
	b.read("forms", "templates", "List the ready-made forms a new form can start from (key, name, what it is for, its questions): project (a client brief), contact, quote, appointment, feedback, event (sign-ups), rsvp, survey, blank.", "/templates", "")
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
	b.write("forms", "create_form", "Make a form as a DRAFT for the person to check and open in Pocket Forms: nobody can answer it until they do. Start from a template (pocket_forms_templates) or give the questions; plain, friendly words in the person's language; ask only what is needed, and mark which question is the person's name and email (role) so answers can go on to their contacts. Returns the form with its url.", "POST", "/forms", formFields, "")
	b.write("forms", "open_form", "Open one of the person's forms so anyone with its link can answer (a form you made as a draft). Always ask first, naming the form.", "POST", "/forms/{id}/open", nil, "")
	b.write("forms", "close_form", "Close one of the person's forms: its page says it takes no more answers. Always ask first, naming the form.", "POST", "/forms/{id}/close", nil, "")
}

func (b *mcpCatalogBuilder) crm() {
	// Pocket CRM: the person's deals (possible work for clients) on a pipeline by stage, each with its next step, due day and timeline.
	// Money: every deal has value in CENTS (450000 = 4,500.00) and the same as amount in currency units (4500), with its currency.
	b.read("crm", "summary", "Get the state of your sales: open deals and their value, the weighted value (value × chance), steps due today and overdue, deals with no next step and gone quiet, won this month and this year with their value, lost this year, win rate, closing soon. Every *_value is {currency: cents}: 450000 is 4,500.00.", "/summary", "")
	b.read("crm", "today", "Read the Today page: deals whose next step is overdue, due today and this week, with no next step, gone quiet (stale), closing soon and won lately; plus the summary. Each deal has amount (currency units), value (cents), currency, next_action, next_due, stage_name, contact and company.", "/today", "")
	b.read("crm", "deals", "List your deals: status open|won|lost|closed; stage = a stage id (pocket_crm_stages); q searches titles, people, companies and notes; due overdue|today|week|none|any (the next step); company; email = one person's deals; starred=1; tag; source; sort stage|due|value|updated|created|close|expected. Each deal: title, stage_name, status, amount in currency units and value in cents, currency, chance, weighted (cents), contact_name/email/phone, company, next_action, next_due, expected_close, overdue, stale, days_in_stage.", "/deals", "status stage q due company email starred tag source sort limit offset")
	b.read("crm", "get_deal", "Read one deal in full: the deal (amount in currency units, value in cents), its timeline (activities: notes, calls, meetings, emails, steps done, stage moves, newest first) and what it has in your other apps (made: contacts, tasks, events, invoices, projects, signings).", "/deals/{id}", "")
	b.read("crm", "search", "Search your deals (titles, people, emails, companies, notes); each deal comes with its link.", "/search", "q limit")
	b.read("crm", "people", "List who you sell to: deals grouped by company (else by person) with email, phone, how many deals are open, won and lost, open_value and won_value as {currency: cents}, the last activity and the deal ids. q filters by name or email.", "/people", "q")
	b.read("crm", "lookup", "Find the deals with one person (email) or company: for a contact from another app. Give email, company or both.", "/lookup", "email company")
	b.read("crm", "day", "Read one day in your sales: the timeline lines written that day (notes, calls, meetings, steps done, deals added, moved, won and lost, each with its deal) and the deals whose next step is due that day.", "/day/{date}", "")
	b.read("crm", "reports", "Get sales reports for the last months (default 12, up to 36): per month deals added, won, lost and won value (cents, the person's currency); the open pipeline per stage (funnel), why deals were lost, where deals came from (sources), days to win and the average won deal.", "/reports", "months")
	b.read("crm", "stages", "List the pipeline's stages in order: id, name, kind open|won|lost, probability (% chance a deal there is won), how many deals and their value (cents).", "/stages", "")
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
	b.write("crm", "add_lead", "Add a deal (possible work for a client) to the person's pipeline: a short title (\"Website for Bistro Sara\"), who it is with and the company, what it is worth (amount, in currency units), the next step and its day. It goes to the first stage unless stage names another. When the person has it on, Pocket CRM also puts the next step in Pocket Tasks. Returns the deal with its id.", "POST", "/deals", lead, "")
	activity := textFields("title body").enum("kind", "note", "call", "meeting", "email", "step")
	activity["kind"].(map[string]any)["description"] = "note (default), call, meeting, email (one that was sent or came in) or step (something done)."
	b.write("crm", "add_note", "Write a line on one deal's timeline (id = the deal): a note, or a call, meeting or email that happened, or a step done, with a short title and what was said in body. It does not move the deal or change its next step.", "POST", "/deals/{id}/activities", activity, "")
}

func (b *mcpCatalogBuilder) booking() {
	// Pocket Booking: the person's booking pages (Calendly-style: people pick a free time and answer a few questions) and the
	// bookings made on them. Times are RFC 3339 with an offset; date/time/end_time are in the person's own time zone. Money is in cents.
	// book, cancel and move may email the guest (tell_guest), confirm emails them and an invite is a link for one named person: always ask.
	b.read("booking", "summary", "Get Pocket Booking at a glance: bookings coming up, today and this week, how many wait for the person to confirm (pending), open booking pages, the next booking, today's list and the waiting ones, with a one-line summary.", "/summary", "")
	b.read("booking", "list", "List bookings: view upcoming (default) | pending (waiting for the person to confirm) | past | cancelled | all; from/to = days YYYY-MM-DD in the person's time zone (to included); q searches names, emails, answers and titles; email = one guest's bookings (exact, any case); type_id = one booking page. Each booking: title, status pending|confirmed|declined|cancelled|done|no_show, start, end, date, time, minutes, the guest (name, email, phone), their answers, where (location_kind, location, meeting_url), price in cents, source (page, invite, owner or the app that booked) and url.", "/bookings", "view from to q email type_id limit offset")
	b.read("booking", "get", "Read one booking in full: who, when (start, end, the guest's time zone), the answers to the page's questions, where or the video link, price and paid, the person's note, why it was cancelled, and what was made from it in the person's other apps (made: calendar event, contact, task…).", "/bookings/{id}", "")
	b.read("booking", "types", "List the person's booking pages (types): title, status draft|open|paused, lengths in minutes (durations), where it happens (location_kind video|link|phone|phone_in|place|ask), price in cents, confirm (auto or by the person), capacity, invite_only, how many bookings are coming up, the public url people book at and link (the person's own page). Use an id here for slots, book and invite.", "/types", "")
	b.read("booking", "get_type", "Read one booking page with its questions, its limits (notice, how far ahead, buffers, per day) and its weekly hours.", "/types/{id}", "")
	b.read("booking", "slots", "The free times on one booking page (id = the page from pocket_booking_types), day by day: from = the first day YYYY-MM-DD (default today), days (default 7, at most 62), minutes = one of the page's lengths, tz = the time zone to show them in (default the person's). Each slot has start, end and seats left. Use it before pocket_booking_book or to tell someone when the person is free.", "/types/{id}/slots", "from days minutes tz")
	b.read("booking", "day", "Read one day in Pocket Booking: the bookings that take place that day (time, who, which page, status, where) and the bookings people made that day — for the journal and the day's plan.", "/day/{date}", "")
	b.read("booking", "search", "Search the person's bookings (guest names, emails, answers, notes) and booking pages.", "/search", "q limit")
	b.read("booking", "lookup", "Everything with one guest (email): their bookings newest first, the ones coming up, how many, the last and the next — for a contact from another app.", "/lookup", "email")
	b.read("booking", "invites", "List the personal booking links the person made (each books once, with the guest's name and email filled in): for whom, which page, the url, whether it was used (and the booking it made) and when it runs out.", "/invites", "")
	bookIn := textFields("start name email phone note client_key").integers("type_id minutes").booleans("tell_guest force")
	bookIn["type_id"].(map[string]any)["description"] = "The booking page (pocket_booking_types)."
	bookIn["start"].(map[string]any)["description"] = "When it starts, RFC 3339 with the offset, e.g. 2026-11-18T10:00:00+01:00 — a start from pocket_booking_slots."
	bookIn["minutes"].(map[string]any)["description"] = "How long, one of the page's lengths (default its first)."
	bookIn["note"].(map[string]any)["description"] = "What it is about, in the guest's or the person's words."
	bookIn["tell_guest"].(map[string]any)["description"] = "Email the guest the confirmation with the time and how to join (default true). Needs an email."
	bookIn["force"].(map[string]any)["description"] = "Book it even when the time is not free. Only when the person said so."
	bookIn["client_key"].(map[string]any)["description"] = "Optional: the same key answers the booking made the first time (e.g. crm:deal:12)."
	b.write("booking", "book", "Book someone in on one of the person's booking pages at a free time (from pocket_booking_slots): it lands in Pocket Calendar like any booking, and the guest is emailed unless tell_guest is false. A time that is taken is refused unless force. Always ask first, naming who, which page and when.", "POST", "/bookings", bookIn, "type_id start name")
	b.write("booking", "confirm", "Confirm a booking that waits for the person (status pending): it becomes confirmed and the guest is told. Always ask first, naming who and when.", "POST", "/bookings/{id}/confirm", nil, "")
	cancelIn := textFields("message").booleans("tell_guest")
	cancelIn["message"].(map[string]any)["description"] = "A few words to the guest about why (shown in the email)."
	cancelIn["tell_guest"].(map[string]any)["description"] = "Email the guest that it is cancelled (default true)."
	b.write("booking", "cancel", "Cancel one of the person's bookings; the time is free again and the guest is emailed unless tell_guest is false. Always ask first, naming who and when.", "POST", "/bookings/{id}/cancel", cancelIn, "")
	moveIn := textFields("start").integers("minutes").booleans("tell_guest")
	moveIn["start"].(map[string]any)["description"] = "The new start, RFC 3339 with the offset (a free time from pocket_booking_slots)."
	moveIn["tell_guest"].(map[string]any)["description"] = "Email the guest the new time (default true)."
	b.write("booking", "move", "Move one of the person's bookings to another time; the guest is emailed the new time unless tell_guest is false. Always ask first, naming who, the old and the new time.", "POST", "/bookings/{id}/move", moveIn, "start")
	inviteIn := textFields("name email note").integers("type_id days")
	inviteIn["type_id"].(map[string]any)["description"] = "The booking page (pocket_booking_types); 0 or left out = the person's first open page."
	inviteIn["days"].(map[string]any)["description"] = "How many days the link works (default 14)."
	inviteIn["note"].(map[string]any)["description"] = "A few words the guest sees on the page."
	b.write("booking", "invite", "Make a personal booking link for one person (\"send Ana a link to book a call\"): it books once, with their name and email filled in. Returns the url to give them (for example in an email the person approves). Always ask first, naming who and which page.", "POST", "/invites", inviteIn, "name")
	typeIn := textFields("title description").integers("minutes").enum("location_kind", "video", "link", "phone", "phone_in", "place", "ask")
	typeIn["minutes"].(map[string]any)["description"] = "How long a booking is, in minutes (e.g. 30)."
	typeIn["location_kind"].(map[string]any)["description"] = "video = a video room made for each booking (default), link = the person's own video link, phone = the person calls, phone_in = the guest calls, place = an address, ask = the guest says where."
	b.write("booking", "create_type", "Make a booking page as a DRAFT for the person to check and open in Pocket Booking: nobody can book on it until they do. Plain, friendly words in the person's language. Returns the page with its link.", "POST", "/types", typeIn, "title")
}
