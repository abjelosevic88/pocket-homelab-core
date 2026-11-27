package server

// MCP app catalog, places and being out: places, weather, trails, map, travel, movement.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) places() {
	// Pocket Places: restaurants, cafés, bars, sights, parks… you love or want to try, your visits with ratings and what they cost, plans, lists, and visits Movement/Money noticed. Costs are minor units.
	b.read("places", "summary", "Get a short overview of your places: how many (want to try, been, favourites), how many visits wait for a rating, the next plans, visits and spending this month and the last visit.", "/summary", "")
	b.read("places", "overview", "Get the places overview: visits to rate (noticed in Pocket Movement and Pocket Money) and plans whose day passed, coming plans, recent visits, favourites, places waiting to be tried, coming trips with your places at the destination, visits and spending this year, top towns and lists. Visit costs can overlap Money: linked payments carry money_tx_id; never add Places and Money totals without verifying it.", "/overview", "")
	b.read("places", "places", "List or filter your places. status want|been; group eat|see|shop|stay|other; kind e.g. restaurant, cafe, bar, bakery, museum, park, viewpoint; sort name|rating|recent|added|visits|near; near=lat,lng with radius_km for places around a spot (each then has distance_m); q searches name, cuisine, town, tags and notes, accents ignored. Each place has rating, price (1-4), visits, last_visit, next_plan, spent and lists.", "/places", "status kind group city country tag q list_id favourite open sort near radius_km limit")
	b.read("places", "get_place", "Read one place: details (address, hours, website, notes, who recommended it), every visit with rating, who came and what it cost, plans, the lists it is on, your places close by, open \"how was it?\" and how often Pocket Movement saw you there.", "/places/{id}", "")
	b.read("places", "visits", "Read visits, newest first (planned=false for real ones, planned=true for plans), each with the place, rating, with_whom, minutes, spent and currency (minor units) and money_tx_id when linked to Pocket Money.", "/visits", "place_id from to planned trip_id q limit offset")
	b.read("places", "lists", "List your lists of places (date night, coffee, a trip) with how many places and how many visited.", "/lists", "")
	b.read("places", "get_list", "Read one list with its places and the note kept for each.", "/lists/{id}", "status")
	b.read("places", "to_rate", "Read what waits for an answer: stays (Pocket Movement) and payments (Pocket Money) at your places, one per place and day, and plans whose day passed (\"did you go?\").", "/inbox", "")
	b.read("places", "search", "Search places, visits and lists (accents ignored).", "/search", "q limit")
	b.read("places", "day", "Read one day: visits, plans and places added that day.", "/day/{date}", "")
	b.read("places", "spending", "Get what visits cost per currency and per month between two days. State the range and currency; avoid double counting linked Money payments.", "/spending", "from to")
	b.read("places", "trips", "List Pocket Travel trips (coming, and with back=N days past ones) with how many of your places are at each destination (want and been) and the lists made for it.", "/trips", "days back")
	placeFields := func() mcpFields {
		f := textFields("name cuisine address city country phone website map_url hours tags notes recommended_by currency").numbers("rating lat lng").integers("price").
			booleans("favourite closed").enum("status", "want", "been").
			enum("kind", "restaurant", "cafe", "bar", "bakery", "street_food", "dessert", "market", "shop", "museum", "sight", "park", "viewpoint", "beach", "nature", "activity", "hotel", "other")
		f["price"].(map[string]any)["description"] = "1 cheap to 4 expensive, 0 unknown."
		f["rating"].(map[string]any)["description"] = "1 to 5 (halves allowed), 0 none; rating a place marks it been."
		f["country"].(map[string]any)["description"] = "ISO 3166 two-letter code, e.g. BA, AT."
		return f
	}
	b.write("places", "add_place", "Save a place to your list (\"save Kibe Mahala, the restaurant with the view\"): want to go by default, or been with a rating.", "POST", "/places", placeFields().field("list_ids", arraySchema(map[string]any{"type": "integer"})), "name")
	b.write("places", "update_place", "Update supplied place fields (favourite, closed for good, a new address, notes).", "PUT", "/places/{id}", placeFields(), "")
	visitFields := func() mcpFields {
		f := textFields("date time notes with_whom currency").integers("place_id minutes spent").numbers("rating").booleans("planned money calendar")
		f["spent"].(map[string]any)["description"] = "What it cost, minor units (2400 = 24.00)."
		f["planned"].(map[string]any)["description"] = "A plan rather than a visit (default: true for a day in the future)."
		f["money"].(map[string]any)["description"] = "Also book the cost as a Pocket Money transaction (default: the person's setting; leave off when the card payment comes into Money anyway)."
		f["calendar"].(map[string]any)["description"] = "Put a plan on Pocket Calendar (default: the person's setting)."
		return f
	}
	b.write("places", "log_visit", "Log a visit to one of your places (\"we had dinner at Dveri, 5 stars, 64 KM\") or plan one (planned true, a day and a time: it goes on the calendar).", "POST", "/visits", visitFields(), "place_id")
	b.write("places", "update_visit", "Update a visit or a plan: rate it, add what it cost or who came, or turn a past plan into a visit (planned false).", "PUT", "/visits/{id}", visitFields(), "")
	b.write("places", "rate_noticed", "Answer a \"how was it?\" from to_rate: logs that visit with its time, how long and the payment linked, and the rating.", "POST", "/inbox/{id}/accept", textFields("notes with_whom").numbers("rating").integers("spent"), "")
	b.write("places", "add_list", "Make a new list of places, optionally with place_ids.", "POST", "/lists", textFields("name emoji description trip_name").integers("trip_id").field("place_ids", arraySchema(map[string]any{"type": "integer"})), "name")
	b.write("places", "add_to_list", "Put one of your places on one of your lists (id = the list).", "POST", "/lists/{id}/places", textFields("note").integers("place_id"), "place_id")
}

func (b *mcpCatalogBuilder) weather() {
	// Pocket Weather: the weather of the person's places and any spot (Open-Meteo): now, 48 hours, 10 days, air quality and pollen, warnings, and a diary of past days.
	// Units are metric: °C, mm of rain, cm of snow, km/h of wind; convert when the person uses °F, inches or mph. Times are local to the place.
	b.read("weather", "summary", "Get the weather at the person's home place: now (temp and feels in °C, humidity %, wind and gust km/h, uv, condition), today and tomorrow (t_min/t_max °C, precip mm, snow cm, precip_prob %, sunrise, sunset), active warnings, a one-line text (\"Cloudy, 3 °C to 9 °C; rain from 15:00.\") and air quality (aqi, band, pollen by day). place is null until the person adds one in Pocket Weather. location is a place id from pocket_weather_places for another place.", "/summary", "location")
	b.read("weather", "forecast", "Get the full forecast of one place (location = a place id from pocket_weather_places; home by default) or of any spot (lat and lon in degrees): now, hours ahead (48 by default; hours up to 240) with temp °C, precip_prob %, precip mm, snow cm, wind km/h, and days from yesterday to 10 days ahead (t_min/t_max °C, precip, rain mm, snow cm, wind, gust km/h, uv, sunshine_h, sunrise, sunset), plus the place's warnings and air quality.", "/forecast", "location lat lon hours")
	b.read("weather", "warnings", "Get the person's active weather warnings across their places: kind frost|ice|heat|wind|rain|snow|storm|uv|pollen|air, level advisory|warning, day, starts_at, ends_at, title, detail, value and place. history=1 adds the last 30 days.", "/warnings", "history")
	b.read("weather", "day", "Get what the weather was or will be on one date at a place (location = a place id; home by default) or any spot (lat and lon): the day's condition, t_min/t_max °C, precip mm, snow cm, wind km/h, sunshine and a one-line text, with source diary (saved), forecast or archive. Past dates come from the diary or the weather archive; dates more than 10 days ahead are not known yet.", "/day/{date}", "location lat lon")
	b.read("weather", "places", "List the places the person follows, with id (for location in the other weather tools), name, region, country, lat, lon, timezone, is_home and whether warnings are on.", "/locations", "")
	b.read("weather", "diary", "Get a month of the weather diary at a place (location = a place id; home by default; month YYYY-MM, this month by default): each day's code, t_min/t_max °C, precip mm, snow cm, wind and gust km/h, uv, sunshine_h, aqi, pollen and note, with the month's stats and the same month last year.", "/diary", "location month")
	weatherPlace := textFields("name country timezone source_ref").numbers("lat lon")
	weatherPlace["lat"].(map[string]any)["description"] = "Latitude in degrees, -90 to 90."
	weatherPlace["lon"].(map[string]any)["description"] = "Longitude in degrees, -180 to 180."
	weatherPlace["timezone"].(map[string]any)["description"] = "IANA timezone like Europe/Vienna; found from the spot when left out."
	weatherPlace["source_ref"].(map[string]any)["description"] = "Optional: your own reference for this place; adding the same one again updates that place instead of adding another."
	// a change, not an addition (mcpChangePosts): the same source_ref again updates the place it added
	b.write("weather", "add_place", "Add a place whose weather the person follows (a trip's city, a parent's town). Give its name and coordinates; it then shows in Pocket Weather with its forecast and warnings.", "POST", "/locations", weatherPlace, "name lat lon")
}

func (b *mcpCatalogBuilder) trails() {
	// Pocket Trails: the person's hikes, walks, runs and bike rides. Routes (a GPX import or drawn on the map) carry their numbers and
	// the spots along them; an outing is a route (or none) on a day with a start time, the weather, a back-by time, the people coming
	// and a packing list from their gear. Distances and heights are in METRES, weights in grams, times in minutes. Map tracks
	// (thousands of points) never reach the assistant: Omit drops them from every answer.
	trailsQuery := func(op *mcpAppTool, name string, schema map[string]any) {
		op.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)[name] = schema
	}
	trailsDay := map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	trailsActivity := []string{"hike", "walk", "run", "bike", "gravel", "mtb", "ski", "other"}
	trails := []*mcpAppTool{
		b.read("trails", "summary", "Get the small picture of the person's hikes, walks, runs and rides: who is out right now (out: the outings going on, with back_by), how many outings are planned, the next one (next: title, date, start_time, activity, distance_m and gain_m in metres, minutes, days until it), how many finished outings of the last two weeks wait for a rating (to_rate), how many routes, and this year's totals (year: outings, distance_m, gain_m, minutes, by activity, by month, summits, the highest point in metres, the longest outing).", "/summary", ""),
		b.read("trails", "today", "Read the Trails Today page. current is the outing going on now, or else the next one in the coming 14 days, in full: the outing (date, start_time, status planned|out, people, back_by, its packing list items with packed true or false, item_count and packed_count), its route's numbers and spots, the day's weather (condition, t_max and t_min in °C, precip_prob in %, wind and gust, sunrise, sunset, hours), advice (level info|caution|warning and a text: daylight, rain, storms, wind, heat), finish (when they would be done) and suggest_back_by. Then upcoming (more planned outings), recent (finished ones; rating 0 = not rated yet), unmarked (planned for a day that passed and never marked as done or called off), ideas (routes never done) and this year's totals. Distances and heights are in metres, times in minutes.", "/today", ""),
		b.read("trails", "routes", "List the person's routes (activity = hike, walk, run, bike, gravel, mtb, ski or other; q = words in the name, region or description; archived = 1 to include archived ones): id, name, activity, region, distance_m, gain_m and loss_m in metres, the lowest and highest point (min_ele, max_ele, metres), minutes (how long it takes at the person's pace), level easy|moderate|hard|expert, loop, start_name, how often it was done (done, last_done) and the next planned date (next). Find a route's id here (by its name with q) to plan an outing on it.", "/routes", "activity q archived"),
		b.read("trails", "get_route", "Read one route (id = the route): its numbers (distance_m, gain_m, loss_m and the highest and lowest point in metres, minutes at the person's pace, level), description, tags, where it starts, the spots along it (waypoints: kind such as water, hut, food, summit, viewpoint, parking, transport or danger, name, note, height ele in metres, at_m = metres from the start), and the outings on it (planned and done, with dates, people and ratings). The map track itself is left out.", "/routes/{id}", ""),
		b.read("trails", "outings", "List outings (status = planned, out, done or cancelled, several joined by commas like planned,out; from and to = YYYY-MM-DD; route_id = only those on one route; q = words; order = desc for the newest first): id, title, activity, date, start_time, status, route_id and route_name, distance_m and gain_m in metres, minutes, people, notes, back_by, rating (1-5, 0 = not rated yet), item_count and packed_count (the packing list) and the day's weather.", "/outings", "status from to route_id q order limit"),
		b.read("trails", "get_outing", "Read one outing in full (id = the outing): the outing (title, date, start_time, status planned|out|done|cancelled, people, notes, back_by, whether someone is told when the person is not back (safety, contact_name), started_at and ended_at, rating, conditions, report, the packing list items with packed true or false), its route's numbers and spots, the day's weather, advice (daylight, rain, storms, wind, heat), sunrise, sunset, finish (when they would be done), suggest_back_by and its links in other apps. Distances and heights are in metres. Map tracks are left out. A forecast older than an hour is fetched again.", "/outings/{id}", ""),
		b.read("trails", "day", "Read one day's outings (date = YYYY-MM-DD; called-off ones are left out): id, title, status, activity, start_time, distance_m and gain_m in metres (what was really covered when a track was recorded), minutes, rating, people, route and a short line (text).", "/day/{date}", ""),
		b.read("trails", "search", "Search the person's routes, outings, gear and the spots on their routes with these words (results: type route|outing|gear|spot, id, title, subtitle, url).", "/search", "q"),
		b.read("trails", "stats", "Read one year of finished outings (year = YYYY, this year when left out): outings, distance_m and gain_m in metres, minutes, by month, by activity, summits reached, the highest point (metres) and the longest outing; previous = the year before to compare; years = the years with outings.", "/stats", "year"),
		b.read("trails", "gear", "List the person's gear (q = words, archived = 1 to include archived): id, name, category, weight_g in grams, worn (worn, not carried), consumable (food, water, gas), note and on how many packing lists it is (packs).", "/gear", "q archived"),
		b.read("trails", "packs", "List the person's packing lists: id, name, activity, items (gear_id, qty and the gear), count and weight in grams (base = the pack without food and water, worn, consumable, total = what is in the pack).", "/packs", ""),
	}
	trailsQuery(trails[2], "activity", map[string]any{"type": "string", "enum": trailsActivity, "description": "Only routes for this activity."})
	trailsQuery(trails[4], "status", map[string]any{"type": "string", "pattern": `^(planned|out|done|cancelled)(,(planned|out|done|cancelled))*$`, "description": "planned, out, done or cancelled; several joined by commas."})
	trailsQuery(trails[4], "from", trailsDay)
	trailsQuery(trails[4], "to", trailsDay)
	trailsQuery(trails[4], "route_id", map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)})
	trailsQuery(trails[4], "order", map[string]any{"type": "string", "enum": []string{"asc", "desc"}})
	trailsQuery(trails[8], "year", map[string]any{"type": "string", "pattern": `^\d{4}$`, "description": "The year, YYYY; leave out for this year."})
	for _, op := range []*mcpAppTool{trails[2], trails[4], trails[8]} {
		compileMCPAppTool(op)
	}
	trailsPerson := objectSchema(textFields("name email"), "name")
	trailsPerson["properties"].(mcpFields)["name"].(map[string]any)["maxLength"] = 120
	trailsOuting := textFields("title notes").enum("activity", trailsActivity...)
	trailsOuting["title"].(map[string]any)["maxLength"] = 200
	trailsOuting["title"].(map[string]any)["description"] = "What it is called (\"Trebević with Ana\"); left out, the route's name is used."
	trailsOuting["notes"].(map[string]any)["description"] = "Anything to remember: where to meet, the bus, who brings what."
	trailsOuting["activity"].(map[string]any)["description"] = "Only when it differs from the route's (or there is no route)."
	trailsOuting["route_id"] = map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": "The route (pocket_trails_routes with q = its name gives the id). Leave out for an outing without a route, then give a title."}
	trailsOuting["date"] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "The day, YYYY-MM-DD."}
	trailsOuting["start_time"] = map[string]any{"type": "string", "pattern": `^([01]\d|2[0-3]):[0-5]\d$`, "description": "When they set off, HH:MM (24 h, local time)."}
	trailsOuting["back_by"] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}T([01]\d|2[0-3]):[0-5]\d$`, "description": "When the person means to be back, YYYY-MM-DDTHH:MM local time; Trails reminds them when it passes."}
	trailsOuting["people"] = arraySchema(trailsPerson)
	trailsOuting["people"].(map[string]any)["maxItems"] = 30
	trailsOuting["people"].(map[string]any)["description"] = "Who comes along: name, and email when known."
	plan := b.write("trails", "plan_outing", "Plan an outing for the person: a route (route_id) or a title, the day, the start time, who comes, notes and when they mean to be back (back_by). Trails adds the day's forecast and advice, and puts it in Pocket Calendar when the person has that on. It never switches on telling someone when the person is not back; they do that in Trails. Returns the planned outing with its weather and advice (distances in metres).", "POST", "/outings", trailsOuting, "date")
	plan.Body = map[string]any{"safety": false}
	plan.Schema["properties"].(mcpFields)["data"].(map[string]any)["anyOf"] = []any{map[string]any{"required": []string{"route_id"}}, map[string]any{"required": []string{"title"}}}
	compileMCPAppTool(plan)
	status := b.write("trails", "outing_status", "Mark where one of the person's outings stands (id = the outing): start = setting off now, back = \"I'm back\" (home safe: it ends the outing with the time back, so no one is warned; if someone was already told they were not back, Trails tells them all is well), done = it happened (no time back), cancel = called off. Returns the outing with a note of who was told.", "POST", "/outings/{id}/status/{action}", nil, "")
	status.Schema["properties"].(mcpFields)["action"] = map[string]any{"type": "string", "enum": []string{"start", "back", "done", "cancel"}, "description": "start, back, done or cancel."}
	compileMCPAppTool(status)
	for _, op := range append(trails, plan, status) {
		op.Omit = []string{"track", "actual_track", "controls", "preview"}
	}
}

func (b *mcpCatalogBuilder) mapApp() {
	// Pocket Map: everything with a place on one map. Most of it is read live from the person's other apps as layers (journeys and
	// stops from Pocket Movement, places, trail routes and outings, trips, Immich photos, payments, fuel stops, games, weather
	// places); their own spots, lines and saved views live in Map. A feature: id "<layer>:<ref>", layer, kind point|line, title,
	// sub, lat, lng, date, time, end, facts, url (opens it in its own app). Lines (many points) never reach the assistant: Omit.
	mapPeriod := map[string]any{"type": "string", "enum": []string{"today", "7d", "30d", "90d", "year", "all"}, "description": "today, 7d, 30d, 90d, year or all; or give from and to instead."}
	mapDay := map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	mapTools := []*mcpAppTool{
		b.read("map", "summary", "Get how many spots and lines of their own the person has on Pocket Map, their lists (name, spots, lines) and saved views (name, layers, period).", "/summary", ""),
		b.read("map", "layers", "List the map's layers: key (for layers in pocket_map_features), name, which app it comes from (group), dated (false = shown whatever the time), whether it is on for the person and whether its app works for them (works; app.state and app.fix say why not).", "/layers", ""),
		b.read("map", "features", "Read what the map shows for a time: every layer that is on (or layers = keys joined by commas, from pocket_map_layers), for period (today, 7d, 30d, 90d, year, all) or from and to (YYYY-MM-DD). Answers features (newest first: id, layer, kind point|line, title, sub, lat, lng, date, time, end for things that last, facts like distances and amounts, url to open it in its own app) and per layer the count and any error. Use it for \"where was I in May?\", \"which places did I go to on my trip?\". Distances inside facts are as the apps wrote them.", "/features", "period from to layers"),
		b.read("map", "day", "Read everything on the map on one day (date = YYYY-MM-DD): journeys, stops, outings, trips, photos, payments and the like from the layers that are on, and the person's own spots dated that day; url opens the day on the map.", "/day/{date}", ""),
		b.read("map", "spots", "List the person's own spots (list = one list's name, q = words): id, name, kind (pin home work food coffee view nature camp parking shop culture sport star), list, lat, lng, address, date, note, link_url, source (the app that added it, empty when they did).", "/spots", "list q"),
		b.read("map", "lines", "List the person's own lines (drawn or from GPX/KML files; list, q): id, name, list, distance_m in metres, date, note, color. The points are left out.", "/lines", "list q"),
		b.read("map", "views", "List the person's saved map views: id, name, layers, period, date_from, date_to, and where the map looks (lat, lng, zoom; 0 = fits to what is shown).", "/views", ""),
		b.read("map", "search", "Search the person's spots and lines, what the map showed lately, and places on OpenStreetMap (found: name, detail, lat, lng) when their online search is on.", "/search", "q"),
	}
	mapQuery := func(op *mcpAppTool, name string, schema map[string]any) {
		op.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)[name] = schema
	}
	mapQuery(mapTools[2], "period", mapPeriod)
	mapQuery(mapTools[2], "from", mapDay)
	mapQuery(mapTools[2], "to", mapDay)
	mapQuery(mapTools[2], "layers", map[string]any{"type": "string", "pattern": `^[a-z.]+(,[a-z.]+)*$`, "description": "Layer keys joined by commas; leave out for the layers that are on."})
	compileMCPAppTool(mapTools[2])
	mapSpot := textFields("name note list address date link_url source_ref").numbers("lat lng").enum("kind", "pin", "home", "work", "food", "coffee", "view", "nature", "camp", "parking", "shop", "culture", "sport", "star")
	mapSpot["name"].(map[string]any)["maxLength"] = 200
	mapSpot["lat"].(map[string]any)["description"] = "Latitude in degrees, -90 to 90."
	mapSpot["lng"].(map[string]any)["description"] = "Longitude in degrees, -180 to 180."
	mapSpot["list"].(map[string]any)["description"] = "A list to put it in (\"Want to go\", \"Summer 2026\"); pocket_map_summary has the existing ones."
	mapSpot["date"].(map[string]any)["description"] = "The day the person was there, YYYY-MM-DD; leave out when it is a place to go."
	mapSpot["source_ref"].(map[string]any)["description"] = "Optional: your own reference; the same one again answers the spot added before instead of adding another."
	mapTools = append(mapTools, b.write("map", "add_spot", "Pin a spot on the person's map (a café to remember, a place to go, where they parked): name and coordinates, optionally a kind, a list, a note, an address and the day. It is theirs to change or remove in Pocket Map.", "POST", "/spots", mapSpot, "name lat lng"))
	for _, op := range mapTools {
		op.Omit = []string{"line"}
	}
}

func (b *mcpCatalogBuilder) travel() {
	b.read("travel", "trips", "List trips you can access.", "/trips", "q archived")
	b.read("travel", "get_trip", "Read a trip's details.", "/trips/{id}/", "")
	b.read("travel", "journal", "Read a trip journal.", "/trips/{trip_id}/journal", "")
	b.read("travel", "checklists", "Read packing and travel checklists.", "/trips/{trip_id}/checklists", "")
	trip := textFields("name destination description start_date end_date currency notes")
	b.write("travel", "create_trip", "Create a trip plan without inviting other users.", "POST", "/trips", trip, "name")
	b.write("travel", "update_trip", "Update supplied trip details.", "PUT", "/trips/{id}/", trip, "")
	b.write("travel", "add_journal", "Add a trip journal entry.", "POST", "/trips/{trip_id}/journal", textFields("date title body"), "body")
}

func (b *mcpCatalogBuilder) movement() {
	b.read("movement", "summary", "What a day or a range (at most 92 days) looked like: distance, journeys with transport modes, the places you stopped at in order, countries. date = YYYY-MM-DD, or from and to.", "/summary", "date from to")
	b.read("movement", "where", "Where you were at a given time: the visit or journey then and the nearest recorded position. at = local time like 2026-10-20T14:30 (or ISO 8601).", "/where", "at")
	b.read("movement", "now", "Your newest position: the place or journey it belongs to, each phone's last report and battery, and today so far.", "/now", "")
	b.read("movement", "day", "One day's timeline in order: visits (place, times, confirmed or suggested) and journeys (mode, distance, duration).", "/day/{date}", "")
	b.read("movement", "insights", "Report for a year (year = 2026) or a range (from, to as YYYY-MM-DD): distance, days tracked, months, transport modes, countries, cities, top places, records and time at home, work and elsewhere.", "/insights", "year from to")
	b.read("movement", "countries", "Every country you have been to, with first and last day and the years.", "/countries", "")
	b.read("movement", "places", "Your places with how often and how long you were there. kind = saved or discovered; q filters by name, address or city; sort = visits, time, recent or name.", "/places", "kind q sort visited limit")
	b.read("movement", "get_place", "Read one place with its visits and when you usually go there (by hour and weekday).", "/places/{id}", "")
	b.read("movement", "visits", "List visits between start_at and end_at (ISO 8601); status = suggested, confirmed or declined.", "/visits", "start_at end_at status place_id limit")
	b.read("movement", "search", "Find your places by name, address or city.", "/search", "q limit")
	visit := textFields("name notes").integers("place_id").enum("status", "suggested", "confirmed", "declined")
	visit["name"].(map[string]any)["description"] = "Names the visit's place, so every visit there shows it."
	b.write("movement", "update_visit", "Confirm, decline or rename a visit, or move it to another of your places (place_id).", "PATCH", "/visits/{id}", visit, "")
	place := textFields("name category notes address").numbers("lat lng radius").booleans("hidden")
	place["category"].(map[string]any)["description"] = "home, work, school, family, food, shop, sport, health, travel, leisure, nature or other."
	place["radius"].(map[string]any)["description"] = "Metres, 15 to 5000 (default 100)."
	b.write("movement", "create_place", "Save a place (home, work, the gym…) with a radius: every visit inside it, past ones too, gets its name and is confirmed.", "POST", "/places", place, "lat lng")
	b.write("movement", "update_place", "Rename, recategorise, move or resize one of your places; omitted fields stay as they are.", "PUT", "/places/{id}", place, "")
}
