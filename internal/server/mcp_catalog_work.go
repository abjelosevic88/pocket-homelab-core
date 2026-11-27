package server

// MCP app catalog, work: review, tables, jobs, sign, projects, pr-review.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) reviewSummary() {
	b.read("review", "summary", "Get review dashboard counts and pending review work in one call.", "/dashboard", "")
}

func (b *mcpCatalogBuilder) tables() {
	// Pocket Tables: the person's own small databases, like Airtable. A base holds tables; a table has fields (columns, each with a type),
	// views (saved filters and sorts: grid, board, calendar, gallery) and rows. Rows come with named = their values as text by field name.
	b.read("tables", "summary", "Get the state of the person's tables: how many bases, tables and rows; the rows with a reminder date due today (due_today), in the next week (soon) and overdue (late), and the 10 rows changed last (recent). Each row: id, title, table_id, table, base, field and date (the date field and its day).", "/summary", "")
	b.read("tables", "today", "Read the Tables home page: the rows whose reminder date is today (today), overdue in the last 30 days (late) and in the next week (soon), each with id, title, table_id, table, base, field and date.", "/today", "")
	b.read("tables", "list", "List the person's bases, each with its tables: base id, name, icon, description, and per table id, name, icon and records (how many rows). Start here to find a table's id.", "/bases", "")
	b.read("tables", "table", "Read one table's layout (id = the table, from pocket_tables_list): the table, its base, fields (id, name, type text|longtext|number|currency|percent|rating|checkbox|date|select|multiselect|email|phone|url|link|attachment|pocket|lookup|rollup|formula|created|updated|autonumber, options such as select choices, primary = the row's title) and views (id, name, type, config with filters and sorts). Read it before adding a row, to know the field names and choices.", "/tables/{id}", "")
	rows := b.read("tables", "rows", "List one table's rows (id = the table): view = a view id (pocket_tables_table) to get only its rows in its order, q = words to find in any field, limit and offset. Each row: id, number, title, named (its values as text by field name — use these), values (the same keyed by field id), comments (how many), created_at, updated_at; plus total.", "/tables/{id}/records", "view q limit offset")
	rows.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)["view"] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": "A view id from pocket_tables_table; leave out for all rows."}
	rows.Query = map[string]string{"named": "1"}
	compileMCPAppTool(rows)
	b.read("tables", "get_row", "Read one row in full (id = the row): record (title, named = values as text by field name, values by field id), its table and fields, history (who changed what, comments) and made (tasks, notes, events made from it in other apps).", "/records/{id}", "")
	b.read("tables", "search", "Search every table: rows with the words in any field (items: id, title, table_id, table, base, the field that matched and a snippet) and tables whose name matches (tables).", "/search", "q limit")
	b.read("tables", "lookup", "Find the rows about one person: email = a row with that address in an email field; name = a row whose text field is exactly that name or that links that contact. For a contact from another app.", "/lookup", "email name")
	b.read("tables", "day", "Read one day in the person's tables: the rows with a date on that day (any date field), each with title, table, base, field and date.", "/day/{date}", "")
	row := mcpFields{"values": map[string]any{"type": "object", "minProperties": 1, "maxProperties": 200,
		"additionalProperties": map[string]any{"anyOf": []any{map[string]any{"type": "string", "maxLength": 30000}, map[string]any{"type": "number"}, map[string]any{"type": "boolean"},
			arraySchema(map[string]any{"anyOf": []any{map[string]any{"type": "string", "maxLength": 1000}, map[string]any{"type": "integer"}}})}},
		"description": "The row's values by field name (as pocket_tables_table lists them) or field id: text as text, numbers as numbers (\"12,50\" is read too), checkbox true/false, a date as YYYY-MM-DD (or YYYY-MM-DDTHH:MM), a select by its choice name (a new name becomes a new choice), a multiselect as a list of names, a link as a list of row ids in the other table. Worked-out fields (formula, lookup, rollup, created, autonumber) cannot be set."}}
	addRow := b.write("tables", "add_row", "Add a row to one of the person's tables (id = the table): values by field name. Read the table first (pocket_tables_table) for its field names, types and select choices. Returns the row with its id.", "POST", "/tables/{id}/records", row, "values")
	addRow.Body = map[string]any{"typecast": true}
}

func (b *mcpCatalogBuilder) jobs() {
	// Pocket Jobs: the person's job search. Jobs sit in stages whose kind is saved (not applied yet), applied (waiting to hear),
	// active (calls, interviews), offer, hired or closed; a job's status is open, hired or closed. Salaries: min_amount/max_amount
	// are in the currency's units (55000 = 55,000.00) per salary_period year|month|hour; salary_min/salary_max are the same in CENTS.
	// Interviews have kind phone|video|onsite|technical|take_home|panel|final|other and starts_at local YYYY-MM-DDTHH:MM (or a day).
	// Offers: base_amount per period, bonus_amount a year, yearly = base for a year + bonus (cents). Days are YYYY-MM-DD.
	b.read("jobs", "summary", "Get the job search at a glance: open jobs (saved, waiting = applied and not heard, talking, offers to decide), hired and closed, applications this week against the weekly goal, this month and in all, response_rate and interview_rate (% of applications from the last 90 days that got an answer / an interview), upcoming interviews and the next one, next steps due today and overdue, applications to follow up, saved jobs to apply for within a week.", "/summary", "")
	b.read("jobs", "today", "Read the Today page: interviews in the next two weeks, open offers with their decide_by day, the week day by day (applications sent), and lists of jobs: overdue, today and this week (next steps), follow_up (applied, no answer for a while), apply (saved jobs to apply for soon or wanted most), no_step, recent (hired lately); plus the summary.", "/today", "")
	b.read("jobs", "jobs", "List the person's jobs: status open|hired|closed|ended; kind = a stage kind saved|applied|active|offer|hired|closed; q searches roles, companies, contacts, places and notes; company; due overdue|today|week|none|any (the next step); starred=1; tag; source linkedin|company_site|job_board|referral|recruiter|network|other; work_mode onsite|hybrid|remote; sort stage|due|updated|created|applied|salary|excitement|fit. Each job: id, title (the role), company, stage_name, stage_kind, status, location, work_mode, min_amount/max_amount, currency, salary_period, applied_on, contact_name/email, next_action, next_due, interviews, next_interview, has_offer, excitement 0-5, fit_score (0-100 from the AI, -1 = not checked), overdue, stale (follow up).", "/jobs", "status kind q company due starred tag source work_mode sort limit offset")
	b.read("jobs", "get_job", "Read one job in full: the job (with the posting's text, notes, cover letter, fit_note), its timeline (activities, newest first), its interviews (with prep, questions, outcome, feedback), its offer (or null) and what it has in the person's other apps (made).", "/jobs/{id}", "")
	b.read("jobs", "interviews", "List interviews: when upcoming|past (all when left out), job = one job's id, from/to = local days YYYY-MM-DD. Each: id, job_id, job_title, company, kind, starts_at, minutes, location (an address or a call link), with_people, prep, questions [{q, a}], outcome good|okay|bad, feedback, thanks_sent, past.", "/interviews", "when job from to limit")
	b.read("jobs", "offers", "List offers, the ones still to decide first: job_id, job_title, company, base_amount per period, period, bonus_amount, yearly (cents), currency, equity, pto_days, benefits, start_date, decide_by, status open|negotiating|accepted|declined, notes, excitement.", "/offers", "")
	b.read("jobs", "companies", "List the companies the person applied to or saved jobs at: how many jobs (open, hired, closed), interviews, the people talked to and their emails, the roles, the last activity and the job ids. q filters by name, person or role.", "/companies", "q")
	b.read("jobs", "reports", "Get job-search reports for the last weeks (weeks, default 12): per week applications sent, interviews and answers; the funnel (applied, heard back, interviewed, offer, hired); per source and per work mode how many were sent, answered, led to interviews and offers; why jobs ended; the average days to an answer.", "/reports", "weeks")
	b.read("jobs", "search", "Search the person's jobs (roles, companies, contacts, places, notes); each job comes with its label and link.", "/search", "q limit")
	b.read("jobs", "day", "Read one day of the job search: applications sent that day, interviews, the timeline lines written (notes, calls, emails, stage moves, offers, hired) with their job, and the next steps due that day.", "/day/{date}", "")
}

func (b *mcpCatalogBuilder) jobsLookupAndWrites() {
	b.read("jobs", "lookup", "Find the jobs connected to one person (email, e.g. a recruiter) or one company: for a contact from another app. Give email, company or both.", "/lookup", "email company")
	job := textFields("title company location url posting currency apply_by stage contact_name contact_email notes next_action next_due note").
		numbers("min_amount max_amount").integers("excitement").
		enum("work_mode", "onsite", "hybrid", "remote").enum("employment", "full_time", "part_time", "contract", "freelance", "internship").
		enum("salary_period", "year", "month", "hour").enum("source", "linkedin", "company_site", "job_board", "referral", "recruiter", "network", "other")
	for _, k := range []string{"min_amount", "max_amount"} {
		job[k].(map[string]any)["minimum"] = 0
		job[k].(map[string]any)["description"] = "The salary in the currency's units (55000 = 55,000.00), not cents, per salary_period. Leave out when the posting does not say."
	}
	job["excitement"].(map[string]any)["minimum"] = 0
	job["excitement"].(map[string]any)["maximum"] = 5
	job["excitement"].(map[string]any)["description"] = "How much the person wants it, 0 to 5."
	job["currency"] = map[string]any{"type": "string", "pattern": `^[A-Za-z]{3}$`, "description": "Three letters (EUR, BAM, USD); leave out for the person's own currency."}
	for _, day := range []string{"apply_by", "next_due"} {
		job[day] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	}
	job["apply_by"].(map[string]any)["description"] = "The last day to apply, YYYY-MM-DD."
	job["next_due"].(map[string]any)["description"] = "The day the next step is due, YYYY-MM-DD."
	job["title"].(map[string]any)["description"] = "The role (\"Senior Go developer\")."
	job["url"].(map[string]any)["description"] = "The posting's address (https://…); the same address is not saved twice."
	job["posting"].(map[string]any)["description"] = "The posting's text, if the person shared it."
	job["stage"].(map[string]any)["description"] = "A stage by its name, or a kind (saved, applied, active, offer); leave out for Saved. A job saved into an applied stage counts as applied today."
	job["next_action"].(map[string]any)["description"] = "The next thing to do, in a few words (\"Tailor the CV and apply\")."
	job["note"].(map[string]any)["description"] = "A first line for the job's timeline: where it was found, what was said."
	b.write("jobs", "add_job", "Save a job to the person's search: the role, the company, where, the salary range, the posting's link or text, who to talk to, the next step and its day. It goes to Saved unless stage says otherwise. Returns the job with its id.", "POST", "/jobs", job, "title")
	jobLine := textFields("title body").enum("kind", "note", "call", "email", "step")
	jobLine["kind"].(map[string]any)["description"] = "note (default), call, email (one that was sent or came in) or step (something done)."
	b.write("jobs", "add_note", "Write a line on one job's timeline (id = the job): a note, a call or email that happened, or a step done, with a short title and what was said in body. It does not move the job or change its next step.", "POST", "/jobs/{id}/activities", jobLine, "")
	interview := textFields("location with_people prep").integers("minutes").
		enum("kind", "phone", "video", "onsite", "technical", "take_home", "panel", "final", "other")
	interview["date"] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "The day of the interview, YYYY-MM-DD."}
	interview["time"] = map[string]any{"type": "string", "pattern": `^([01]\d|2[0-3]):[0-5]\d$`, "description": "The local start time HH:MM; leave out for a whole day (a take-home task)."}
	interview["minutes"].(map[string]any)["minimum"] = 5
	interview["minutes"].(map[string]any)["maximum"] = 1440
	interview["location"].(map[string]any)["description"] = "An address, or the call link."
	interview["with_people"].(map[string]any)["description"] = "Who the person meets (names, roles)."
	b.write("jobs", "add_interview", "Add an interview to one job (id = the job): its kind, day and time, length, place or call link and who it is with. A saved or applied job moves on to the first active stage, and when the person has it on the interview also goes to Pocket Calendar. Returns the interview.", "POST", "/jobs/{id}/interviews", interview, "date")
}

func (b *mcpCatalogBuilder) sign() {
	// Pocket Sign: PDFs sent to people to sign (contracts, quotes, consent forms) and what others sent the person to sign.
	b.read("sign", "summary", "Get what needs attention about signing in ONE call: documents waiting for the person's own signature (to_sign, sent by someone on this box), documents waiting for others (waiting: who it waits for in next, progress done/total), drafts, recently completed and the latest activity, plus counts per status.", "/summary", "")
	b.read("sign", "overview", "Get the Pocket Sign home screen: to_sign (waiting for the person), waiting (sent, waiting for others), drafts, recent (completed) and activity (the history across documents), with counts.", "/overview", "")
	b.read("sign", "list", "List documents sent for signing (templates are separate). status: draft, pending (sent, waiting for people), completed, declined, expired, voided, ended (any finished one) or all. Each has its people (recipients with role signer/approver/viewer/cc and status draft/waiting/sent/opened/done/declined), progress, next (who it waits for) and sent_at/completed_at/expires_at.", "/documents", "status q limit offset")
	b.read("sign", "get", "Read one document: its people (with ids, status, when they opened and signed, and why someone declined), the boxes to fill in, the message, expiry and reminders, the signed PDF if done, and the AI summary when one was made.", "/documents/{id}", "")
	b.read("sign", "audit", "Read a document's history, oldest first: created, sent, emailed, opened, signed, approved, declined, reminded, completed, voided, with who, when and from which IP. This is what the signing certificate prints.", "/documents/{id}/audit", "")
	b.read("sign", "templates", "List the person's templates (reusable PDFs with people and boxes placed): each has roles (recipient_id, role_name like Client or Tenant, role, and a fixed name/email if any), how often it was used and its public link if switched on.", "/templates", "")
	b.read("sign", "to_sign", "List what others on this box sent the person to sign: waiting (their turn now) and done (already signed), with title, who sent it, role and expiry. The person signs in Pocket Sign under To sign.", "/inbox", "")
	b.read("sign", "search", "Search documents and templates by title, people's names or emails, and the words in the PDF.", "/search", "q")
	b.read("sign", "day", "Read one day of signing: documents sent, completed, signed by others (signed) and signed by the person (signed_by_me).", "/day/{date}", "")
	b.read("sign", "stats", "Get documents sent, completed and declined per month, the completion rate and the average hours from sending to the last signature.", "/stats", "months")
	// No tool sends a document out (that publishes it to other people, like /send and /share): use_template makes a draft the person sends in Pocket Sign.
	b.write("sign", "remind", "Email one person a reminder to sign now (only someone whose turn it is: status sent or opened). recipient_id comes from pocket_sign_get. Pocket Sign also reminds by itself every few days when the document says so.", "POST", "/documents/{id}/remind",
		mcpFields{}.integers("recipient_id"), "recipient_id")
	b.write("sign", "void", "Cancel a document that is still waiting for signatures. The links stop working and the people who had one are told by email. reason: a short plain reason they will see.", "POST", "/documents/{id}/void", textFields("reason"), "reason")
	b.write("sign", "use_template", "Make a DRAFT document from a template and fill in its people: people lists each role of the template (recipient_id from pocket_sign_templates roles) with the name and email of who takes it. Nothing is sent: give the person the draft's link (item_url) so they check it and press Send in Pocket Sign. title and message are optional (the template's own are used).", "POST", "/templates/{id}/use",
		textFields("title message").field("people", arraySchema(objectSchema(mcpFields{}.integers("recipient_id").field("name", map[string]any{"type": "string", "maxLength": 200}).field("email", map[string]any{"type": "string", "maxLength": 320}), "recipient_id email"))), "people")
}

func (b *mcpCatalogBuilder) review() {
	b.read("review", "list", "List reviews you participate in.", "/reviews", "q status role")
	b.read("review", "get", "Read a review and your role.", "/reviews/{id}/", "")
	b.read("review", "tasks", "Read tasks on a review.", "/reviews/{review_id}/tasks", "")
	b.read("review", "comments", "Read review comments.", "/reviews/{review_id}/comments", "")
	b.write("review", "create", "Create a review workspace without inviting participants.", "POST", "/reviews", textFields("title description due_date"), "title")
	b.write("review", "create_task", "Create a review task without assigning or messaging another person.", "POST", "/reviews/{review_id}/tasks", textFields("title notes due_date"), "title")

	b.write("review", "update_task", "Update a review task or mark it done, preserving omitted fields.", "PUT", "/reviews/{review_id}/tasks/{task_id}", textFields("title notes due_date").enum("status", "open", "done"), "")
}

func (b *mcpCatalogBuilder) projects() {
	b.read("projects", "summary", "Get your open, overdue and due-soon issues, in-progress counts, current cycles and projects that need attention in one call.", "/dashboard", "")
	b.read("projects", "teams", "List the teams you are on with their workflow states, labels and members (use their names/ids when creating or updating issues).", "/teams", "")
	b.read("projects", "issues", "Find issues. team = team key (WEB); status = open, done, started, backlog or triage; assignee_id = me or a numeric id; cycle = current or next; due = overdue, today, week or none; q = words or an identifier like WEB-12.", "/issues", "team status assignee_id priority project_id cycle due label_id q sort limit offset")
	b.read("projects", "get_issue", "Read one issue by numeric id with its sub-issues, relations, attachments, comments and history.", "/issues/{id}", "")
	b.read("projects", "attachment", "Open a file attached to an issue (id = the attachment's number: an attachment id from pocket_projects_get_issue, or N in a /api/v1/attachments/N/file link in a description or comment, e.g. a pasted screenshot). Pictures come back as an image you can look at (large ones scaled to 1568 px), text files as text; other kinds (PDF, archives) only by name, type and size.", "/attachments/{id}/file", "").FileResponse = true
	issue := textFields("team title description state assignee due_date cycle parent_id").integers("priority project_id milestone_id").numbers("estimate").strings("labels")
	issue["team"].(map[string]any)["description"] = "Team key, e.g. WEB (from pocket_projects_teams). Required when you are on several teams."
	issue["state"].(map[string]any)["description"] = "A workflow state name of the team (e.g. In Progress) or a type: backlog, unstarted, started, completed, canceled."
	issue["assignee"].(map[string]any)["description"] = "me, none, an email or an @name of a team member."
	issue["priority"].(map[string]any)["description"] = "0 none, 1 urgent, 2 high, 3 medium, 4 low."
	issue["cycle"].(map[string]any)["description"] = "current, next or none."
	issue["labels"].(map[string]any)["description"] = "Label names; missing ones are created on the team."
	b.write("projects", "create_issue", "Create an issue in a team (it gets an identifier like WEB-13). Dates are YYYY-MM-DD.", "POST", "/issues", issue, "title")
	upd := textFields("title description state assignee due_date cycle parent_id").integers("priority project_id milestone_id").numbers("estimate").strings("labels").
		field("add_label_ids", arraySchema(map[string]any{"type": "integer"})).field("remove_label_ids", arraySchema(map[string]any{"type": "integer"}))
	b.write("projects", "update_issue", "Update supplied issue fields (title, description, state, priority, assignee, due date, project, cycle, labels); omitted fields stay as they are.", "PATCH", "/issues/{id}", upd, "")
	b.write("projects", "comment", "Comment on an issue in Markdown; @name mentions notify team members.", "POST", "/issues/{id}/comments", textFields("body"), "body")
	linkPR := textFields("url title branch repo").integers("number")
	linkPR["url"].(map[string]any)["description"] = "The pull request's address, e.g. https://github.com/owner/repo/pull/12."
	b.write("projects", "link_pr", "Link a pull request you opened to an issue (numeric id); Pocket Projects then tracks whether it is merged and completes the issue when it is.", "POST", "/issues/{id}/pull-requests", linkPR, "url")
	relate := textFields("issue").enum("type", "related", "blocks", "blocked_by", "duplicate", "duplicated_by")
	relate["type"].(map[string]any)["description"] = "How the issue (id) stands to the other one: related, blocks (it blocks the other), blocked_by, duplicate (it duplicates the other, which closes it as Duplicate) or duplicated_by."
	relate["issue"].(map[string]any)["description"] = "The other issue's identifier, like WEB-14 (same workspace)."
	b.write("projects", "relate", "Link two issues so the link shows under both issues' relations: related, blocks/blocked_by or duplicate/duplicated_by. The issue (id) and the other (issue) are named by identifier. Answers with the issue's relations. Links can be removed only in Pocket Projects.", "POST", "/issues/{id}/relations", relate, "type issue")
	b.read("projects", "projects", "List projects you can see with status, health, progress, lead, start and target dates.", "/projects", "team_id status q")
	b.read("projects", "get_project", "Read a project with milestones, latest status updates, documents, resources and stakeholders.", "/projects/{id}", "")
	newProject := textFields("name summary content start_date target_date").enum("status", "backlog", "planned", "started", "blocked", "paused", "completed", "canceled").
		field("team_ids", arraySchema(map[string]any{"type": "integer"})).strings("teams")
	newProject["teams"].(map[string]any)["description"] = "Team keys like [\"WEB\"]; required instead of team_ids once workspaces are connected."
	b.write("projects", "create_project", "Create a project for one or more of your teams.", "POST", "/projects", newProject, "name")
	update := textFields("body project").enum("health", "on_track", "at_risk", "off_track")
	update["project"].(map[string]any)["description"] = "The project's exact name; required once workspaces are connected (checked against the project in the workspace)."
	b.write("projects", "post_update", "Post a status update on a project; health is on_track, at_risk or off_track.", "POST", "/projects/{id}/updates", update, "body health")
	b.read("projects", "cycles", "List the cycles (sprints) of your teams with progress; status = current, upcoming or past.", "/cycles", "team_id status")
	b.read("projects", "calendar", "Dated items between from and to (YYYY-MM-DD): issue due dates, milestones, project starts and targets, cycles. scope = mine (default) or all.", "/calendar", "from to scope")
	b.read("projects", "search", "Search issues, projects, project documents and comments.", "/search", "q limit")
	remember := textFields("text")
	remember["text"].(map[string]any)["description"] = "One short factual line, e.g. \"Tests run with make test; the web app is in web/\"."
	b.write("projects", "remember", "Save one short durable fact about a team's code, conventions or environment to the team's AI memory (shown to future Pocket AI conversations for that team). Team = key like AS. No secrets.", "POST", "/teams/{team}/memory", remember, "text")
	b.read("projects", "shared_notes", "List the team's shared notes on an issue (numeric id): title, who wrote and changed them, updated_at. body = 1 includes their text. Everyone on the team sees them; the person's own notes are in Pocket Notes instead.", "/issues/{id}/shared-notes", "body")
	b.read("projects", "get_shared_note", "Read one shared note of an issue (issue id, note id) with its text and updated_at.", "/issues/{id}/shared-notes/{note}", "")
	b.write("projects", "create_shared_note", "Write a new shared note on an issue (numeric id) in Markdown; the whole team sees and can edit it. Only for what the person wants the team to have.", "POST", "/issues/{id}/shared-notes", textFields("title body"), "body")
	sharedEdit := textFields("title body base")
	sharedEdit["base"].(map[string]any)["description"] = "The note's updated_at as you last read it. If a teammate saved since, nothing is written and the error says so: read it again and merge."
	b.write("projects", "update_shared_note", "Change a shared note's title or text (issue id, note id); the text replaces the whole note.", "PATCH", "/issues/{id}/shared-notes/{note}", sharedEdit, "base")
	b.write("projects", "share_note", "Copy one of your own Pocket Notes (note = its id in Pocket Notes) onto the issue (numeric id) as a shared note for the team; sharing the same note again replaces that copy. The note itself stays private and unchanged.", "POST", "/issues/{id}/notes/{note}/share", nil, "")
	b.read("projects", "workspaces", "List your Pocket Projects workspaces (your own and the instances you connected) by address, with active = the one open to Pocket AI now (the person picks it by opening it in Pocket Projects; the others refuse every call). Pass the address as workspace to the other pocket_projects_* tools.", "/workspaces", "")
	b.read("projects", "inbox", "Read your Pocket Projects inbox: assignments, mentions, comments, status changes and reminders.", "/notifications", "filter limit")
}

func (b *mcpCatalogBuilder) prReview() {
	b.read("pr-review", "find", "Find pull requests in Pocket PR Review by number (4205 or #4205), owner/repo#12, a GitHub pull request address, or words of the title, repository, author or branch — open, closed and archived. Returns id for the other pocket_pr-review tools.", "/pulls/find", "q")
	b.read("pr-review", "list", "List the pull requests in Pocket PR Review: waiting for your review, yours and others you added. state = open (default), closed, merged or all; reason = requested, mine or added; repo = owner/name.", "/pulls", "state reason repo limit")
	b.read("pr-review", "brief", "Read one pull request (id from pocket_pr-review_find) in one call, without diffs: title, description, branches, size, the walkthrough (one-sentence summary, before and after, a diagram of how data moves, steps in reading order with their files, risks, questions for the author) and every changed file with one sentence about it. Start here for any question about a pull request.", "/pulls/{id}/brief", "")
	b.read("pr-review", "diff", "Read the unified diff of one changed file of a pull request: path as pocket_pr-review_brief lists it (or just the file name when only one file has it). Large files are cut at 200 KB; search them with pocket_pr-review_grep.", "/pulls/{id}/diff", "path").TextResponse = true
	b.read("pr-review", "grep", "Search every changed line of a pull request's code (added, removed and the context around them) with file and line numbers. q = words that must all be on the line (any case), or a regular expression with regex = 1; path = part of a file path; side = added, removed or all. Use it to check whether a pull request adds or uses something, e.g. a feature flag (q = flag, or ENABLED), a migration, an endpoint or a setting.", "/pulls/{id}/grep", "q path side regex")
	b.read("pr-review", "status", "Where a pull request stands on GitHub now: CI checks (passed, failing, running, each with its link), whether it can merge (conflicts, blocked, how many commits behind its base), the review decision with who approved, asked for changes or is still asked, and unresolved conversations.", "/pulls/{id}/status", "")
	b.read("pr-review", "conversation", "Read the GitHub review threads on a pull request's lines (who said what, on which file and line).", "/pulls/{id}/threads", "")
	b.read("pr-review", "comments", "Read your own review comments on a pull request in Pocket PR Review (drafts not sent yet and sent ones).", "/pulls/{id}/comments", "")
	b.read("pr-review", "summary", "Get Pocket PR Review's overview in one call: pull requests waiting for your review, yours, and reviews you sent.", "/dashboard", "")
}
