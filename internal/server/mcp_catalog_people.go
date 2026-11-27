package server

// MCP app catalog, people: league, forum, capsule, family, chat.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) league() {
	// Pocket League: sport with friends. Groups per sport (tennis, 5-a-side, padel…), games (planned at a time, or voting on times),
	// each person's answer in|maybe|out (past the player limit "in" becomes waiting), results, a table with ratings, a chat.
	// Reads only, plus answering for the person themself: the AI never writes in the chat, enters or confirms results, plans or cancels games.
	b.read("league", "summary", "Get the person's sport in one call: the next planned game (when, where, which group, how many are in of the minimum, the person's own answer), their groups with unread chat counts, how many games are coming and this month's played and won.", "/summary", "")
	b.read("league", "today", "Read the League home page: the next games (next), the coming games the person has not answered yet (unanswered), games still voting on a time (voting), results others entered that wait for the person to confirm (confirm) and the person's last results (recent, W/D/L as won); unread chat messages and groups counted.", "/today", "")
	b.read("league", "upcoming", "List the coming games in all the person's groups, soonest first: id, group_name, sport, status planned|voting, starts_at (local time; empty while voting, then vote_until), venue, min/max players, in/maybe/out/waiting counts, my_answer (the person's own: in, maybe, out, waiting or empty = not answered), cost_cents and currency, comments.", "/upcoming", "")
	b.read("league", "games", "List games: group = a group id; upcoming=1 or past=1; from and to (YYYY-MM-DD or YYYY-MM-DDTHH:MM); limit. Same fields as pocket_league_upcoming, cancelled games too (cancel_reason).", "/games", "group upcoming past from to limit")
	b.read("league", "get_game", "Read one game in full: the game (when, where, status, counts, my_answer), its group, people (each with their answer in|maybe|out|waiting or none yet, note, side and rating), the times to vote on (options) while voting, its results (matches), player-of-the-game votes and each player's share of the cost (share_cost, cents).", "/games/{id}", "")
	b.read("league", "groups", "List the person's groups: id, name, sport (sport_label), venue, usual weekday and time, members, unread chat count and the next planned game (next_game). archived=1 includes archived groups.", "/groups", "archived")
	b.read("league", "get_group", "Read one group: the group, its members (name, role organiser|player) and its coming games (upcoming).", "/groups/{id}", "")
	b.read("league", "table", "Read a group's league table: rows in position order with name, played, won, drawn, lost, points, for/against (games for racket sports, with sets_won/sets_lost), rating (Elo-like, starts at 1000) and its last change, form (the last five, W/D/L, oldest first), streak and player-of-the-game count (mvp). period = all (default), year, season, month or 90d.", "/groups/{id}/table", "period")
	b.read("league", "player", "Read one player in a group (id = the group, uid = the player's user_id from the table or members): their line in the table, rating over time, head-to-head against each other player and their last matches.", "/groups/{id}/players/{uid}", "")
	b.read("league", "results", "List results (matches), newest first: group = a group id, game = a game id, player = a user id, from and to (YYYY-MM-DD), limit. Each has the match (played_at, players with side a|b or place, score, text like \"6–4 4–6 7–5\", winner a|b|draw, state confirmed|waiting|disputed, names by user id), group_name and emoji.", "/matches", "group game player from to limit")
	b.read("league", "search", "Search the person's groups, players, games (title, venue, notes) and chat lines; each hit has kind group|player|game|message, title, subtitle and its link.", "/search", "q limit")
	b.read("league", "lookup", "Find whether someone (email) plays with the person, in which groups, and their record against each other there (played, won, drawn, lost, together, last played): for a contact from another app.", "/lookup", "email")
	b.read("league", "day", "Read one day in the person's sport: the games that day (time, group, venue, status, my_answer, how many in) and the results they played (text, outcome W/D/L).", "/day/{date}", "")
	// Tournaments (a cup, an Americano evening, a ladder…): reads only. Entering or confirming results, signing up or dropping out stay in the app.
	tournaments := b.read("league", "tournaments", "List the tournaments in the person's groups, the ones being played first (status live), then open for sign-up (signup), then finished or cancelled: id, name, format (knockout, double_elim, round_robin, groups_knockout, swiss, americano, mexicano, kotc, team_ties, ranking = scored rounds like golf, ladder, box_league, arena) with format_name in words, status, entries (how many are in) and entry_kind player|pair|team, starts_at and ends_at (local time), venue_name, capacity, signup_until, group_id, group_name, sport, emoji, rated (counts in the group's table), winner (the winner's name once finished) and mine (the person plays in it). group_id = only that group's tournaments. For who leads, the draw or the next match, read the tournament with pocket_league_get_tournament.", "/tournaments", "")
	tournaments.Within = "/groups/{group_id}/tournaments"
	tournaments.Schema["properties"].(mcpFields)["group_id"] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": "A group id (pocket_league_groups): only that group's tournaments. Leave out for all of them."}
	compileMCPAppTool(tournaments)
	b.read("league", "get_tournament", "Read one tournament in full (id = the tournament). tournament: name, format, status signup|live|finished|cancelled, starts_at, venue, winner. entries: each entry (a player, pair or team) with id, name, status in|withdrawn|disqualified, seed, rating, members (user_id; names maps user ids to names). mine: the person's own entry ids. stages in order (id, kind, name, status live|done; groups then knockout has a groups stage, then the knockout): tables = standings per pool, keyed by the pool id in groups (rows in rank order: id = the entry, rank, played, won, drawn, lost, points, for/against, sets, decided_by = the tie-break that split them), or leaderboard for scored rounds (golf, bowling: rank, total, gross, rounds). matches: id, stage_id, round, label (\"Final\", \"Semi-final 1\"…), a_entry and b_entry (entry ids; sides.a/sides.b for formats where partners change, like Americano), court_id (courts), scheduled_at, status pending (waiting for who plays) | ready | called (to a court now) | reported (waiting for the other side to confirm) | done | void, winner a|b|draw, score and a_total/b_total (points, Americano). podium once done: rank 1-3 with entry_id. A ladder also has challenges (challenger, defender, status, play_by). For \"who is leading\" read the table or leaderboard of the live stage; for \"my next match\" the first match not done with one of mine as a side; for \"who won\" the podium or tournament.winner.", "/tournaments/{id}", "")
	answer := mcpFields{}.enum("answer", "in", "maybe", "out", "")
	answer["answer"].(map[string]any)["description"] = "in, maybe or out; an empty string takes the answer back. When the game is full, in puts the person on the waiting list (the reply's result says waiting)."
	answer["note"] = map[string]any{"type": "string", "maxLength": 120, "description": "A few words next to the answer that the group sees (\"10 min late\"); leave out when the person said none."}
	b.write("league", "answer", "Answer for the person (only themself) whether they are coming to a planned game: in, maybe or out (id = the game). The group sees it and, when a place frees up, the first one waiting moves up. A game still voting on times or cancelled refuses. Always ask first, naming the game and its day. Returns the game with result = what the answer became (in or waiting).", "POST", "/games/{id}/answer", answer, "answer")
}

func (b *mcpCatalogBuilder) forum() {
	b.read("forum", "summary", "Get Pocket Forum at a glance: how many forums the person is in, topics with unread posts, new topics, unread notifications, what waits for review (as a moderator), and the latest topics, events coming up and open questions with their links.", "/summary", "")
	b.read("forum", "forums", "List the forums the person is in (slug, name, visibility private/box/public, their role, unread and new counts, members, topics) and the ones they could join.", "/forums", "")
	b.read("forum", "forum", "Read one forum by its slug or id: about, rules, categories with their numbers, the people who run it, its numbers this week and the person's membership.", "/forums/{id}", "")
	b.read("forum", "topics", "List topics with the person's read state (unread = new posts since they read it, is_new = never opened). forum = a slug (empty = every forum they are in); category = a slug; filter: latest, unread, new, top (period week/month/year/all), unsolved (open questions), solved, noreplies, mine, posted, events; tag; kind (discussion, question, announcement, event).", "/topics", "forum category tag filter kind period page limit")
	b.read("forum", "topic", "Read one topic with all its posts in order (author, number, Markdown body, reactions, the accepted answer as solved_post_id, polls with their counts), its category and tags, and for an event who is coming.", "/topics/{id}", "")
	b.read("forum", "search", "Search every forum the person can read (full text, accents ignored): titles and posts with a snippet and a link. forum = a slug; solved = yes or no (open questions); titles = 1 for titles only.", "/search", "q forum solved titles tag")
	b.read("forum", "notifications", "Read the person's forum notifications (mentions, replies, quotes, reactions, solutions, join requests, flags, reminders) with links; unread=1 for unread only.", "/notifications", "unread limit")
	b.read("forum", "bookmarks", "List the posts the person bookmarked, with their notes and reminders.", "/bookmarks", "")
	b.read("forum", "events", "List the event topics coming up in the person's forums (date, time, place) with the person's own answer (going, maybe, no).", "/events", "")
	b.read("forum", "day", "Read what the person wrote in the forums on one day (topics started and replies, with links) — for the journal.", "/day/{date}", "")
	topic := textFields("forum title body event_start event_end event_place").integers("category_id").enum("kind", "discussion", "question", "event").strings("tags")
	topic["forum"].(map[string]any)["description"] = "The forum's slug (see the forums tool)."
	topic["body"].(map[string]any)["description"] = "The first post, in Markdown; @handle mentions someone."
	topic["event_start"].(map[string]any)["description"] = "For kind event: local start, YYYY-MM-DDTHH:MM (or a date)."
	b.write("forum", "start_topic", "Start a topic in one of the person's forums, as them: a discussion, a question (others can answer, the person marks the solution) or an event (with event_start and event_place). Returns the topic with its link; in some forums it waits for a moderator first.", "POST", "/topics", topic, "forum title body")
	b.write("forum", "reply", "Reply to a topic as the person, in Markdown; reply_to = the number of the post answered (optional).", "POST", "/topics/{id}/posts", textFields("body").integers("reply_to"), "body")
	b.write("forum", "mark_read", "Mark a topic read up to a post number (empty = all of it).", "POST", "/topics/{id}/read", mcpFields{}.integers("number"), "")
}

func (b *mcpCatalogBuilder) capsule() {
	b.read("capsule", "summary", "Get Pocket Capsule at a glance: time capsules ready to open now, the next ones to open (when, in words), the ones still being filled, and how many are opened, with their links.", "/summary", "")
	b.read("capsule", "capsules", "List the person's time capsules (made by them or that they are in): title, state (filling, sealed, opened), ready (sealed and its day has come), when it opens and in words, how many things inside, their role. state = ready, filling, sealed or opened.", "/capsules", "state")
	b.read("capsule", "capsule", "Read one time capsule. While it is being filled: what the person may see inside. Sealed: only the outside (when it opens, how many things of each kind, from whom) — nothing inside is ever shown before it opens. Opened: everything (letters, predictions with whether they came true, questions with answers, goals, lists), the world of sealing day, then-and-now and the letter written back.", "/capsules/{id}", "")
	b.read("capsule", "predictions", "Read every prediction and goal from the person's opened capsules: whether each came true (yes, partly, no or not marked), how sure they were, the share that came true overall and by how sure they were.", "/predictions", "")
	b.read("capsule", "search", "Search the person's time capsules by name and outside, and the things inside the opened ones, with links. Never finds anything inside a sealed capsule.", "/search", "q")
	b.read("capsule", "events", "What happened lately in the person's capsules (made, sealed, things put in, ready, opened, sealed broken early, written back), newest first; days = how far back (default 30).", "/events", "days")
	b.read("capsule", "day", "Read one day in the person's time capsules (sealed, opened, things put in, capsules that open that day) — for the journal.", "/day/{date}", "")
	b.read("capsule", "templates", "List the kinds of capsule to start from (letter to future me, New Year's, for my child on their 18th birthday, family, couple, birthday letter, team, graduation, blank) with ideas: things to write about, questions, predictions and lists.", "/templates", "")
	capsule := textFields("title about opens_at for_name for_date").integers("for_age").enum("template", "self", "newyear", "child", "family", "couple", "birthday", "team", "graduation", "blank").enum("open_rule", "date", "birthday", "anniversary")
	capsule["opens_at"].(map[string]any)["description"] = "When it opens: a local date YYYY-MM-DD (at the person's opening hour) or YYYY-MM-DDTHH:MM. With open_rule birthday or anniversary give for_date (YYYY-MM-DD) and for_age instead."
	capsule["for_age"].(map[string]any)["description"] = "Open on this birthday or anniversary (18 = the 18th); 0 = the next one."
	b.write("capsule", "start_capsule", "Start a time capsule for the person (it is not sealed: they add to it and seal it themselves in Pocket Capsule). Returns it with its link.", "POST", "/capsules", capsule, "title")
	item := textFields("title body url").integers("confidence").enum("kind", "letter", "prediction", "question", "goal", "link")
	item["body"].(map[string]any)["description"] = "The letter, or the prediction, question or goal itself."
	item["confidence"].(map[string]any)["description"] = "For a prediction: how sure, 0-100."
	b.write("capsule", "add_to_capsule", "Put something in one of the person's time capsules, as them: a letter, a prediction (with how sure), a question for whoever opens it, a goal or a link. A sealed capsule with its slot open takes it unseen — nobody, the person included, sees it until the capsule opens. This never seals or opens a capsule.", "POST", "/capsules/{id}/items", item, "kind")
}

func (b *mcpCatalogBuilder) family() {
	// Pocket Family: a household shared by its people (grown-ups, children, helpers such as a grandparent or the sitter; a person may
	// be in more than one family — the person's current one is used). Runs are pick-ups, drop-offs and lifts on a day (time HH:MM,
	// children = person ids, driver_id = who takes it, 0 = nobody yet; state planned|asking|done|skipped, asking = the driver asked for
	// someone else to take it); weekly plans make them. Chores repeat and may go in turns (whose = whose turn). Days are YYYY-MM-DD,
	// local; amounts in letters are in CENTS. Person ids come from pocket_family_family.
	b.read("family", "summary", "Get the family at a glance: today's runs (who picks up whom), the person's own runs today, runs that need someone in the next week (needs_someone: nobody yet or asking), chores due today (and the person's own), how many things are on the shopping lists, letters to deal with, unseen notes on the board, what the children take along tomorrow, the next plan, and whether there is no school today.", "/summary", "")
	b.read("family", "family", "Read the person's current family: its people (id, name, role grownup|child|helper, birthday, school, class_name, phone, notes such as allergies, whether they have an account) and me (the person asking).", "/family", "")
	familyToday := b.read("family", "today", "Read the family's Today (date = YYYY-MM-DD, today when left out): runs today and tomorrow with who takes them, runs that need someone, the agenda (the family's plans, birthdays, holidays), chores due (by person), what to take along today and tomorrow (bring_*: person_id, what, for), whether there is no school, letters to deal with soon, shopping lists with how much is open and who is at the shop, the latest notes on the board, and who did what lately.", "/today", "date")
	familyToday.Query = map[string]string{"quick": "1"}
	familyWeek := b.read("family", "week", "Read the family's week day by day (from = YYYY-MM-DD, the start of this week when left out; days = 1 to 31, default 7): each day's runs, agenda, chores due and what to take along, and no_school.", "/week", "from days")
	familyWeek.Query = map[string]string{"quick": "1"}
	b.read("family", "runs", "List the runs between two days (from, to = YYYY-MM-DD; today and the next 6 days when left out; mine=1 for only the person's own): id, day, time, kind pickup|dropoff|lift, place, children (person ids), driver_id (0 = nobody yet), state, asked_by and asked_note when someone asked for cover, clash when the driver is due somewhere else then.", "/runs", "from to mine")
	b.read("family", "plans", "List the weekly runs (the school run, a club): id, kind, weekdays (1 = Monday), time, place, children, driver_id, rota (weekday → person), term_only (not in school holidays), active.", "/plans", "")
	b.read("family", "chores", "List the chores (all=1 to include finished one-offs): id, title, emoji, repeat once|daily|weekly|monthly|every, weekdays, assign one|turns|anyone, people, whose (whose turn now), stars, next_due, last_done (who, when) and done_on (days this week).", "/chores", "all")
	b.read("family", "stars", "Read the week's chores per person (week = any day in it): done, stars and last week's stars, plus the latest chores done.", "/stars", "week")
	b.read("family", "events", "List the family's own plans between two days (from, to = YYYY-MM-DD): id, title, day, end_day, start_time, end_time, place, people, kind school|activity|appointment|family|other, repeat, bring (what to take along), notes. Repeating plans come once per day they fall on.", "/events", "from to")
	b.read("family", "lists", "List the shopping lists: id, name, how many things are still to get (open), and shopper_id when someone is at the shop now.", "/lists", "")
	b.read("family", "list_items", "Read one shopping list (id = the list): the list and its items (name, qty, note, aisle, added_by, done, done_by).", "/lists/{id}/items", "")
	b.read("family", "school", "Read the school year: terms, holidays and closed days (kind term|holiday|closed, starts_on, ends_on), each child's school week (person_id, weekday, starts, ends, bring = what to take that day) and now (the current term, the next break, no_school today).", "/school", "")
	b.read("family", "letters", "List letters from school or a club (state todo|done|none, all when left out): id, title, sender, summary, people, kind info|permission|payment|bring|event, action (what to do), due_on, amount in cents and currency, state, done_by.", "/letters", "state limit")
	b.read("family", "get_letter", "Read one letter (id) in full: its text, what it asks, the dates made from it (events) and what the person made from it in other apps.", "/letters/{id}", "")
	b.read("family", "board", "Read the family board, pinned first then newest: id, body, person_id (who wrote it), pinned, until, seen_by (person ids) and seen (by the person asking).", "/posts", "")
	b.read("family", "info", "Read what everyone in the family should know, by section emergency|health|school|home|sitter|other: label, value, phone, person_id (0 = the whole family): who to call, allergies, the doctor, the school office, the wifi, bedtimes.", "/info", "")
	b.read("family", "search", "Search the family by words: people, plans, letters, chores, shopping items, info lines and notes on the board (results: type, id, title, subtitle, url).", "/search", "q")
	b.read("family", "day", "Read one day in the family (date = YYYY-MM-DD): runs, agenda, chores due and done, what to take along and who did what.", "/day/{date}", "")
	familyDay := func(description string) map[string]any {
		return map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": description}
	}
	familyClock := func(description string) map[string]any {
		return map[string]any{"type": "string", "pattern": `^([01]\d|2[0-3]):[0-5]\d$`, "description": description}
	}
	familyPeople := func(description string) map[string]any {
		s := arraySchema(map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)})
		s["description"] = description
		return s
	}
	familyItems := textFields("name list")
	familyItems["name"].(map[string]any)["description"] = "What to buy; one thing per line (\"2 milk\\nbread\"). A thing already on the list is not added twice."
	familyItems["list"].(map[string]any)["description"] = "The list's name (from pocket_family_lists); the first list when left out."
	b.write("family", "add_items", "Put things on the family's shopping list. Returns the list, the items and how many were new (added).", "POST", "/items", familyItems, "name")
	familyEvent := textFields("title place bring notes").enum("kind", "family", "school", "activity", "appointment", "other")
	familyEvent["day"] = familyDay("The day.")
	familyEvent["start_time"] = familyClock("From, HH:MM; leave out for all day.")
	familyEvent["end_time"] = familyClock("To, HH:MM.")
	familyEvent["people"] = familyPeople("Who it is for (person ids from pocket_family_family).")
	familyEvent["bring"].(map[string]any)["description"] = "What to take along that day."
	b.write("family", "add_event", "Add a plan to the family's week (an appointment, a party, a school thing, a visit). Returns the event.", "POST", "/events", familyEvent, "title day")
	familyRun := textFields("place title notes").enum("kind", "pickup", "dropoff", "lift")
	familyRun["day"] = familyDay("The day.")
	familyRun["time"] = familyClock("When, HH:MM.")
	familyRun["children"] = familyPeople("Who is picked up, dropped off or given a lift (person ids).")
	familyRun["driver_id"] = map[string]any{"type": "integer", "minimum": 0, "maximum": int64(9007199254740991), "description": "Who takes it (a person id); 0 or left out = nobody yet, and the other grown-ups are asked."}
	b.write("family", "add_run", "Add one pick-up, drop-off or lift. Ask the person who takes it if they did not say. Returns the run.", "POST", "/runs", familyRun, "day kind")
	b.write("family", "take_run", "The person takes a run (id): they become the one who does it, and whoever asked is told. A change: ask first.", "POST", "/runs/{id}/take", nil, "")
	familyDone := mcpFields{"person_id": map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": "Who did it; the person asking when left out."}}
	b.write("family", "done_chore", "Tick off a chore (id) as done today: it moves to its next day, and a chore in turns passes to the next person. A change: ask first.", "POST", "/chores/{id}/done", familyDone, "")
	familyPost := textFields("body").booleans("pinned")
	familyPost["body"].(map[string]any)["description"] = "The note for everyone (\"Grandma comes for lunch on Sunday\")."
	b.write("family", "add_note", "Put a note on the family board for everyone; the others get a push when they asked for one. Returns the post.", "POST", "/posts", familyPost, "body")
}

func (b *mcpCatalogBuilder) chat() {
	// Pocket Chat: the person's conversations (direct, groups and "Me", their own space), shared with the people in them; messages can be text, cards from other apps, polls, to-do lists, voice messages (with their words) and Pocket AI's answers.
	b.read("chat", "summary", "Get what waits in your chats: unread counts, the conversations with unread messages (name, how many, the last message), the messages that mention you, and today's activity per conversation.", "/summary", "")
	b.read("chat", "conversations", "List your conversations: id, name as you see it (the other person in a direct chat, \"Me\" for your own space), kind direct|group|self, members, unread and mentioned counts, the last message, muted/pinned/archived.", "/conversations", "")
	b.read("chat", "messages", "Read a conversation's messages, oldest first (default the latest 60): author_name, body, kind (text, card, poll, todo, voice, ai, app, system), created_at, data (a card's title, a poll's question and options with votes, a to-do list's items, a voice message's words as data.text), reactions. before / after = a message id; around = a message id to read the messages near it.", "/conversations/{id}/messages", "before after around limit")
	b.read("chat", "search", "Search the messages in your conversations (accents ignored), newest first: each has snippet, author_name, conversation_title, conversation_id, created_at. conversation = an id to search one; from = a person's user id; kind photos|files|links|cards|polls|todos|voice; since/until YYYY-MM-DD.", "/search", "q conversation from kind since until limit")
	b.read("chat", "unread", "Count what you have not read: messages, conversations and mentions.", "/unread", "")
	b.read("chat", "day", "Read one day in your chats: messages per conversation, plans people suggested (with day and time), cards shared from other apps, photos and voice messages.", "/day/{date}", "")
	b.read("chat", "people", "List the people on this Pocket Chat (id, name, email, online now) — who the person can write to.", "/people", "")
	b.read("chat", "saved", "List the messages the person saved (starred), newest first, with their conversation.", "/saved", "")
}
