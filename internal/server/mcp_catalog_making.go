package server

// MCP app catalog, making things: puzzlecraft, slides, math, printfarm, gamebuilder.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) puzzlecraft() {
	b.read("puzzlecraft", "summary", "Get Pocket Puzzlecraft at a glance: how many puzzles of each kind the person has, how many are shared, escape rooms and treasure hunts coming up (with what is still to set up), today's puzzles of the day and whether they are solved, and their run of days.", "/summary", "")
	b.read("puzzlecraft", "today", "Read the person's Today page: the three puzzles of the day (kind, level, solved and the time), the run of days, puzzles they are in the middle of, escape rooms coming up and who solved their shared puzzles in the last week. date=YYYY-MM-DD reads an earlier day.", "/today", "date")
	b.read("puzzlecraft", "puzzles", "List the person's puzzles (crossword, wordsearch, sudoku, nonogram = picture puzzle, cryptogram = secret code, scramble, quiz, maze, logic, ladder, jigsaw): title, kind, level, size, ready (fair, one answer), share_on, plays, solves, best_ms. kind, q, tag filter; shared=1 only shared ones; archived=1 the archive.", "/puzzles", "kind q tag shared archived limit")
	b.read("puzzlecraft", "puzzle", "Read one puzzle as a player sees it (never its answer), with its share link (share_url, playable without an account when share_on) and how it was played.", "/puzzles/{id}", "")
	b.read("puzzlecraft", "results", "Read how one puzzle was played: every go (player, time ms, hints, mistakes, finished), the best times and the average.", "/puzzles/{id}/plays", "")
	b.read("puzzlecraft", "adventures", "List the person's escape rooms, treasure hunts, mysteries and party games: title, kind, date, time, place, stages_n, minutes (time limit), status, share_on, plays and live (teams playing right now).", "/adventures", "")
	b.read("puzzlecraft", "adventure", "Read one escape room or hunt: its story, stages (title, story, kind of lock — answers and hiding places are left out), setting-up list and share link.", "/adventures/{id}", "")
	b.read("puzzlecraft", "books", "List the person's puzzle books (puzzles put together to print or share as one link).", "/books", "")
	b.read("puzzlecraft", "wordlists", "List the person's word lists (words with clues to make puzzles from).", "/wordlists", "")
	b.read("puzzlecraft", "stats", "Read the person's solving: puzzles solved, by kind with the usual and the best time and hints, the run of days, the last 84 days.", "/stats", "")
	b.read("puzzlecraft", "search", "Search the person's puzzles, escape rooms, books and word lists.", "/search", "q")
	b.read("puzzlecraft", "day", "Read one day in Pocket Puzzlecraft: what was solved (by the person and by players of their shared puzzles) and escape rooms planned that day.", "/day/{date}", "")
	b.read("puzzlecraft", "words", "Find English words that fit a crossword pattern (? = any letter, e.g. C?T), everyday words first, and the person's own word-list words.", "/words", "pattern limit")
	make := textFields("title instructions words_text theme message text author picture mode style").integers("size count").
		enum("kind", "crossword", "wordsearch", "sudoku", "nonogram", "cryptogram", "scramble", "quiz", "maze", "logic", "ladder", "jigsaw").
		enum("level", "easy", "medium", "hard", "expert").enum("language", "en", "bcs", "other").enum("cipher", "random", "caesar", "numbers", "symbols").strings("tags").booleans("share_on")
	make["words_text"].(map[string]any)["description"] = "Words for a crossword, word search, scramble or quiz, one per line, the clue after a dash: \"apple - fruit that keeps the doctor away\"."
	make["theme"].(map[string]any)["description"] = "A built-in word list instead of words: animals, fruit, kitchen, space, weather, sports, music, countries, home, ocean, feelings, birthday, school; Bosnian/Croatian/Serbian: zivotinje, voce, grad."
	make["size"].(map[string]any)["description"] = "Sudoku: 4, 6 or 9. Crossword (style classic), word search, maze: squares across."
	make["text"].(map[string]any)["description"] = "Secret code (cryptogram): the message to put in code (empty = a famous quote)."
	make["message"].(map[string]any)["description"] = "Word search: a hidden message spelled by the leftover letters."
	make["mode"].(map[string]any)["description"] = "Quiz: riddles (ten old riddles) or quiz (multiple choice from words_text)."
	make["style"].(map[string]any)["description"] = "Crossword: freeform (words criss-crossed, default with words) or classic (a symmetric grid filled with English words; clues left to write)."
	make["share_on"].(map[string]any)["description"] = "true = switch its share link on, so anyone with share_url can play it without an account."
	make["picture"].(map[string]any)["description"] = "Picture puzzle (nonogram) from the library: heart, house, cat, tree, star, smile, boat, key, fish, umbrella, apple, mushroom, note, car, bird, duck."
	b.write("puzzlecraft", "make_puzzle", "Make a puzzle for the person, tested for one fair answer: give kind and what to make it from (words_text, theme, size, text…); everything else is chosen for them. Returns the puzzle with its link (and share_url, playable without an account when share_on).", "POST", "/puzzles", make, "kind")
	b.write("puzzlecraft", "add_wordlist", "Add a word list to make puzzles from: name and text with one word per line, the clue after a dash.", "POST", "/wordlists", textFields("name text").enum("language", "en", "bcs", "other"), "name text")
}

func (b *mcpCatalogBuilder) slides() {
	b.read("slides", "summary", "Get Pocket Slides at a glance: how many decks, slides, talks given and rehearsals, and the next talk (when, where, how often it was rehearsed), with links.", "/summary", "")
	b.read("slides", "decks", "List the person's presentation decks: title, number of slides, theme, the talk it is for (talk_at local YYYY-MM-DDTHH:MM, place), rehearsals and talks so far, whether it is shared or being presented now. archived = true for the archived ones.", "/decks", "archived")
	b.read("slides", "deck", "Read one deck with every slide in order: layout, title, subtitle, body (short Markdown: - points), second column, data (chart numbers, table rows, poll answers), the speaker notes and whether it is skipped when presenting.", "/decks/{id}", "")
	b.read("slides", "shows", "List talks and rehearsals (newest first): when, how long (total_ms), milliseconds per slide id, people following, questions, poll answers and the audience's rating. deck = one deck's only.", "/shows", "deck limit")
	b.read("slides", "show", "Read one talk or rehearsal: the time of every slide, the questions the audience asked (answered or not), the poll results per slide and what the audience thought.", "/shows/{id}", "")
	b.read("slides", "search", "Search the person's decks by the words on their slides and in their speaker notes, with links to the slide.", "/search", "q limit")
	b.read("slides", "day", "Read one day in Pocket Slides (talks given, rehearsals, a talk planned that day) — for the journal.", "/day/{date}", "")
	b.read("slides", "layouts", "List the built-in slide layouts (and the fields each uses), themes and deck templates, to make slides that fit.", "/builtins", "")
	slideData := mcpFields{}.booleans("build").integers("correct").strings("options").field("left", map[string]any{"type": "string", "maxLength": 80}).field("right", map[string]any{"type": "string", "maxLength": 80}).field("lang", map[string]any{"type": "string", "maxLength": 20})
	slide := textFields("title subtitle body body2 notes").enum("layout", "title", "section", "bullets", "two", "image_right", "image_left", "image", "quote", "number", "chart", "table", "code", "poll", "agenda", "end", "blank").
		booleans("hidden").integers("seconds").field("data", objectSchema(slideData, ""))
	slide["body"].(map[string]any)["description"] = "Short Markdown: lines starting \"- \" are points (under 9 words each), \"1. \" numbered, two spaces in front for a point under a point; **bold**. For quote: the quote; for code: the code; for number: where the number comes from."
	slide["title"].(map[string]any)["description"] = "The heading; for number: the big number itself (\"3 in 4\", \"€2 m\"); for poll: the question."
	slide["subtitle"].(map[string]any)["description"] = "A line under the heading; for quote: who said it; for number: what it means; for section: the line above (\"Part two\")."
	slide["notes"].(map[string]any)["description"] = "Speaker notes: what to say, 2 to 5 plain sentences. Never shown to the audience."
	slideData["options"].(map[string]any)["description"] = "For poll: 2 to 8 short answers the audience picks from on their phones."
	slideData["correct"].(map[string]any)["description"] = "For poll as a quiz: the right answer, 1-based (0 = none)."
	slideData["build"].(map[string]any)["description"] = "Show the points one at a time when presenting."
	deck := textFields("title about talk_title talk_at talk_place source_ref").integers("target_minutes").
		enum("template", "blank", "talk", "lesson", "update", "pitch", "photos", "workshop").
		enum("theme", "paper", "night", "chalk", "sunrise", "studio", "ocean", "forest", "mono", "candy", "slate").
		field("slides", arraySchema(objectSchema(slide, "layout")))
	deck["talk_at"].(map[string]any)["description"] = "When the talk is given: local YYYY-MM-DDTHH:MM (or YYYY-MM-DD)."
	deck["slides"].(map[string]any)["description"] = "The slides, in order (start with a title slide, end with an end slide). Without slides the template's slides are made."
	b.write("slides", "make_deck", "Make a presentation deck for the person, with the slides given (or from a template). They present, rehearse and share it themselves in Pocket Slides. Returns it with its link.", "POST", "/decks", deck, "title")
	addSlide := mcpFields{}.integers("after")
	for k, v := range slide {
		addSlide[k] = v
	}
	addSlide["after"].(map[string]any)["description"] = "Put it after this slide id; without it, at the end."
	b.write("slides", "add_slide", "Add one slide to one of the person's decks.", "POST", "/decks/{id}/slides", addSlide, "layout")
	b.write("slides", "update_slide", "Change the supplied fields of a slide this app made (slides the person made themselves are left alone).", "PUT", "/slides/{id}", slide, "")
}

func (b *mcpCatalogBuilder) math() {
	b.read("math", "summary", "Get Pocket Math at a glance: notebooks, own calculations, kept answers and today's practice (each child and the person against the daily goal), with links.", "/summary", "")
	b.read("math", "calculate", "Work out sums exactly with Pocket Math's calculator — use it instead of doing arithmetic yourself. Units (5 km + 300 m, 60 mph to km/h, 6.5 L/100km to mpg, 3 kW * 2 h * 0.19 BAM/kWh), money with the European Central Bank's rates (100 EUR in BAM, $20 + €15), percentages (20% of 80, 50 + 10%, 10% off 80), dates (2026-12-25 - today, today + 3 weeks), names on earlier lines (x = 4 then x^2 + 1, several lines separated by newlines), equations solved (2x + 3 = 11, solve(x^2 = 9)), statistics (mean(…), sd(…)), loans (pmt(4%/12, 240, 150000 EUR)), integral(x^2, x, 0, 3), and 'to fraction|hex|%'. result.text is the answer as the person reads it; result.number and result.unit the value. angle = deg (default) or rad for sin/cos/tan.", "/calculate", "q angle")
	b.read("math", "convert", "Convert an amount between units or currencies (value, from, to), e.g. 5 km to mi, 180 °C to °F, 100 EUR to BAM.", "/convert", "value from to")
	b.read("math", "formulas", "List the ready calculations: the person's own (mine) and the built-ins (library: paint, floor, wallpaper, loan, savings, vat, tip, trip_fuel, bmi, calories, pace, days_until, quadratic, circle and more), each with its key or id, the boxes to fill in (name, label, unit, example) and the answers.", "/formulas", "q")
	b.read("math", "run", "Work out one ready calculation without keeping it: key (a built-in) or formula_id (the person's own), and values as 'name = value; name = value' using the box names from formulas (a bare number takes the box's unit; omitted boxes use their example). Returns every answer and the main one.", "/run", "key formula_id values")
	b.read("math", "answers", "List the answers the person kept from ready calculations (newest first): what for, what was filled in, the answers.", "/runs", "key formula_id limit")
	b.read("math", "notebooks", "List the person's maths notebooks (title, folder, first line, kinds of cells). q searches titles and sums.", "/notebooks", "q subject archived")
	b.read("math", "notebook", "Read one notebook: every cell in order — calculations with the answer of each line (results), text, graphs (data.exprs), geometry, tables of data (data.columns) and equations.", "/notebooks/{id}", "")
	b.read("math", "search", "Search the person's notebooks, calculations and kept answers.", "/search", "q limit")
	b.read("math", "day", "Read one day in Pocket Math (notebooks worked on, answers kept, practice rounds) — for the journal.", "/day/{date}", "")
	b.read("math", "practice", "How practice goes: per person (the person and each child) today against the goal, the streak, the last 30 days and accuracy per skill; recent rounds. learner = one child's name only.", "/practice", "learner limit")
	mcell := textFields("title body").enum("kind", "calc", "text")
	mcell["body"].(map[string]any)["description"] = "For calc: one sum per line in Pocket Math's calculator syntax (as for calculate; names set on a line are known on the lines and cells below; 'Label: 450 EUR' lines and 'total'). For text: Markdown words."
	nbook := textFields("title subject about source_ref").field("cells", arraySchema(objectSchema(mcell, "kind")))
	nbook["subject"].(map[string]any)["description"] = "A folder, like Home, Money, School, Travel."
	b.write("math", "make_notebook", "Make a maths notebook for the person with calculation and text cells; Pocket Math works every line out. Returns it with its link.", "POST", "/notebooks", nbook, "title")
	b.write("math", "add_cell", "Add a calculation or text cell at the end of one of the person's notebooks.", "POST", "/notebooks/{id}/cells", mcell, "kind")
	keep := textFields("key values_text title note").integers("formula_id")
	keep["values_text"].(map[string]any)["description"] = "The boxes as 'name = value; name = value' (box names from formulas)."
	keep["title"].(map[string]any)["description"] = "What it is for, e.g. Bedroom, the trip to Split."
	b.write("math", "keep_answer", "Work out a ready calculation (key or formula_id) and keep the answer in the person's kept answers.", "POST", "/runs", keep, "")
}

func (b *mcpCatalogBuilder) printfarm() {
	b.read("printfarm", "summary", "Get Pocket Print Farm at a glance: each 3D printer now (printing what, % done, time left, when it ends, the filament in it and how much is left), how many jobs wait, the week (done, failed, hours, grams) and filament running low, with links.", "/summary", "")
	b.read("printfarm", "farm", "Every printer as it is right now: state (idle, printing, paused, done = waiting for the bed to be cleared, stopped, error, offline), the print on it with progress, time left and ends_at, temperatures, the loaded spool, and the next job it would take.", "/farm", "")
	b.read("printfarm", "printers", "List the person's 3D printers (model, connection, bed size, materials, print hours, prints and failures).", "/printers", "archived")
	b.read("printfarm", "printer", "Read one printer: its state now, upkeep (what is due), log book, last prints and the last 30 days.", "/printers/{id}", "")
	b.read("printfarm", "queue", "List the print queue in order: what to print, how many (done of quantity), which printer or filament, for whom, by when, price. status = open (default) | done,cancelled | all.", "/jobs", "status")
	b.read("printfarm", "models", "List or search the model library (STL, 3MF, OBJ, G-code): name, tags, size, the slicer's time and grams, how often printed. q searches names, tags, designer and file names; tag=, fav=1, sort = added|name|printed.", "/models", "q tag fav sort limit")
	b.read("printfarm", "model", "Read one model: its files with what the slicer said (time, grams, layer height, settings), its prints, the settings that worked and the jobs queued for it.", "/models/{id}", "")
	b.read("printfarm", "spools", "List the filament spools on the shelf: material, brand, colour, grams left of how many, where it is, the printer it is loaded in. material= filters; archived=1 includes empty ones.", "/spools", "material archived")
	b.read("printfarm", "prints", "List prints, newest first: what, printer, status (printing, done, failed, cancelled), why it failed, time, grams, cost, rating. printer=, model=, status=done,failed, month=YYYY-MM, q=.", "/prints", "printer model status month q limit")
	b.read("printfarm", "print", "Read one print: how it went (failure reason and notes), settings, time and filament against the slicer's estimate, what it cost (filament, electricity, wear) and a price to sell one at.", "/prints/{id}", "")
	b.read("printfarm", "upkeep", "List printer upkeep: what each printer needs every so many print hours or days, what is due or soon, and the log book of upkeep and repairs.", "/upkeep", "printer")
	b.read("printfarm", "stats", "3D printing numbers: by month (prints, hours, grams, cost), per printer, per material, most printed models, why prints failed, totals and the success rate. months = 1..36 (default 12).", "/stats", "months")
	b.read("printfarm", "search", "Search the person's 3D printing: models (and their files), printers, filament, prints and queued jobs.", "/search", "q limit")
	b.read("printfarm", "day", "Read one day in Pocket Print Farm (prints done, failed or started, upkeep done, models added) — for the journal.", "/day/{date}", "")
	pjob := textFields("title material due_on for_whom notes").integers("model_id file_id printer_id filament_id quantity price")
	pjob["due_on"].(map[string]any)["description"] = "The day it is needed by, YYYY-MM-DD."
	pjob["price"].(map[string]any)["description"] = "What one piece is sold for, in cents of the person's currency (0 = not sold)."
	pjob["model_id"].(map[string]any)["description"] = "A model from models; its newest G-code is used unless file_id is given."
	b.write("printfarm", "add_job", "Put a print job in the person's queue: a model (model_id from models) or just a title, how many, a material (PLA, PETG…) or filament_id, a printer (empty = the first one free), for whom and by when. It is printed when the person starts it; this never starts a printer.", "POST", "/jobs", pjob, "")
	pspool := textFields("bought_on location notes").integers("filament_id price count").numbers("initial_g left_g")
	pspool["price"].(map[string]any)["description"] = "What one spool cost, in cents."
	b.write("printfarm", "add_spool", "Put spools of a filament the person already has (filament_id from spools) on the shelf: how many, grams on each, price, where it is.", "POST", "/spools", pspool, "filament_id")
	pweigh := textFields("note").numbers("gross_g left_g used_g")
	pweigh["gross_g"].(map[string]any)["description"] = "The spool on a scale, with the empty spool."
	b.write("printfarm", "weigh_spool", "Set how much filament is left on a spool (id): its weight on a scale (gross_g), the grams left (left_g), or grams used by a print not seen (used_g).", "POST", "/spools/{id}/weigh", pweigh, "")
}

func (b *mcpCatalogBuilder) gamebuilder() {
	// Pocket Game Builder: the game itself (doc: levels, objects, rules) is large and of no use to the assistant, so it is
	// left out of every answer; a write never shares a game.
	gbOps := []*mcpAppTool{
		b.read("gamebuilder", "summary", "Get Pocket Game Builder at a glance: how many games the person made, the latest ones (scenes, rules, shared or not, plays by others, plays this week, open to-dos, a play link when shared) and this week's plays, with links.", "/summary", ""),
		b.read("gamebuilder", "games", "List the person's games, newest change first: title, template, shared (share_on, share_url), plays, best score, how many scenes, objects and rules, open to-dos, favourite. filter = shared | fav | archived; q searches titles and descriptions.", "/games", "filter q limit"),
		b.read("gamebuilder", "game", "Read one game (id from games): title, description, template, whether it is shared and its play link, plays and best score, how many scenes, objects and rules. The game itself (levels, objects, rules) is left out.", "/games/{id}", ""),
		b.read("gamebuilder", "plays", "Read who played one game (id) and how: the plays (who, the person's own test plays or others', score, seconds, won or lost, level), the best scores table, plays per day, and whether a low score is best.", "/games/{id}/plays", ""),
		b.read("gamebuilder", "todos", "List one game's to-do list (id = the game): what is still to make or fix, done or not, with a link to the task when it went to Pocket Tasks.", "/games/{id}/todos", ""),
		b.read("gamebuilder", "templates", "List the templates a new game can start from (platformer, maze, shooter, bricks, catch, quiz, blank): what each is and its screen size.", "/templates", ""),
		b.read("gamebuilder", "stats", "Game numbers: plays per day, total plays, the most played games with their best scores, how many games, and the pictures, sounds and music in the library. days = how many days back (default 30).", "/stats", "days"),
		b.read("gamebuilder", "search", "Search the person's games (titles, descriptions and the words in them) and their library (pictures, sounds, music).", "/search", "q limit"),
		b.read("gamebuilder", "day", "Read one day in Pocket Game Builder (games started, games worked on, the person's test plays, plays by others and which games they played) — for the journal.", "/day/{date}", ""),
	}
	gbQuestion := objectSchema(mcpFields{"q": map[string]any{"type": "string", "maxLength": 500},
		"answers": map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 200}, "minItems": 2, "maxItems": 6, "description": "The right answer first, then the wrong ones."}}, "q answers")
	gbNew := textFields("title description").enum("template", "platformer", "maze", "shooter", "bricks", "catch", "quiz", "blank").enum("size", "wide", "tall", "square", "big").field("questions", arraySchema(gbQuestion))
	gbNew["questions"].(map[string]any)["description"] = "For a quiz: the questions, each with the right answer first and then the wrong ones (the game mixes them)."
	gbIdea := textFields("idea")
	gbIdea["idea"].(map[string]any)["description"] = "The game in a sentence or two, e.g. a cat catching falling fish, faster each level."
	gbOps = append(gbOps,
		b.write("gamebuilder", "make_game", "Make a new game for the person from a template (platformer, maze, shooter, bricks, catch, quiz or blank), with a title, a screen size and, for a quiz, the questions. It is the person's to change and play; this never shares it.", "POST", "/games", gbNew, "template"),
		b.write("gamebuilder", "make_game_from_idea", "Make a new game from an idea in plain words: Pocket AI picks the template and fills it in. It is the person's to try and change; this never shares it.", "POST", "/ai/game", gbIdea, "idea"),
		b.write("gamebuilder", "add_todo", "Put a line on one game's to-do list (id = the game): something to make or fix in it.", "POST", "/games/{id}/todos", textFields("text"), "text"),
	)
	for _, op := range gbOps {
		op.Omit = []string{"doc"}
	}
}
