package server

// MCP app catalog, the box, the assistant and the web: errors, vault, hub, monitoring, backups, search, ai, cameras, analytics.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) errors() {
	// Pocket Errors: the errors of the person's apps and websites (Sentry SDKs, Pocket apps through Core, this box's
	// container logs) grouped into issues: open, resolved (back when it happens again) or ignored. Times are UTC.
	// Code around a stack line and the hourly bars never reach the assistant: Omit.
	errStatus := map[string]any{"type": "string", "enum": []string{"open", "resolved", "ignored", "all"}, "description": "open (the default), resolved, ignored or all."}
	errTools := []*mcpAppTool{
		b.read("errors", "summary", "Get what is broken right now: open issues, how many are new today (new), how many came back after being resolved (regressed), errors in the last 24 hours (events_24h, by level per hour in hours), the newest open issues (latest: id, title, culprit = where in the code, project_name, level, times, people, last_seen) and which of this box's containers are read for errors (watch).", "/summary", ""),
		b.read("errors", "issues", "List issues (status = open, resolved, ignored or all; project = a project id from pocket_errors_projects; level = fatal, error or warning to get that level and worse; q = words in the title, place, project or note; since = a length like 24h or 7d for issues seen since then; sort = last, first, times or people): id, title (\"TypeError: x is undefined\"), type, culprit (the function/file or request it happened in), level, status, regressed (came back), times (ever), events_24h, people (how many of the app's users), first_seen, last_seen, first_release, last_release, environment, project_name, new (first seen in the last 24 hours), note. total is before the limit.", "/issues", "status project level q since sort limit offset"),
		b.read("errors", "get_issue", "Read one issue (id): everything pocket_errors_issues gives, its project (name, platform, container), Pocket AI's explanation when one was asked for (ai_text), links to what it became in other apps (a Pocket Projects issue, a task) and tags with how their values spread over the kept events (release, browser, os, environment…).", "/issues/{id}", ""),
		b.read("errors", "latest_event", "Read the newest time an issue happened (id = the issue), to see what went wrong: event.data has exceptions (type, value, frames outermost first with function, module, file, line, in_app = the app's own code; the failing frame is last), message, breadcrumbs (what happened before it), request (method, url, headers with secrets filtered), user, tags, contexts (browser, os, runtime), extra and log_lines (a container's lines around it). pos/total/older/newer say where it sits among the kept events.", "/issues/{id}/latest", ""),
		b.read("errors", "events", "List an issue's kept events, newest first (id = the issue; before = an event id to page on): id, at, level, message, environment, release, who (the app's user), url (the request).", "/issues/{id}/events", "before limit"),
		b.read("errors", "projects", "List the person's projects (each app, site, service or container that sends errors): id, name, platform, source (docker = read from a container's logs, else a Pocket app reporting through Core, empty = a DSN), container, open (open issues), events_24h, last_event_at, archived, and dsn (the Sentry DSN to give an app).", "/projects", ""),
		b.read("errors", "search", "Search issues and projects by words (results: type issue|project, id, title, subtitle, url).", "/search", "q"),
		b.read("errors", "day", "Read one day (date = YYYY-MM-DD, local): the issues first seen that day (new) and how many errors each project had (projects, events).", "/day/{date}", ""),
	}
	errQuery := func(op *mcpAppTool, name string, schema map[string]any) {
		op.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)[name] = schema
	}
	errQuery(errTools[1], "status", errStatus)
	errQuery(errTools[1], "level", map[string]any{"type": "string", "enum": []string{"fatal", "error", "warning"}})
	errQuery(errTools[1], "sort", map[string]any{"type": "string", "enum": []string{"last", "first", "times", "people"}})
	errQuery(errTools[1], "since", map[string]any{"type": "string", "pattern": `^\d{1,4}(h|m)$|^\d{1,3}d$`, "description": "How far back: 30m, 24h, 7d."})
	errQuery(errTools[1], "project", map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)})
	compileMCPAppTool(errTools[1])
	errUpd := mcpFields{
		"status": map[string]any{"type": "string", "enum": []string{"resolved", "ignored", "open"}, "description": "resolved = fixed (it comes back as open if it happens again), ignored = no more alerts, open = open again."},
		"for":    map[string]any{"type": "string", "enum": []string{"1h", "24h", "7d", "30d", ""}, "description": "Only with ignored: for how long; empty = for good."},
	}
	errTools = append(errTools, b.write("errors", "update_issue", "Resolve, ignore (for a while or for good) or reopen one of the person's issues (id). Ask before resolving or ignoring: it changes what they are told about. Returns the issue.", "POST", "/issues/{id}/status", errUpd, "status"))
	for _, op := range errTools {
		op.Omit = []string{"pre_context", "post_context", "hours"}
	}

	// Pocket Errors → Logs: every line this box's containers and services (the systemd journal: system and user units, the kernel)
	// wrote, for an admin of Errors (others get 403). Times are unix milliseconds; level = that level and worse.
	logSpan := map[string]any{"type": "string", "pattern": `^\d{1,4}(m|h|d)$`, "description": "How far back: 30m, 6h, 24h, 7d (default 24h)."}
	logLevel := map[string]any{"type": "string", "enum": []string{"debug", "info", "warning", "error", "fatal"}, "description": "This level and worse."}
	logTools := []*mcpAppTool{
		b.read("errors", "logs", "Search this box's log lines and those phones and devices send, newest first (an admin's): q = words that must all be in the line (\"a phrase\" in quotes, -word to leave out), source = a source id, container or unit name (pocket-notes, caddy, user:pocket-ai.service; comma separated), group = pocket|docker|system|user|kernel|device (phones and apps sending their own lines, e.g. source pocket-homelab-ios = the Pocket Homelab iPhone app), level = that level and worse, since = how far back (24h, 7d) or from/to (unix ms or RFC 3339), pattern = a pattern id from pocket_errors_log_patterns, before = a line id to page on, limit (max 1000). Lines: id, at (unix ms), source, source_label, group, level, msg, attrs (the fields it logged; Caddy requests have method, host, uri, status, ms). more = there are older ones.", "/logs", "q source group level since from to pattern before limit"),
		b.read("errors", "log_overview", "Get how much this box logged in a span (range = 1h, 24h, 7d…): totals by level and the same span before (previous), bytes written, the sources with their lines, errors and warnings, busiest first. Use it for \"what is noisy\", \"what is failing\" or \"did something change\".", "/logs/overview", "range"),
		b.read("errors", "log_patterns", "List the messages that repeat (values like numbers, ids and addresses taken out): sort = top (most lines), errors (warnings and worse) or new (first seen in the span: what changed); range, source, limit. Each: id (use as pattern in pocket_errors_logs), source_label, level, template, sample, n (in the span), share, first_at, last_at, new.", "/logs/patterns", "range sort source limit"),
		b.read("errors", "log_web", "Get the web traffic Caddy logged in a span (range): total requests, errors (5xx), client (4xx), slow (over a second), avg_ms and per site (host): n, ok, redirects, client_errors, server_errors, slow, avg_ms.", "/logs/web", "range"),
		b.read("errors", "log_sources", "List every log source (containers and journal units) with what is kept of it (keep = all, warn, error, count) and its lines, errors and warnings in the span (range).", "/logs/sources", "range"),
		b.read("errors", "log_context", "Read the lines its source wrote around one line (id from pocket_errors_logs; n before and after, default 25), oldest first.", "/logs/lines/{id}/context", "n"),
		b.read("errors", "issue_logs", "Read what an issue's container wrote two minutes either side of an event (id = the issue; event = an event id, default the newest): lines oldest first, hit = the line nearest the event, source.", "/issues/{id}/logs", "event"),
	}
	for _, name := range []string{"since"} {
		errQuery(logTools[0], name, logSpan)
	}
	errQuery(logTools[0], "level", logLevel)
	errQuery(logTools[0], "limit", map[string]any{"type": "integer", "minimum": 1, "maximum": 1000})
	errQuery(logTools[1], "range", logSpan)
	errQuery(logTools[2], "range", logSpan)
	errQuery(logTools[2], "sort", map[string]any{"type": "string", "enum": []string{"top", "errors", "new"}})
	errQuery(logTools[3], "range", logSpan)
	errQuery(logTools[4], "range", logSpan)
	for _, op := range logTools {
		compileMCPAppTool(op)
	}
	// the bars behind the charts never reach the assistant
	logTools[1].Omit = []string{"volume", "heat", "groups", "spark"}
	logTools[2].Omit = []string{"spark"}
	logTools[3].Omit = []string{"classes", "ms", "spark", "error_spark"}
	logTools[4].Omit = []string{"spark"}
}

func (b *mcpCatalogBuilder) vault() {
	b.read("vault", "overview", "Get the person's password vault at a glance: how many items of each kind, which apps are signed in and when they were last used, whether two-step login is on, recent sign-ins and wrong tries, and — only if the person shares them — the health numbers (weak, reused, old and leaked passwords, as counts, and a score). It never holds a name, a site, a username or a password; nothing can open the vault without the master password.", "/vault/overview", "")
}

func (b *mcpCatalogBuilder) hub() {
	b.read("hub", "preferences", "Read your personal Hub layout and preferences.", "/prefs", "")
	space := objectSchema(textFields("id name color").strings("apps"), "id name apps")
	b.write("hub", "update_preferences", "Create/update personal Hub spaces, favorites or appearance. Supplied arrays replace those arrays; other preferences are preserved. Does not change server configuration.", "PUT", "/prefs", mcpFields{}.enum("theme", "system", "light", "dark").enum("density", "cozy", "compact").strings("order dock hidden").field("spaces", arraySchema(space)), "").Merge = true
}

func (b *mcpCatalogBuilder) monitoring() {
	b.read("monitoring", "servers", "List the servers you can monitor and their current summaries.", "/servers", "")
	b.read("monitoring", "get_server", "Read one accessible server's status.", "/servers/{id}", "")
	b.read("monitoring", "snapshot", "Read current metrics for an accessible server.", "/servers/{id}/snapshot", "")
	b.read("monitoring", "alerts", "Read monitoring alerts visible to you.", "/alerts", "server_id status limit")
}

func (b *mcpCatalogBuilder) backups() {
	b.read("backups", "status", "Backup health of the box in one call: healthy or warning, when the last good backup finished, failing jobs, stored size, and (for admins) the attention list with each problem in words.", "/widget", "")
	b.read("backups", "coverage", "Which apps (running Docker containers, grouped by compose project) are covered by a backup, skipped on purpose (with the reason) or not covered at all; status = uncovered, skipped or covered narrows the list. Admins only. Apps outside Docker are not checked.", "/coverage/summary", "status")
}

func (b *mcpCatalogBuilder) search() {
	b.read("search", "web", "Search the web privately through Pocket Search (SearXNG on the person's own server; their engines and site rules apply, nothing is added to their search history). terms = the search; cat = general (default), news, images, videos, it, science; time = day, week, month or year; page = 1-20. Results carry url, title, content (snippet), domain and published date.", "/search", "terms cat time page lang")
	b.read("search", "read_page", "Read a web page's article text through Pocket Search's reading view: fetched by the person's server, scripts, trackers and adverts removed. url = the page's http(s) address.", "/read", "url")
}

func (b *mcpCatalogBuilder) ai() {
	b.read("ai", "threads", "Find your Pocket AI conversations.", "/threads", "q limit")
	b.read("ai", "conversation", "Read the recent messages in one of your Pocket AI conversations.", "/ask-context/{id}", "")
	b.read("ai", "schedules", "List your Pocket AI scheduled runs: id, name, the full prompt, timing (spec), machine (host_id, 0 = the Pocket AI server), enabled, next and last run, last status and summary.", "/schedules", "")
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
	b.write("ai", "create_schedule", "Create a Pocket AI scheduled run: a prompt an agent runs by itself on a timing (spec), on this server or another machine (host_id), in a folder (cwd). enabled defaults to true and notify to attention. Read pocket_ai_schedules first to reuse an existing schedule's machine, folder and agent.", "POST", "/schedules", newSchedule, "name prompt spec")
	b.write("ai", "update_schedule", "Edit one of your Pocket AI scheduled runs (schedule = id from pocket_ai_schedules): name, prompt, timing, notify or enabled; omitted fields stay as they are. The agent, model, permission mode, machine and folder cannot be changed here.", "PUT", "/schedules/{schedule}", schedule, "")
}

func (b *mcpCatalogBuilder) cameras() {
	// Pocket Cameras: the person's security cameras (their own and ones shared with them), what they saw (events: person,
	// vehicle, animal, parcel, a doorbell ring — with the AI's sentence and a picture) and the house's mode (home, away,
	// night, off), which decides which cameras alert. Times are UTC RFC 3339; days YYYY-MM-DD in the person's time zone.
	// Nothing here changes a camera's settings, rings the doorbell or deletes; set_mode is always asked.
	b.read("cameras", "summary", "Get Pocket Cameras at a glance: the mode (home, away, night, off) and why, each camera (online, recording, its last event), what was seen today (people, vehicles, parcels, animals, doorbell rings, alerts), how many events are not looked at yet, the latest events and a one-line text.", "/summary", "")
	b.read("cameras", "cameras", "List the person's cameras and the ones shared with them: name, room, kind, whether it is on, online and recording, what it alerts on (labels) and in which modes, mine (false = shared by owner) and its last event.", "/cameras", "")
	b.read("cameras", "camera", "Read one camera: its state now, what it alerts on and when, its zones, recordings kept (recorded_from/to), storage used and who it is shared with.", "/cameras/{id}", "")
	b.read("cameras", "events", "List what the cameras saw, newest first: camera = one camera's id; label = person|vehicle|animal|parcel|doorbell|motion; day = YYYY-MM-DD, or from/to (days, to included), or hours = the last N hours; unreviewed=1 only the ones not looked at; starred=1 the kept ones; q searches the AI's sentences, notes, pets and parcels. Each event: camera, title, summary (the AI's sentence), labels, worry (0 ordinary, 1 worth a look, 2 urgent), mode at the time, alerted, pet, parcel_text, weather and url.", "/events", "camera label day from to hours unreviewed starred q limit offset")
	b.read("cameras", "event", "Read one event in full: when, which camera, what was seen (labels, the AI's sentence, worry), the parcel it probably was, your pet, the weather, the note, and whether video was recorded around it.", "/events/{id}", "")
	b.read("cameras", "search", "Search the person's cameras (name, room) and events (the AI's sentences, notes, pets, parcels; or a word like person, parcels, delivery, vehicle, animal).", "/search", "q limit")
	b.read("cameras", "day", "Read one day in Pocket Cameras: every event (time, camera, what was seen, alerted or not) and the changes of mode (Away when everyone left, Night) — for the journal.", "/day/{date}", "")
	b.read("cameras", "stats", "The numbers over the last days (default 30): events a day by what they were (people, vehicles, animals, parcels, doorbell, false alarms, alerts), the busy hours of the week (Monday first × 24 hours), each camera's events and storage used.", "/stats", "days")
	b.read("cameras", "timeline", "One day of recordings: per camera the stretches of video kept (spans), its events, and the mode changes. day = YYYY-MM-DD (default today), camera = one camera's id.", "/timeline", "day camera")
	b.read("cameras", "mode", "Read the house's mode (home, away, night, off), who or what set it and why, whether it switches by itself (auto_mode) and what it would be now, the night hours and the latest changes.", "/mode", "")
}

func (b *mcpCatalogBuilder) analytics() {
	// Pocket Analytics: the person's websites and who visits them (no cookies; visitors are a daily hash, never a person).
	// Ranges: range = today | yesterday | 7d | 30d (default) | month | lastmonth | 12mo | year | all | custom (from/to YYYY-MM-DD);
	// a filter f = dim:value (country:BA, source:Google, page:/blog/*, device:Phone, goal:<id>). Visits one by one are not offered.
	// The only changes: adding a goal, a funnel or a note on the chart (each approved per call); nothing is edited or deleted here.
	b.read("analytics", "summary", "Get Pocket Analytics at a glance: each of the person's websites with visitors today, yesterday, the last 7 days and the 7 before (week, week_before), pages seen this week, the people on it now (live), its top sources and pages; total adds them up.", "/summary", "")
	b.read("analytics", "sites", "List the person's websites: id, domain, name, whether the script is installed (installed, last_hit), visitors today, in the last 30 days and the 30 before, people on the site now (live).", "/sites", "")
	b.read("analytics", "stats", "A website's numbers for a time range (id = a site from pocket_analytics_sites): totals (visitors, visits, pageviews, views_per_visit, bounce_rate = % who left after one page, duration = average visit in seconds, events), the same for the period before (previous), and the chart (series by hour/day/week/month, starts in Unix seconds). range as above; f = a filter; interval = hour|day|week|month.", "/sites/{id}/stats", "range from to interval f")
	b.read("analytics", "breakdown", "A website's top list for a range: dim = channel | source | referrer | utm_campaign | utm_source | utm_medium | page | entry | exit | country (ISO code) | region | city | language | device | browser | os | event | outbound (links followed out) | download (files) | click | contact (email and phone links) | form (forms sent) | search (site searches) | notfound (missing pages); or dim=prop with event and key (an event's property). Rows: value, visitors, visits, pageviews, events, bounce_rate, duration; pages also time_on_page (s) and scroll (% read). limit (default 10, at most 1000).", "/sites/{id}/breakdown", "dim range from to f limit offset event key")
	b.read("analytics", "goals", "How each of a website's goals did in a range: the goal (a page reached, an event, or a page read by a robot), visitors who reached it, conversions (how often) and rate (% of all visitors). A robot goal (robot: true, e.g. an install script fetched by curl = an install run) has only conversions: times, not people, and the filters do not apply.", "/sites/{id}/conversions", "range from to f")
	b.read("analytics", "live", "Who is on a website right now: now = visitors in the last 5 minutes, minutes = pages seen per minute over the last 30, the pages being read, where they came from, their countries, and the latest hits.", "/sites/{id}/live", "")
	b.read("analytics", "day", "Read one day in Pocket Analytics: visitors and pages seen per website that day — for the journal.", "/day/{date}", "")
	b.read("analytics", "search", "Search the person's websites (domain, name) and their pages seen in the last 30 days.", "/search", "q")
	b.read("analytics", "site", "Read one website's setup (id = a site from pocket_analytics_sites): its goals (id, name, kind page|event|robot, match) and funnels (id, name, steps) — the ids the goal filter (f=goal:<id>) and pocket_analytics_funnel need — plus what it counts.", "/sites/{id}", "")
	b.read("analytics", "funnel", "How a website's funnel did in a range (id = the site, fid = a funnel from pocket_analytics_site): per step the visitors who reached it in order, how many dropped after the step before, rate (% of the first step) and next (% of the step before). A robot step (robot: true, e.g. an install script fetched by curl) counts times, not people, and is not tied to a visit.", "/sites/{id}/funnels/{fid}/counts", "range from to f")
	b.read("analytics", "landing", "The pages visits start on (id = the site), with how each went: visits, visitors, bounce (% who saw only that page and did nothing), engaged (seconds per visit), pages per visit, converted and rate (% of those visits that went on to a goal; goal = one goal's id, else any; a page goal counts only after the first page).", "/sites/{id}/landing", "range from to f goal limit")
	b.read("analytics", "journeys", "The ways people take through a website: visits grouped by their first pages in order (steps = 2–6, default 3; a reload counts once), with visits, more (went on to more pages) and converted (reached a goal; goal = one goal's id). Filter by source (f=source:ChatGPT) to see the way people from one place take.", "/sites/{id}/journeys", "range from to f steps goal limit")
	b.read("analytics", "reading", "How far down a page people read (id = the site, page = a path, default the most viewed page that marks its parts): views, depth (% of measured views that got 25/50/75/100% down) and sections (the parts the site marks with data-pocket-section, with the % of views that had each on screen, in page order); marked = the pages that report parts.", "/sites/{id}/reading", "page range from to f")
	b.read("analytics", "ai", "What AI assistants do with a website: visits = people ChatGPT, Perplexity and others sent (sources), reads = pages their robots read (robots), and per page both (reads, visits). A page read often but never visited is known to the assistants but not recommended.", "/sites/{id}/ai", "range from to f limit")
	b.read("analytics", "searches", "What people searched for on a website: term, visitors, times, results (the fewest results it found; null when the site does not say), where (e.g. docs); nothing_found = searches with no results. empty=1 lists only the searches that found nothing (missing content).", "/sites/{id}/searches", "range from to f empty limit")
	b.read("analytics", "event_pages", "The pages one event happened on (event = its name, e.g. \"Copied the install line\"): page, visitors, events.", "/sites/{id}/event-pages", "event range from to f limit")
	b.read("analytics", "events", "The event names a website has seen lately; with event = a name, that event's property keys (for pocket_analytics_breakdown with dim=prop, event and key).", "/sites/{id}/events", "event")
	b.read("analytics", "speed", "How fast a website's pages were for visitors (Core Web Vitals, measured in their browsers): all and rows by dim = page | device | browser | os | country, each with loads and lcp (main content shown), inp (a press answered), cls (layout moved), ttfb (server answered), fcp as {p75, n} — three of four loads were this fast or faster; ms, cls a number. Good: lcp ≤ 2500, inp ≤ 200, cls ≤ 0.1, ttfb ≤ 800.", "/sites/{id}/speed", "dim range from to f limit")
	b.read("analytics", "when", "When people visit a website: visits by weekday and hour in the site's time zone, cells[7][24] (0 = Sunday), max, visits, week_start.", "/sites/{id}/when", "range from to f")
	b.read("analytics", "paths", "The way through one page (page = a path, * for any part): views, before (pages people were on just before; \"\" = they came in here) and after (where they went next; \"\" = they left).", "/sites/{id}/paths", "page range from to f limit")
	b.read("analytics", "notes", "Notes on a website's chart: what happened on a day (a newsletter, a redesign), as {day, text}; from/to = YYYY-MM-DD.", "/sites/{id}/notes", "from to")
	b.read("analytics", "keywords", "The words people searched on Google before they came to a website (from Google Search Console; two or three days behind): by = query (the words) or page (the pages Google sent them to); rows of words, clicks, impressions (times shown), ctr, position (average place, 1 = first); totals; state = ok | off (no Google key) | no_property | error.", "/sites/{id}/keywords", "by range from to f limit")
	b.read("analytics", "robots", "The robots that read a website (never counted as visitors): by = name (who), path (what they read) or kind; kind = ai,search,preview,seo,monitor,other (comma list; preview = someone shared a link in a chat); name = one robot. Rows: value, kind, hits (pages read), pages, last. \"Scripts and tools\" includes curl fetching an install script. Filters do not apply.", "/sites/{id}/robots", "by kind name range from to limit")
	b.read("analytics", "unnamed_robots", "The robots no rule names (\"Other robots\"), by what they call themselves: agent (their User-Agent; \"\" = none), hits, first, last (Unix seconds), path (the last page read).", "/sites/{id}/robots/unnamed", "range from to limit")
	goalKind := map[string]any{"type": "string", "enum": []string{"page", "event", "robot"}, "description": "page = a page reached (match a path, * for any part: /pricing, /blog/*); event = an event's name (\"Get the app\", \"File Download\"); robot = a page read by robots (curl fetching /install.sh), counted in times, not people."}
	b.write("analytics", "add_goal", "Add a goal to one of the person's websites (id = the site from pocket_analytics_sites): name, kind and match; robot = one robot's name for a robot goal (\"Scripts and tools\"), empty = any. Read pocket_analytics_site first so the same goal is not added twice. Returns the goal.", "POST", "/sites/{id}/goals",
		textFields("name match robot").field("kind", goalKind), "name kind match")
	step := objectSchema(textFields("match label robot").field("kind", goalKind), "kind match")
	b.write("analytics", "add_funnel", "Add a funnel to one of the person's websites (id = the site): a name and 2 to 8 steps in order, each a page or an event (kind, match, an optional label); only the last step may be a robot step. Read pocket_analytics_site first so the same funnel is not added twice. Returns the funnel.", "POST", "/sites/{id}/funnels",
		textFields("name").field("steps", arraySchema(step)), "name steps")
	b.write("analytics", "add_note", "Put a note on a website's chart (id = the site): what happened on a day (day = YYYY-MM-DD, text up to 200 characters), like a redesign, a launch or a tracking change. Returns the note.", "POST", "/sites/{id}/notes",
		textFields("day text"), "day text")
}

func (b *mcpCatalogBuilder) camerasWrites() {
	modeIn := textFields("reason").enum("mode", "home", "away", "night", "off")
	modeIn["mode"].(map[string]any)["description"] = "home = only cameras that alert at home do; away = the house is empty, most cameras alert; night = night watch; off = every alert paused except the doorbell (the cameras still record)."
	modeIn["reason"].(map[string]any)["description"] = "A few words on why, shown in the mode's history (\"leaving for the weekend\")."
	b.write("cameras", "set_mode", "Switch the house's mode (\"we are leaving\" → away, \"pause the cameras\" → off). Always ask first, naming the mode.", "PUT", "/mode", modeIn, "mode")
	reviewIn := textFields("before").integers("camera_id")
	reviewIn["camera_id"].(map[string]any)["description"] = "Only this camera's events (0 or left out = all of the person's own cameras)."
	reviewIn["before"].(map[string]any)["description"] = "Only events up to this time, RFC 3339 (left out = all)."
	b.write("cameras", "review_events", "Mark the person's events as looked at (they stop counting as new). Always ask first.", "POST", "/events/review", reviewIn, "")
	markIn := textFields("note").booleans("reviewed starred")
	markIn["starred"].(map[string]any)["description"] = "Keep this event (and its clip) forever, past the camera's keep time."
	markIn["note"].(map[string]any)["description"] = "A note on the event (replaces the old one)."
	b.write("cameras", "mark_event", "Mark one event of the person's own camera: looked at (reviewed), kept (starred) or a note. Always ask first.", "PUT", "/events/{id}", markIn, "")
}
