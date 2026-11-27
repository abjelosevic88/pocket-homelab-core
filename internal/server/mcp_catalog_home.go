package server

// MCP app catalog, the home and what is in it: garage, inventory, kitchen, home, pets, wardrobe, rooms.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) garageSummary() {
	b.read("garage", "summary", "Get a vehicle overview, recorded costs, fuel and active maintenance reminders in one call. Costs can overlap Money: never add app totals without verifying shared transaction IDs. Similar dates/amounts are only possible duplicates.", "/vehicles/{id}/overview", "")
	b.read("garage", "reports", "Get a vehicle's cost totals by year, month and category. State the range and currency, and separate maintenance costs from fuel. Avoid double counting linked Money transactions.", "/vehicles/{id}/reports", "months")
	b.read("garage", "reminders", "Get maintenance reminders across accessible vehicles.", "/reminders", "")
}

func (b *mcpCatalogBuilder) inventory() {
	b.read("inventory", "list", "Search inventory items, quantities and locations.", "/items", "q location category tag limit")
	b.read("inventory", "get", "Read an inventory item.", "/items/{id}", "")
	b.read("inventory", "locations", "List inventory locations.", "/locations", "")
	inventory := textFields("name notes location category unit barcode brand model serial purchase_date expiry_date tags").numbers("quantity min_quantity price").integers("location_id category_id")
	b.write("inventory", "create", "Add an inventory item.", "POST", "/items", inventory, "name")
	b.write("inventory", "update", "Update supplied inventory item fields.", "PUT", "/items/{id}", inventory, "")
	b.write("inventory", "adjust", "Adjust quantity by delta; negative delta records consumption.", "POST", "/items/{id}/adjust", textFields("note").numbers("delta"), "delta")
}

func (b *mcpCatalogBuilder) garage() {
	b.read("garage", "vehicles", "List your vehicles.", "/vehicles", "")
	b.read("garage", "get_vehicle", "Read a vehicle's details.", "/vehicles/{id}/", "")
	b.read("garage", "fuel", "Read fuel entries and their linked Money transaction IDs. Amounts are minor units; distinguish confirmed links from possible duplicate amounts.", "/vehicles/{vehicle_id}/fuel", "q")
	b.read("garage", "records", "Read maintenance records for a vehicle.", "/vehicles/{vehicle_id}/records", "q kind from to limit")
	b.read("garage", "notes", "Read a vehicle's notes.", "/vehicles/{vehicle_id}/notes", "")
	vehicle := textFields("name make model plate vin notes").integers("year").numbers("initial_odometer")
	b.write("garage", "create_vehicle", "Create a vehicle record.", "POST", "/vehicles", vehicle, "name")
	b.write("garage", "update_vehicle", "Update supplied vehicle details.", "PUT", "/vehicles/{id}/", textFields("name make model plate vin notes").integers("year"), "")
	b.write("garage", "add_note", "Add a maintenance or ownership note to a vehicle.", "POST", "/vehicles/{vehicle_id}/notes", textFields("title body").booleans("pinned"), "body")
}

func (b *mcpCatalogBuilder) kitchen() {
	b.read("kitchen", "recipes", "Search your recipe collection.", "/recipes", "q course cuisine tag limit")
	b.read("kitchen", "get_recipe", "Read a recipe and its ingredients.", "/recipes/{id}", "")
	b.read("kitchen", "lists", "List shopping lists.", "/lists", "")
	b.read("kitchen", "get_list", "Read a shopping list and its items.", "/lists/{id}", "")
	b.read("kitchen", "pantry", "Read pantry stock.", "/pantry", "q location category low expiring")
	ingredient := objectSchema(textFields("name unit note").numbers("qty"), "name")
	recipe := textFields("title description course cuisine tags notes").integers("servings prep_minutes cook_minutes").field("ingredients", arraySchema(ingredient)).strings("steps")
	b.write("kitchen", "create_recipe", "Create a recipe with ingredients and preparation steps.", "POST", "/recipes", recipe, "title")
	b.write("kitchen", "update_recipe", "Update supplied recipe fields. Supplied ingredient/step arrays replace those arrays.", "PUT", "/recipes/{id}", recipe, "")
	b.write("kitchen", "add_shopping_item", "Add an item to a shopping list.", "POST", "/lists/{list_id}/items", textFields("name unit note aisle").numbers("qty"), "name")
	b.write("kitchen", "update_shopping_item", "Update or check off a shopping item.", "PUT", "/lists/{list_id}/items/{id}", textFields("name unit note").numbers("qty").booleans("checked"), "")
}

func (b *mcpCatalogBuilder) home() {
	// Pocket Home: homes, appliances, upkeep (jobs), the log (records), meters and pros. Costs are minor units of the home's currency.
	b.read("home", "summary", "Get the home overview in one call: counts of overdue, due and soon upkeep, what needs doing, the next two months, recent log entries, warranties ending and meters needing a reading, and spending this year vs last year to date. Log costs can overlap Money: confirmed bookings carry money_tx_id; never add Home and Money totals without verifying it.", "/overview", "home_id")
	b.read("home", "due", "Get upkeep that is overdue, due today, due soon and coming in the next days (default 30) across your homes, each with status and days_left.", "/due", "days home_id")
	b.read("home", "homes", "List your homes with their appliance counts and open, overdue and soon upkeep.", "/homes", "")
	b.read("home", "things", "List appliances and parts of the house (boiler, roof, washing machine) with room, model, warranty and next upkeep. Filter by home, category, room or text.", "/things", "home_id category room q")
	b.read("home", "get_thing", "Read one appliance or part: details, warranty state, its upkeep, log and attachments.", "/things/{id}", "")
	b.read("home", "jobs", "List upkeep (recurring or one-off maintenance) with status (overdue, due, soon, ok, paused, done), due_on and how often it repeats.", "/jobs", "home_id thing_id q all")
	b.read("home", "get_job", "Read one piece of upkeep and the log entries written when it was done.", "/jobs/{id}", "")
	b.read("home", "records", "Read the log of repairs, maintenance, improvements, checks and purchases, newest first. cost is minor units of currency; money_tx_id is the confirmed Pocket Money booking.", "/records", "home_id thing_id pro_id kind from to q limit offset")
	b.read("home", "spending", "Get what the log cost per month, per kind, per appliance and per year. State the range and currency; avoid double counting linked Money transactions.", "/spending", "months home_id")
	b.read("home", "meters", "List electricity, water, gas and other meters with their last reading.", "/meters", "home_id")
	b.read("home", "get_meter", "Read a meter with every reading, use per calendar month (spread evenly between readings) and a yearly summary.", "/meters/{id}", "")
	b.read("home", "pros", "List the pros who work on the house (plumber, electrician) with their jobs and what they cost.", "/pros", "q")
	b.read("home", "search", "Search homes, appliances, upkeep, log entries, pros and meters.", "/search", "q limit")
	homeRecord := textFields("title date notes").integers("home_id thing_id job_id pro_id cost").booleans("money").enum("kind", "maintenance", "repair", "improvement", "inspection", "purchase", "other")
	homeRecord["cost"].(map[string]any)["description"] = "Minor units of the home's currency (8900 = 89.00)."
	homeRecord["money"].(map[string]any)["description"] = "Also create a Pocket Money transaction when the person has Money connected (default: their setting)."
	b.write("home", "log_work", "Add a log entry (a repair, service, improvement, check or purchase). Use thing_id for the appliance; the home follows from it.", "POST", "/records", homeRecord, "title")
	b.write("home", "complete_job", "Mark upkeep done: writes a log entry (log: false to skip) and moves recurring upkeep to its next date, counted from date. Send at least the date (YYYY-MM-DD).", "POST", "/jobs/{id}/done", textFields("date notes").integers("cost pro_id").booleans("log money"), "")
	b.write("home", "add_reading", "Add a meter reading. A value below the last reading needs reset (the meter was replaced).", "POST", "/meters/{id}/readings", textFields("read_on note").numbers("value").booleans("reset"), "value")
	homeJob := func() mcpFields {
		f := textFields("title notes due_on last_done").integers("home_id thing_id pro_id every_n soon_days est_cost").enum("every_unit", "day", "week", "month", "year")
		f["every_n"].(map[string]any)["description"] = "Repeat every N units; 0 = once."
		return f
	}
	b.write("home", "add_job", "Add upkeep: something to do every N days, weeks, months or years (or once, every_n 0), first due on due_on.", "POST", "/jobs", homeJob(), "title")
	b.write("home", "update_job", "Update supplied upkeep fields, or pause it.", "PUT", "/jobs/{id}", homeJob().booleans("paused"), "")
	homeThing := func() mcpFields {
		return textFields("name room brand model serial installed_on purchased_on warranty_until vendor manual_url notes").integers("home_id price").
			enum("category", "heating", "cooling", "water", "electrical", "appliance", "kitchen", "structure", "outdoor", "safety", "other")
	}
	b.write("home", "add_thing", "Add an appliance or part of the house to a home.", "POST", "/things", homeThing(), "name")
	b.write("home", "update_thing", "Update supplied appliance fields.", "PUT", "/things/{id}", homeThing().booleans("archived"), "")
}

func (b *mcpCatalogBuilder) homePlants() {
	// Pocket Home's house plants: a plant's care is upkeep (jobs with plant_id and care), done care is a log entry of kind plant.
	b.read("home", "plants", "List house plants with their care (water, fertilise, mist, rotate, prune, pests, repot), what is due and when each was last watered. Gone plants only with archived=1.", "/plants", "home_id room q archived")
	b.read("home", "get_plant", "Read one house plant: details, its care schedule and status, and its care log.", "/plants/{id}", "")
	b.write("home", "plant_done", "Mark one care done for one plant (default care water: \"I watered the monstera\"). Moves that care to its next date and writes the log; care not on the schedule is only logged.", "POST", "/plants/{id}/done",
		textFields("date notes title").integers("cost").enum("care", "water", "fertilise", "mist", "rotate", "prune", "pests", "repot"), "")
	b.write("home", "water_plants", "Mark care done for several plants at once (default water). With no plant_ids it does every plant whose care is due today or overdue (\"I watered everything\").", "POST", "/plants/done",
		textFields("date").integers("home_id").field("plant_ids", arraySchema(map[string]any{"type": "integer"})).enum("care", "water", "fertilise", "mist", "rotate", "prune", "pests", "repot"), "")
}

func (b *mcpCatalogBuilder) pets() {
	// Pocket Pets: pets, their care (vaccines, worming, flea and tick, nails, medicine), the health log, weights, vets and sitters. Costs are minor units of the pet's currency.
	b.read("pets", "summary", "Get the pets overview in one call: counts of overdue, due and soon care, what needs doing, the next two months, recent log entries, vaccines and insurance that run out, birthdays, coming trips with the care due while away, and spending this year vs last year to date. Log costs can overlap Money: confirmed bookings carry money_tx_id; never add Pets and Money totals without verifying it.", "/overview", "pet_id")
	b.read("pets", "due", "Get care that is overdue, due today, due soon and coming in the next days (default 30) across your pets, each with status and days_left.", "/due", "days pet_id")
	b.read("pets", "pets", "List your pets with species, breed, age, latest weight, vet, insurance end date and open, overdue and soon care.", "/pets", "")
	b.read("pets", "get_pet", "Read one pet: profile (microchip, passport, food, allergies, health, vet, insurance), its care, health log, weights, the latest vaccine of each name with how long it holds, and papers.", "/pets/{id}", "")
	b.read("pets", "care", "List care (vaccines, worming, flea and tick, check-ups, nails, medicine) with status (overdue, due, soon, ok, paused, done), due_on and how often it repeats.", "/care", "pet_id kind q all")
	b.read("pets", "get_care", "Read one piece of care and the log entries written when it was done.", "/care/{id}", "")
	b.read("pets", "records", "Read the health log (vet visits, vaccines with valid_until, treatments, medicine, grooming, symptoms, tests, surgery, food and supplies), newest first. cost is minor units; money_tx_id is the confirmed Pocket Money booking.", "/records", "pet_id pro_id kind from to q limit offset")
	b.read("pets", "weights", "Read weighings, newest first, each with the change in kg since the one before.", "/weights", "pet_id limit")
	b.read("pets", "spending", "Get what the pets cost per month, per kind, per pet and per year. State the range and currency; avoid double counting linked Money transactions.", "/spending", "months pet_id")
	b.read("pets", "pros", "List vets (with 24/7 emergency clinics), groomers, sitters, walkers and boarding with their visits and what they cost.", "/pros", "q")
	b.read("pets", "search", "Search pets, care, health log entries and vets/sitters.", "/search", "q limit")
	petRecord := textFields("title date notes valid_until").integers("pet_id care_id pro_id cost").booleans("money").enum("kind", "vet_visit", "vaccine", "treatment", "medication", "grooming", "symptom", "test", "surgery", "purchase", "other")
	petRecord["cost"].(map[string]any)["description"] = "Minor units of the pet's currency (4500 = 45.00)."
	petRecord["valid_until"].(map[string]any)["description"] = "For a vaccine or treatment: until when it holds (YYYY-MM-DD)."
	petRecord["money"].(map[string]any)["description"] = "Also create a Pocket Money transaction when the person has Money connected (default: their setting)."
	b.write("pets", "log_visit", "Add to a pet's health log: a vet visit, a vaccine (with valid_until), a treatment, medicine, grooming, a symptom, a test, surgery or a purchase.", "POST", "/records", petRecord, "title")
	b.write("pets", "complete_care", "Mark care done (\"Luna got her rabies shot\"): writes a log entry (log: false to skip) and moves recurring care to its next date, counted from date. A vaccine holds until the next due date unless valid_until says otherwise.", "POST", "/care/{id}/done",
		textFields("date notes valid_until").integers("cost pro_id").booleans("log money"), "")
	b.write("pets", "add_weight", "Add a weighing for a pet, in kilograms.", "POST", "/pets/{id}/weights", textFields("weighed_on note").numbers("kg"), "kg")
	petCare := func() mcpFields {
		f := textFields("title notes due_on last_done").integers("pet_id pro_id every_n soon_days est_cost").enum("every_unit", "day", "week", "month", "year").
			enum("kind", "vaccine", "worming", "flea_tick", "checkup", "dental", "grooming", "nails", "medication", "other")
		f["every_n"].(map[string]any)["description"] = "Repeat every N units; 0 = once."
		return f
	}
	b.write("pets", "add_care", "Add care for a pet: something to do every N days, weeks, months or years (or once, every_n 0), first due on due_on.", "POST", "/care", petCare(), "title")
	b.write("pets", "update_care", "Update supplied care fields, or pause it.", "PUT", "/care/{id}", petCare().booleans("paused"), "")
	petFields := func() mcpFields {
		return textFields("name breed born_on color microchip passport_no food allergies conditions insurer policy_no insured_until notes passed_on").integers("vet_id").booleans("neutered").
			enum("species", "dog", "cat", "rabbit", "rodent", "bird", "fish", "reptile", "horse", "other").enum("sex", "male", "female", "")
	}
	b.write("pets", "add_pet", "Add a pet.", "POST", "/pets", petFields(), "name")
	b.write("pets", "update_pet", "Update supplied pet fields (passed_on stops its reminders).", "PUT", "/pets/{id}", petFields().booleans("archived"), "")
}

func (b *mcpCatalogBuilder) wardrobe() {
	// Pocket Wardrobe: the person's clothes (items: category top|knit|bottom|dress|outerwear|shoes|bag|accessory|active|other, kind such as
	// shirt, jeans, sneakers, coat; colours with a family name; warmth 0-5, formality 1-5 = sporty…black tie; status ready|laundry|repair|lent|stored),
	// saved outfits (item_ids), wears (what was worn on a day, or planned when planned=true), packing lists and ideas worth buying.
	// Occasions: everyday work formal_work interview date dinner party wedding formal travel outdoors sport home. Styles: smart_casual classic
	// minimal casual street sporty business boho romantic edgy preppy colourful outdoor. Prices are in CENTS. Days are YYYY-MM-DD.
	b.read("wardrobe", "summary", "Get the wardrobe at a glance: how many pieces and outfits, how many wait in the laundry, at the tailor or are lent, whether tomorrow's outfit is planned and today's logged, the next trip to pack for and the ideas kept.", "/summary", "")
	b.read("wardrobe", "today", "What to wear on a day (date = YYYY-MM-DD, today when left out; occasion and style to override): the weather where the person is, their calendar events with the occasion each needs, a training day or a hike, the planned outfit and what was worn, scored outfit ideas (suggest.suggestions: items, score 0-100, reasons, warnings; the pieces in suggest.items/items) and tips. Name the pieces, not their ids.", "/today", "date occasion style")
	b.read("wardrobe", "suggest", "Outfit ideas from the person's own clothes for a day, an occasion and a style (date, occasion, style, n = how many, include = a piece id every idea must have, shuffle = a number for other ideas). Each has its pieces, a score and the reasons it works. Pieces in the laundry are left out.", "/suggest", "date occasion style n include shuffle")
	b.read("wardrobe", "items", "List the person's pieces (category, status, q = words such as navy or wool, gone=1 for what they no longer have): id, name, kind, colours, warmth, formality, styles, brand, size, price (cents), wears, last_worn, per_wear (cost per wear, cents), status.", "/items", "category status q gone")
	b.read("wardrobe", "get_item", "Read one piece (id): its details, the days it was worn, the outfits it is in, pieces much like it, and what it goes best with.", "/items/{id}", "")
	b.read("wardrobe", "outfits", "List the saved outfits: id, name, item_ids, occasion, style, rating, favourite, wears, last_worn (the pieces are in items).", "/outfits", "")
	b.read("wardrobe", "wears", "List what was worn and planned between two days (from, to = YYYY-MM-DD): date, planned, item_ids, occasion, event, the weather that day, notes (the pieces are in items).", "/wears", "from to")
	b.read("wardrobe", "day", "Read one day (date = YYYY-MM-DD): what was worn or planned, by name, with a picture link.", "/day/{date}", "")
	b.read("wardrobe", "packs", "List the packing lists: trip name, destination, dates, purpose, the pieces (item_id, packed) and extras.", "/packs", "")
	b.read("wardrobe", "gaps", "What is worth buying for the person's styles (style = one style id instead): pieces the style is built on that they lack, each with why, how many new outfits it makes with what they have (unlocks) and shop search links.", "/gaps", "style")
	b.read("wardrobe", "ideas", "List the ideas the person kept to buy (status idea|wish|bought|dismissed): title, kind, colour, why, unlocks, wish_url on Pocket Wishlist.", "/ideas", "status")
	b.read("wardrobe", "stats", "The wardrobe in numbers for the last days (days, 365 when left out): how much of it is worn, days logged, value, spent this year, most worn, never worn, best and worst cost per wear, colours, categories.", "/stats", "days")
	b.read("wardrobe", "search", "Find pieces and outfits by words (name, brand, colour, notes).", "/search", "q")
	wardrobeItem := textFields("name kind brand size shop notes").enum("category", "top", "knit", "bottom", "dress", "outerwear", "shoes", "bag", "accessory", "active", "other").integers("price warmth formality")
	wardrobeItem["kind"].(map[string]any)["description"] = "What it is: tshirt, shirt, blouse, sweater, cardigan, hoodie, jeans, chinos, trousers, shorts, skirt, dress, blazer, jacket, raincoat, coat, puffer, sneakers, loafers, smart_shoes, boots, heels, sandals, bag, belt, scarf… (sets the category, warmth and formality)."
	wardrobeItem["colors"] = arraySchema(objectSchema(textFields("hex name"), ""))
	wardrobeItem["colors"].(map[string]any)["description"] = "Its colours, main first: {\"name\": \"navy\"} or {\"hex\": \"#1f2b48\"}."
	wardrobeItem["price"].(map[string]any)["description"] = "What it cost, in CENTS."
	b.write("wardrobe", "add_item", "Add a piece of clothing (something the person bought or got). Returns the piece; a photo can be added in the app.", "POST", "/items", wardrobeItem, "kind")
	wardrobeWear := textFields("occasion event notes").booleans("planned")
	wardrobeWear["date"] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "The day; today when left out. A day ahead needs planned=true."}
	wardrobeWear["item_ids"] = arraySchema(map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)})
	wardrobeWear["item_ids"].(map[string]any)["description"] = "The pieces (ids from pocket_wardrobe_items or a suggestion)."
	b.write("wardrobe", "log_outfit", "Log what the person wore on a day, or plan an outfit for a day ahead (planned=true). Worn tops go to the laundry when the person has that on. Returns the wear.", "POST", "/wears", wardrobeWear, "item_ids")
}

func (b *mcpCatalogBuilder) rooms() {
	b.read("rooms", "summary", "Get Pocket Rooms at a glance: the person's floor plans with their area (inside), rooms, pieces of furniture, what is still to buy and open checklist items, the furniture they want to buy, and each plan's link.", "/summary", "")
	b.read("rooms", "plans", "List the person's floor plans (flats, houses, offices): name, kind, status (living, moving, idea, done), area_cm2 (divide by 10000 for m²), rooms_n, items_n, budget (minor units still to buy), open_todos, the Pocket Home home it belongs to. archived=1 lists the archive.", "/plans", "archived")
	b.read("rooms", "plan", "Read one plan: its floors (name, level, ceiling height in cm) with their layouts (\"As it is\" and other options; current = how it is now), and its ways in (the doors, hallways, corners, stairs and lift measured on the way into it).", "/plans/{id}", "")
	b.read("rooms", "sizes", "Read a plan's rooms and sizes per floor: each room's name, kind, size (\"4.2 m × 3.5 m\" for rectangles), area_cm2, walls to paint (wall_cm2), skirting_cm, doors, windows and daylight; the floor finishes to order with 10 % for cuts (order_cm2); the furniture with its room and what is still to buy with prices. layout=ID reads another option than the current one.", "/plans/{id}/summary", "layout")
	b.read("rooms", "layout", "Read one layout's whole drawing in centimetres (x right, y down): rooms (name, kind, inside outline points), doors and windows on their edges, furniture (type, name, centre x/y, width w, depth d, height h, turned rot degrees, still to buy) and measurements. Use pocket_rooms_sizes for areas.", "/layouts/{id}", "")
	b.read("rooms", "furniture", "List the person's measured furniture: name, type, width, depth and height in cm, status (own, want = to buy, maybe, gone), price (minor units), shop, and placed_in (the plans and rooms where it stands). q searches.", "/pieces", "status q limit")
	b.read("rooms", "get_piece", "Read one measured piece of furniture and how it does against every measured way in (fits or where it gets stuck, how to carry it).", "/pieces/{id}", "")
	b.read("rooms", "fit", "Check whether a piece of furniture gets through: give piece_id (a measured piece) or width, depth and height in cm; and route_id (a plan's measured way in, from pocket_rooms_plan) or one door (door_width, door_height, and turn = the width of the hallway you turn out of into the door). Answers per step whether it fits, how to carry it (upright, on its side, turned diagonally, tilted up round a corner) and the cm to spare. hands = cm left for hands (default the person's setting).", "/fit", "piece_id width depth height route_id door_width door_height turn hands")
	b.read("rooms", "search", "Search the person's plans, rooms (by name, on every layout, with their area) and measured furniture.", "/search", "q limit")
	b.read("rooms", "checklist", "List checklist items of the person's plans (measure, buy, book the movers): title, due date, done, the plan. plan_id narrows to one plan, open=1 only what is still to do.", "/todos", "plan_id open")
	b.read("rooms", "day", "Read one day in Pocket Rooms: checklist items due or done that day.", "/day/{date}", "")
	piece := textFields("name type category status currency url shop notes").numbers("width depth height").integers("price")
	piece["type"].(map[string]any)["description"] = "How it is drawn: sofa, sofa_l, armchair, coffee_table, tv_unit, bookcase, shelf, bed_double, bed_single, crib, nightstand, wardrobe, dresser, desk, office_chair, chair, dining_table, table_round, bench, counter, sink_unit, stove, fridge, dishwasher, washer, toilet, bathtub, shower, basin, plant, rug, lamp, piano, radiator, box."
	piece["category"].(map[string]any)["description"] = "living, sleeping, dining, kitchen, bath, work, storage, kids, outdoor or other."
	piece["status"].(map[string]any)["description"] = "own (default for the person), want (to buy, the default when you add it), maybe or gone."
	piece["price"].(map[string]any)["description"] = "Minor units (cents) of currency."
	for _, k := range []string{"width", "depth", "height"} {
		piece[k].(map[string]any)["description"] = "Centimetres (" + map[string]string{"width": "side to side", "depth": "front to back", "height": "floor to top"}[k] + ")."
	}
	b.write("rooms", "add_piece", "Add a piece of furniture with its size to the person's measured furniture (for example one they are thinking of buying), so it can be checked against their doors and placed on a plan. Returns the piece with its link.", "POST", "/pieces", piece, "name width depth height")
	b.write("rooms", "add_checklist_item", "Add an item to a plan's checklist (measure the alcove, order the sofa, book the movers), with an optional due date (YYYY-MM-DD).", "POST", "/todos", textFields("title due").integers("plan_id"), "plan_id title")
}
