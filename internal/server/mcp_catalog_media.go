package server

// MCP app catalog, media: bookshelf, voice, blog, photos, photolab, karaoke, podcasts, subtitles.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) bookshelf() {
	b.read("bookshelf", "list", "Search books on your shelf.", "/books", "q status tag limit")
	b.read("bookshelf", "get", "Read a book, review and reading progress.", "/books/{id}", "")
	b.read("bookshelf", "reading", "Read what you are reading now: progress, pages read today, pace, days left and when each book will be finished.", "/reading/today", "")
	book := textFields("title authors isbn13 isbn10 description tags review").integers("pages current_page").numbers("rating").enum("status", "want", "reading", "read", "abandoned")
	b.write("bookshelf", "create", "Add a book to your shelf.", "POST", "/books", book, "title")
	b.write("bookshelf", "update", "Update supplied book details, rating or review.", "PUT", "/books/{id}", book, "")
	b.write("bookshelf", "progress", "Record your current page. This may change want-to-read to reading.", "POST", "/books/{id}/progress", mcpFields{}.integers("page"), "page")
}

func (b *mcpCatalogBuilder) voice() {
	// Pocket Voice: voice memos recorded on the phone, written out on the server (whisper.cpp) and tidied by Pocket AI (title, summary, cleaned text, kind and actions:
	// tasks, events, journal lines and expenses found in the memo), then sent to Pocket Notes, Tasks, Calendar or Money. Expense amounts here are major units (12.5 = 12.50).
	b.read("voice", "summary", "Get a short overview of your voice memos: how many (all, in the inbox waiting to be filed, starred, still being written out, failed), minutes recorded, how many today, and the latest inbox memos with their summary and how many suggested actions wait to be sent.", "/summary", "")
	b.read("voice", "memos", "List or filter your voice memos, newest first (oldest=1 reverses). q searches titles and words (accents ignored); kind note|todo|idea|journal|meeting|shopping|reminder; tag; inbox=1 only memos not filed yet; starred=1; status queued|working|done|failed; from and to YYYY-MM-DD (the day it was recorded). Each memo has title, recorded_at, duration_ms, status, summary, tidy (the cleaned text), transcript, kind, tags, actions (what the AI found, with sent_to once sent) and sent (what was made from it in other apps); counts has the totals.", "/memos", "q kind tag inbox starred status from to limit offset oldest")
	b.read("voice", "get_memo", "Read one voice memo: transcript (the words as written out, with timed segments), tidy (the cleaned text), summary, kind, tags, language, status (queued|working|done|failed, with error), actions the AI found (a list: type task|event|journal|expense with title, due, start, end, location, text, amount in major units, currency, date and notes; sent_to and sent_url once sent, skipped when dismissed) and sent (every item made from it in Notes, Tasks, Calendar or Money).", "/memos/{id}", "")
	b.read("voice", "search", "Search your voice memos' titles and words (1 to 200 characters, accents ignored).", "/search", "q limit")
	b.read("voice", "day", "Read one day's voice memos, oldest first, each with its time, length, summary, text and where it was sent: what Pocket Day puts in the journal.", "/day/{date}", "")
	b.read("voice", "tags", "List the tags on your voice memos with how many memos have each.", "/tags", "")
	memoFields := func(text string) mcpFields {
		f := textFields("title tags recorded_at "+text).enum("kind", "note", "todo", "idea", "journal", "meeting", "shopping", "reminder")
		f["tags"].(map[string]any)["description"] = "Comma-separated, e.g. \"work, ideas\"; replaces all tags."
		f["recorded_at"].(map[string]any)["description"] = "RFC 3339 time, e.g. 2026-11-05T09:30:00+01:00."
		return f
	}
	b.write("voice", "add_memo", "Keep a typed or dictated text as a voice memo (\"keep this as a memo: …\"). text is required; title, kind and tags are optional (recorded_at defaults to now). Pocket AI tidies it in the background when the person has that on: title, summary, kind and suggested actions.", "POST", "/memos",
		memoFields("text").field("language", map[string]any{"type": "string", "pattern": `^[a-z]{2,3}$`, "description": "Language code like bs or en (default: the person's setting)."}), "text")
	b.write("voice", "update_memo", "Update supplied memo fields: title, transcript (fix the words as written out; then pocket_voice_tidy_memo to tidy again), tidy (the cleaned text), summary, kind, tags, starred, filed (true takes it out of the inbox), recorded_at.", "PUT", "/memos/{id}",
		memoFields("transcript tidy summary").booleans("starred filed"), "")
	b.write("voice", "tidy_memo", "Run the Pocket AI tidy on one memo now and return it: rewrites summary, tidy text and the suggested actions not sent yet (so action indexes change), and title, kind and tags unless the person set them. Needs an AI connected in Pocket Voice.", "POST", "/memos/{id}/tidy", nil, "")
	send := textFields("title text folder due notes start end location currency date").enum("to", "note", "daily", "task", "event", "expense").integers("list_id calendar_id account_id category_id").
		field("action", map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "Index (0-based) in the memo's actions of the suggestion being sent; it is then marked sent and cannot be sent twice. Its fields are not filled in for you: pass them too."}).
		field("amount", map[string]any{"type": "number", "minimum": 0.01, "maximum": 10000000, "description": "Expense amount in MAJOR units (12.5 = 12.50), not minor units."})
	send["due"].(map[string]any)["description"] = "Task due day, YYYY-MM-DD."
	send["start"].(map[string]any)["description"] = "Event start, YYYY-MM-DD or YYYY-MM-DDTHH:MM (local time)."
	send["date"].(map[string]any)["description"] = "Expense day, YYYY-MM-DD (default today)."
	send["folder"].(map[string]any)["description"] = "Pocket Notes folder for a note (default: the person's Voice setting)."
	b.write("voice", "send_memo", "Send a memo, or one action found in it, into the person's own Pocket apps through Voice's links: to note (a new Pocket Notes note with the tidy text; title, text and folder optional), daily (a line in today's daily note; a journal action goes here with its text), task (Pocket Tasks: title required; due, notes, list_id), event (Pocket Calendar: title and start required; end, location, calendar_id) or expense (Pocket Money: title and amount required, amount in major units here; currency, date, account_id, category_id). Prefer the actions the AI already found (pocket_voice_get_memo): pass action = its index with its fields. Lists, calendars and accounts default to the person's Voice settings. This creates things in other apps: always ask first. Returns sent (app, kind, title, url) and the memo.", "POST", "/memos/{id}/send", send, "to")
}

func (b *mcpCatalogBuilder) blog() {
	// Pocket Blog: the person's own blogs (several per person), posts and pages in Markdown, drafts until published, comments readers wrote and readers counted per day.
	b.read("blog", "summary", "Get a short overview of your blogs: each blog with its public address, published posts, drafts and comments waiting; readers in the last 7 days; the latest drafts, scheduled posts and recently published posts with their readers.", "/summary", "")
	b.read("blog", "blogs", "List your blogs with their public address (url), look (theme, accent), language, who can read them (visibility public|unlisted|private), comment mode and counts.", "/blogs", "")
	b.read("blog", "posts", "List your posts or pages, drafts first then newest. blog = a blog id (default all); kind post|page; status draft|scheduled|published; tag; q searches titles and text (accents ignored); sort updated|oldest|views. Each has title, slug, status, published_at or publish_at, excerpt, tags, words, views, comments_count and url (the public address; a draft's url carries its private preview link).", "/posts", "blog kind status tag q limit offset sort")
	b.read("blog", "get_post", "Read one post or page with its full Markdown body, excerpt, tags, status and dates, cover picture, readers, comments and where it came from (source: another Pocket app or an import).", "/posts/{id}", "")
	b.read("blog", "search", "Search your posts and pages (drafts too) by their words; each result has a snippet, status, date and url.", "/search", "q limit")
	b.read("blog", "comments", "List the comments readers wrote under your posts, newest first: status pending (waiting for your OK), approved or spam (default: all but spam); blog and post narrow it. Each has name, body, post_title and post_url.", "/comments", "blog post status")
	b.read("blog", "stats", "Readers of your blog: total in the last days (default 30) and the period before, readers per day, the most read posts and the number of comments. Counted without cookies, once a day per reader per post.", "/stats", "blog days")
	b.read("blog", "day", "Read what happened on your blog on one day: posts published and posts written, readers and comments.", "/day/{date}", "")
	b.read("blog", "tags", "List the tags on a blog's posts with how many posts have each (blog = a blog id; default your first blog).", "/tags", "blog")
	b.read("blog", "ideas", "Things the person did lately in their other Pocket apps that would make a good post (a trip they came back from, a book they finished, a place they loved, a recipe they cooked, a note tagged #blog), with why.", "/ideas", "")
	postFields := func() mcpFields {
		f := textFields("title body excerpt tags seo_description").enum("kind", "post", "page").booleans("featured comments in_menu")
		f["body"].(map[string]any)["description"] = "The post in Markdown: ## headings, paragraphs, - lists, **bold**, > quotes, links, ![alt](url) pictures; a YouTube or Vimeo link alone on a line becomes the video. No HTML (it is not shown). No # title line: the title is separate."
		f["tags"].(map[string]any)["description"] = "Comma-separated, e.g. \"travel, bosnia\"; replaces all tags."
		f["excerpt"].(map[string]any)["description"] = "One or two sentences shown in lists and when shared (empty: the start of the post)."
		return f
	}
	b.write("blog", "create_draft", "Start a post (or page) as a DRAFT in the person's blog, for them to finish and publish in Pocket Blog: nothing is public. Write it in the blog's language (pocket_blog_blogs) and the person's voice, from what they told you or from their other apps (a trip, a book, a place, a recipe); never invent facts about them, leave [placeholders] instead. blog_id picks the blog (default the first). Returns the post with its private preview url.", "POST", "/posts",
		postFields().integers("blog_id"), "title")
	b.write("blog", "update_post", "Change supplied fields of one of the person's posts: title, body (the whole Markdown text), excerpt, tags, seo_description, featured, comments, in_menu. A published post changes for readers at once: ask first.", "PUT", "/posts/{id}", postFields(), "")
	b.write("blog", "publish", "Publish a post on the blog now, or schedule it: at = an RFC 3339 time or YYYY-MM-DDTHH:MM local (empty = now). This makes it public to everyone who can read the blog: always ask first, naming the post and the blog.", "POST", "/posts/{id}/publish", textFields("at"), "")
	b.write("blog", "unpublish", "Take a published or scheduled post off the blog; it becomes a draft again. Readers no longer see it: always ask first.", "POST", "/posts/{id}/unpublish", nil, "")
	b.write("blog", "reply_comment", "Answer a reader's comment (id = the comment) as the blog's author; a waiting comment is approved with it. The answer is public: always ask first and show the text.", "POST", "/comments/{id}/reply", textFields("body"), "body")
}

func (b *mcpCatalogBuilder) photos() {
	b.read("photos", "search", "Search the person's photo library in Pocket Photos: newest first, or the best matches for a description when q is set (smart search on what is in the photo: \"beach at sunset\", \"dog in the snow\"); favorites=1 for favourites only; type photo (default) or video; limit up to 100 (30). Each: id, title (file name), kind, taken_at, place, width, height, favorite, item_url (opens the photo in Pocket Photos) and thumb_url. To edit one, pass its id to pocket_photolab_add_pictures with source photos.", "/photos", "q favorites type limit")
	b.read("photos", "get", "Read one photo from Pocket Photos (id = its asset id from pocket_photos_search or a Pocket Photos link ?photo=<id>): title, taken_at, place, latitude/longitude, camera, description, the people recognised in it, size, favorite, item_url and thumb_url.", "/photos/{id}", "")
	b.read("photos", "view", "Look at one photo from Pocket Photos (id = its asset id from pocket_photos_search or a Pocket Photos link ?photo=<id>): it comes back as an image you can see (screen size, large ones scaled to 1568 px), so you can say what is in it, read text in it or describe it.", "/photos/{id}/picture", "").FileResponse = true
}

func (b *mcpCatalogBuilder) photolab() {
	b.read("photolab", "summary", "Get Photo Lab at a glance: how many pictures, edited, favourites, own looks and saved copies, the storage used, the latest pictures worked on and the latest copies saved (to the device, Pocket Photos or Pocket Files).", "/home", "")
	b.read("photolab", "pictures", "List the pictures in Photo Lab, the person's picture editor. q searches names, original file names and notes; show edited|unedited|favorites|photos|files|upload|collage|copy; sort recent|added|taken|title|size. Each: id, title, width, height, source (photos = from Pocket Photos, with source_ref its asset id), edited, favorite, taken_at, camera, exports, updated_at.", "/images", "q show sort limit offset")
	b.read("photolab", "get_picture", "Read one picture: its details and its edit (recipe: crop, turn, sliders, look, effects, text and drawings, frame), its versions count, and item_url to open it in the editor.", "/images/{id}", "")
	b.read("photolab", "looks", "List the person's own looks (saved light, colour and effects) by name. Built-in looks are: vivid, warm, cool, golden, teal-orange, film, fade, matte, pastel, chrome, dramatic, vintage, lomo, summer, winter, night, mono, noir, silver, sepia.", "/presets", "")
	b.read("photolab", "history", "List the copies saved out of Photo Lab, newest first: title, format, width, height, bytes, destination (download, photos, files), target (the new photo's id or the file's path).", "/exports", "limit")
	labImport := mcpFields{}.enum("source", "photos", "files").strings("ids paths").field("space", map[string]any{"type": "string", "maxLength": 200, "description": "Pocket Files: the place, \"home\" for My files (default)."})
	labImport["ids"].(map[string]any)["description"] = "Pocket Photos asset ids (from Pocket Photos), up to 100."
	labImport["paths"].(map[string]any)["description"] = "Pocket Files paths like /Pictures/beach.jpg, up to 100."
	b.write("photolab", "add_pictures", "Bring pictures into Photo Lab to edit: from Pocket Photos (source photos, ids) or Pocket Files (source files, paths). Pictures already there are not added twice. Returns them with their ids and, for one, its url in the editor.", "POST", "/images/import", labImport, "source")
	labApply := mcpFields{}.field("ids", arraySchema(map[string]any{"type": "integer", "minimum": 1})).
		field("look", map[string]any{"type": "string", "maxLength": 80, "description": "A built-in look (vivid, warm, cool, golden, teal-orange, film, fade, matte, pastel, chrome, dramatic, vintage, lomo, summer, winter, night, mono, noir, silver, sepia), the name of one of the person's own looks, or none to take the look off."}).
		field("strength", map[string]any{"type": "number", "minimum": 0, "maximum": 100, "description": "How strong a built-in look is, 0-100 (default 80)."}).
		field("adjust", map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "number"}, "description": "Sliders to set, -100..100 (hue -180..180; sharpen, denoise, grain, fade, blur 0..100): exposure, brightness, contrast, highlights, shadows, whites, blacks, temperature (warmth), tint, vibrance, saturation, hue, clarity, dehaze, sharpen, denoise, vignette, grain, fade, blur. 0 resets one."}).
		enum("aspect", "none", "original", "1:1", "4:5", "5:4", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16").
		field("rotate", map[string]any{"type": "integer", "enum": []any{0, 90, 180, 270}, "description": "Turn clockwise by this much (0 = back upright)."}).
		field("straighten", map[string]any{"type": "number", "minimum": -45, "maximum": 45}).
		booleans("black_and_white reset").
		field("frame", map[string]any{"type": "number", "minimum": 0, "maximum": 25, "description": "A white border as % of the short side; 0 removes it."}).
		field("long_edge", map[string]any{"type": "integer", "minimum": 0, "maximum": 20000, "description": "Resize so the long side has this many pixels; 0 = full size."})
	labApply["ids"].(map[string]any)["description"] = "The pictures to change (Photo Lab ids), 1 to 100."
	labApply["aspect"].(map[string]any)["description"] = "Crop to this shape, centred (none = no crop)."
	labApply["reset"].(map[string]any)["description"] = "Start from the original before applying the rest."
	b.write("photolab", "apply", "Change the edit of one or more Photo Lab pictures: put on a look, set sliders, crop to a shape, turn, straighten, black and white, a border, a size. Originals never change and every change can be undone in the editor; the new look is drawn the next time Photo Lab is open. Returns the pictures with their edits.", "POST", "/images/apply", labApply, "ids")
}

func (b *mcpCatalogBuilder) karaoke() {
	b.read("karaoke", "summary", "Get Pocket Karaoke at a glance: how many songs and nights, what was sung this week, the night on now (with its join code and how many wait) and the next one planned, with links.", "/summary", "")
	b.read("karaoke", "songs", "List or search the person's karaoke songs (title, artist, language, where from: upload, youtube, music, files, link; whether they have words; best score). q searches title, artist and tags; favourite=1, no_words=1, book=ID, sort = title|artist|added|sung|plays.", "/songs", "q source language favourite no_words book sort limit")
	b.read("karaoke", "song", "Read one song: its details, its words (LRC, timed or not) and who sang it with the scores.", "/songs/{id}", "")
	b.read("karaoke", "nights", "List the person's karaoke nights: the live one first, then planned (with day, place, join code), then past ones with how many songs were sung and guests.", "/nights", "")
	b.read("karaoke", "night", "Read one night: its details, what is on the big screen now, the queue in order (who sings what), songs asked for, guests and what was sung with the scores.", "/nights/{id}", "")
	b.read("karaoke", "singers", "List the person's singers with how much they sang, their best and average score.", "/singers", "")
	b.read("karaoke", "history", "List what was sung (newest first): song, singer, night or practice, score, cheers and stars. Filter by song, singer, night (ids), practice=1, from=YYYY-MM-DD.", "/performances", "song singer night practice from limit")
	b.read("karaoke", "stats", "Karaoke numbers: the most sung songs and singers, songs sung per day, average and best score. period = month|year (empty = ever).", "/stats", "period")
	b.read("karaoke", "search", "Search the person's karaoke: songs by title, artist or a line of their words, singers and nights.", "/search", "q limit")
	b.read("karaoke", "day", "Read one day in Pocket Karaoke (nights and every song sung, with scores) — for the journal.", "/day/{date}", "")
	b.read("karaoke", "youtube_search", "Search YouTube for karaoke videos to add (the person's own 'karaoke' word is added unless exact=1). Returns id, title, channel, length and whether it looks like a karaoke video (have = the song id when already added). Nothing is added.", "/youtube/search", "q exact")
	ksong := textFields("url title artist lyrics tags notes")
	ksong["url"].(map[string]any)["description"] = "A YouTube link (watch, youtu.be, shorts) or a link to a song file; with a url the title and artist come from it."
	ksong["lyrics"].(map[string]any)["description"] = "The words as LRC ([mm:ss.xx] per line) or plain lines. Never invent lyrics; only words the person gave or that were found."
	b.write("karaoke", "add_song", "Add a song to the person's karaoke library: a YouTube link (from youtube_search: https://youtu.be/ID) or a song file link — or a title with its words. The words are looked up by themselves when the person allows it. Returns the song (the one already there if it was added before).", "POST", "/songs", ksong, "")
	knight := textFields("title starts_at place about").enum("rotation", "fair", "order").booleans("approve guest_youtube scores calendar").integers("per_guest autoplay")
	knight["starts_at"].(map[string]any)["description"] = "Local date and time, YYYY-MM-DDTHH:MM."
	b.write("karaoke", "plan_night", "Plan a karaoke night for the person (it starts when they press Start). Guests join on their phones with the code in the answer.", "POST", "/nights", knight, "title")
	kq := textFields("singer note").integers("song_id singer_id key_shift").booleans("front")
	kq["singer"].(map[string]any)["description"] = "The name shown in the queue, e.g. Ana or Ana & Marko."
	b.write("karaoke", "queue_song", "Put one of the person's songs in a night's queue for someone (id = the night; song_id from songs). It goes round fairly unless front is true.", "POST", "/nights/{id}/queue", kq, "song_id")
}

func (b *mcpCatalogBuilder) podcasts() {
	b.read("podcasts", "subscriptions", "List your podcast subscriptions.", "/podcasts", "q kind sort")
	b.read("podcasts", "episodes", "Search episodes in your library.", "/episodes", "q podcast status saved queued limit offset")
	b.read("podcasts", "get_episode", "Read an episode and its show notes.", "/episodes/{id}", "")
	b.read("podcasts", "queue", "Read your listening queue.", "/queue", "")
	b.write("podcasts", "subscribe", "Subscribe to a podcast feed URL. The application fetches the feed.", "POST", "/podcasts", textFields("url title"), "url")
	b.write("podcasts", "favorite", "Set a podcast's favorite flag.", "POST", "/podcasts/{id}/favorite", mcpFields{}.booleans("favorite"), "favorite")
	b.write("podcasts", "save_episode", "Save an episode to your saved list.", "POST", "/episodes/{id}/save", nil, "")
	b.write("podcasts", "queue_episode", "Add an episode to your listening queue.", "POST", "/episodes/{id}/queue", nil, "")
}

func (b *mcpCatalogBuilder) subtitles() {
	b.read("subtitles", "titles", "List titles in the subtitle library.", "/library/titles", "q limit offset")
	b.read("subtitles", "files", "List subtitle files.", "/library/files", "q title lang status library season limit offset")
	b.read("subtitles", "get_file", "Read a subtitle file's metadata.", "/files/{id}/", "")
	b.read("subtitles", "text", "Read subtitle text (up to the application's preview limit).", "/files/{id}/text", "").TextResponse = true
	b.write("subtitles", "nudge", "Shift the timing of one subtitle file by a signed millisecond offset; creates the application's normal backup.", "POST", "/files/{id}/nudge", mcpFields{"ms": map[string]any{"type": "integer", "minimum": -60000, "maximum": 60000}}, "ms")
	b.write("subtitles", "pin", "Pin a subtitle file with a note.", "POST", "/files/{id}/pin", textFields("note"), "note")
}
