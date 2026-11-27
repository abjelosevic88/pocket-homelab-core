package server

// MCP app catalog, health: fitness, nutrition.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) fitness() {
	// Pocket Fitness: the person's training log (gym workouts with exercises and sets, routines, a weekly plan, personal records); Pocket Health's runs and rides count toward the week.
	// Units are metric: weight in kg, distance_m in metres, duration_s in seconds; convert when the person uses lb or miles. started_at is UTC, date is the local day.
	b.read("fitness", "summary", "Get a short overview of training: week_done of week_goal workouts this week (from week_start, Pocket Health's runs count), streak_weeks, today's planned session (routine, time, exercises, minutes, done; null on a rest day), a workout in progress, last_workout (name, date, duration_s, set_count, volume kg, records) and the latest records, with a one-line text.", "/summary", "")
	b.read("fitness", "week", "Get one week (the week date YYYY-MM-DD is in; this week by default): each day's workouts (Pocket Health's runs and rides marked health true, with distance_m) and what was planned, upcoming days, done against goal.", "/week", "date")
	b.read("fitness", "day", "Get one date's training: workouts with each exercise's sets as text (\"80 kg × 8, 80 kg × 8\"), duration_min, sets, volume_kg and records; other (Pocket Health's runs and rides); planned (the routine planned that weekday); and a one-line text.", "/day/{date}", "")
	b.read("fitness", "workouts", "List finished workouts, newest first: from and to YYYY-MM-DD; routine = a routine id; exercise = an exercise id (only workouts with it); sets=1 adds every set (exercise_id, type normal|warmup|drop|failure, weight kg, reps, duration_s, distance_m, rpe). Each has name, date, duration_s, exercises (count), set_count, volume (kg lifted), reps, distance_m and records (how many new). Exercise names: pocket_fitness_get_workout or pocket_fitness_exercises.", "/workouts", "from to routine exercise sets limit offset")
	b.read("fitness", "get_workout", "Read one workout with every set (weight kg, reps, duration_s, distance_m, type, rpe, pr = the records a set set), exercises (by id, with name, muscle, kind), previous (each exercise's sets the time before) and the routine it came from.", "/workouts/{id}", "")
	b.read("fitness", "records", "Get personal records: records = the best per exercise and kind (weight = heaviest kg, e1rm = estimated one-rep max kg, volume = most kg × reps in a set, reps, duration, distance) with the set, workout_id and date; recent = the latest sets that set one, with a text. exercise = an exercise id for one exercise.", "/records", "exercise")
	b.read("fitness", "progress", "Get progress over weeks (weeks, 12 by default): per week workouts, Pocket Health workouts, volume kg, sets and minutes; sets per muscle in the last 30 days; body weight from Pocket Health; recent records; the streak, the weekly goal and totals (workouts, hours, volume since the first).", "/progress", "weeks")
	b.read("fitness", "routines", "List the person's routines (Push day, Leg day…) with id, notes, outdoors, the weekdays they are planned on (1 = Monday), how often done and last_done, and their exercises with the aim: sets, reps_min–reps_max, weight kg, duration_s, distance_m, rest_s.", "/routines", "")
	b.read("fitness", "plan", "Get the weekly plan: [{weekday (1 = Monday … 7 = Sunday), routine_id, routine, time}]; days left out are rest days.", "/plan", "")
	b.read("fitness", "exercises", "List the exercises (built-in and the person's own): id, name, muscle, secondary muscles, equipment, kind (weight = kg × reps, reps = bodyweight reps, time = duration_s, cardio = distance_m + duration_s). archived=1 includes hidden ones.", "/exercises", "archived")
	b.read("fitness", "get_exercise", "Read one exercise with its records, its history (each workout's sets with it, newest first) and chart points per day (best kg, e1rm, volume).", "/exercises/{id}", "")
	fitnessSet := mcpFields{}.numbers("weight distance_m rpe").integers("reps duration_s").enum("type", "normal", "warmup", "drop", "failure").field("note", map[string]any{"type": "string", "maxLength": 500})
	fitnessSet["weight"].(map[string]any)["description"] = "Kilograms (convert lb: 1 lb = 0.4536 kg); for a bodyweight exercise the extra weight, if any."
	fitnessSet["distance_m"].(map[string]any)["description"] = "Metres (5 km = 5000)."
	fitnessSet["duration_s"].(map[string]any)["description"] = "Seconds (a 60 s plank = 60; 26 min = 1560)."
	fitnessSet["type"].(map[string]any)["description"] = "normal (default), warmup (never counts toward totals or records), drop or failure."
	fitnessSet["rpe"].(map[string]any)["description"] = "How hard, 1 to 10 (optional)."
	fitnessExercise := mcpFields{}.integers("exercise_id").field("name", map[string]any{"type": "string", "maxLength": 200}).enum("kind", "weight", "reps", "time", "cardio").
		field("sets", map[string]any{"type": "array", "items": objectSchema(fitnessSet, ""), "minItems": 1, "maxItems": 100})
	fitnessExercise["name"].(map[string]any)["description"] = "The exercise's name (\"bench\" finds Bench Press (Barbell)); matched to the person's exercises or the built-in list, an unknown name is added as their own. Or give exercise_id."
	fitnessExercise["kind"].(map[string]any)["description"] = "Only for a name that is new: weight, reps (bodyweight), time or cardio."
	fitnessLog := textFields("name date started_at notes source_ref").numbers("duration_min").integers("routine_id").
		field("feel", map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "How it felt, 1 to 5 (optional)."}).
		field("exercises", map[string]any{"type": "array", "items": objectSchema(fitnessExercise, "sets"), "minItems": 1, "maxItems": 50})
	fitnessLog["date"].(map[string]any)["description"] = "The local day YYYY-MM-DD (today by default)."
	fitnessLog["started_at"].(map[string]any)["description"] = "Local start time YYYY-MM-DDTHH:MM (optional)."
	fitnessLog["duration_min"].(map[string]any)["description"] = "How long it took in minutes (optional)."
	fitnessLog["routine_id"].(map[string]any)["description"] = "The routine it followed, an id from pocket_fitness_routines (optional)."
	fitnessLog["source_ref"].(map[string]any)["description"] = "Optional: your own reference; logging the same one again updates that workout instead of adding another."
	// a change, not an addition (mcpChangePosts): it also goes to Pocket Health and ticks a Pocket Habits habit, and the same source_ref again updates
	b.write("fitness", "log_workout", "Log a finished workout (\"bench 3x8 at 80, then pull-ups 10, 8\"). One entry in exercises per exercise, one entry in sets per set: weight is ALWAYS kg, so \"3x8 at 80\" is three sets {\"weight\": 80, \"reps\": 8}; bodyweight reps are {\"reps\": 10}; a plank {\"duration_s\": 60}; a 5 km run in 26 min {\"distance_m\": 5000, \"duration_s\": 1560}. Records are worked out, Only workouts that happened (not in the future). The workout goes to Pocket Health, ticks the habit and counts toward the week; the answer has the workout, new records (with text) and the week (done of goal).", "POST", "/workouts/log", fitnessLog, "exercises")
}

func (b *mcpCatalogBuilder) nutrition() {
	// Pocket Nutrition: the person's food diary (what they ate each day, by meal, with calories and macros), water, goals, their foods and saved meals.
	// Units: energy in kcal; protein, carbs, fat, fiber, sugar and sat_fat in grams; sodium in milligrams; water in millilitres. date is the local day YYYY-MM-DD; meal is breakfast, lunch, dinner or snacks.
	b.read("nutrition", "summary", "Get today's food in short: totals (kcal; protein, carbs, fat, fiber, sugar, sat_fat in g; sodium in mg; the last four null when unknown), entries (how many foods logged), water_ml, goals (kcal, protein g, carbs g, fat g, fiber g, water ml, burned = kcal burned by exercise per Pocket Health, budget = the kcal goal plus burned when the person counts exercise), remaining (kcal, g and ml left to the goals; negative = over), this week (days logged, on goal, average kcal), streak (days in a row logged) and a one-line text.", "/summary", "")
	b.read("nutrition", "day", "Get one date's food diary: meals [{meal, label, totals, entries (each food with name, qty × serving = amount in its unit g|ml|serving, kcal, protein g, carbs g, fat g, sodium mg)}], the day's totals, entries (count), water_ml, goals, remaining (left to the goals; negative = over), the day's note and a text (\"1,850 kcal of 2,300; protein 112 g, carbs 190 g, fat 64 g. Breakfast: oats, banana…\").", "/day/{date}", "")
	b.read("nutrition", "week", "Get the week a date (YYYY-MM-DD, today by default) is in: days [{date, entries, kcal, protein g, carbs g, fat g, …, water_ml}], logged (days with food logged), on_goal (days within 10% under to 5% over the kcal goal with the protein reached), avg_kcal, avg_protein, avg_carbs, avg_fat (g, over logged days) and goal (kcal).", "/week", "date")
	b.read("nutrition", "trends", "Get the last days (days, 30 by default, up to 366) day by day (kcal, macros g, water_ml), with average (kcal, protein/carbs/fat/fiber/sugar g, sodium mg, water ml over logged days), split (% of energy from protein, carbs and fat), top (the foods that brought the most kcal, with times and share %), by_meal (average kcal per meal), weight (kg, from Pocket Health), on_goal, goals and streak.", "/trends", "days")
	b.read("nutrition", "entries", "List the diary's entries in order (day, meal, position); the last 7 days by default. date = one day, or from and to YYYY-MM-DD; meal breakfast|lunch|dinner|snacks. Each entry: id, date, meal, name, food_id, qty × serving = amount in unit (g|ml|serving), kcal, protein g, carbs g, fat g, fiber g, sugar g, sat_fat g, sodium mg (null when unknown), note and source (the app that logged it).", "/entries", "date from to meal limit")
	b.read("nutrition", "foods_search", "Find foods to log by name: the person's own foods first (most logged, favourites), then the built-in list (id 0, builtin = its key). Without q, the foods the person logs most at that meal. Each food has its numbers per 100 g (unit g), per 100 ml (unit ml) or per serving (unit serving): kcal, protein/carbs/fat g, sodium mg, and servings [{label (\"1 egg\"), amount in the unit}].", "/foods/search", "q meal")
	b.read("nutrition", "meals", "List the person's saved meals (foods often eaten together, like \"usual breakfast\"): id, name, meal, notes, items (food, qty, serving, amount and numbers: kcal, macros g) and totals.", "/meals", "")
	nutritionNumbers := []string{"kcal", "protein", "carbs", "fat", "fiber", "sugar"}
	nutritionItem := mcpFields{}.field("name", map[string]any{"type": "string", "maxLength": 200}).integers("food_id").field("builtin", map[string]any{"type": "string", "maxLength": 100}).
		field("qty", map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1000}).field("serving", map[string]any{"type": "string", "maxLength": 100}).
		field("amount", map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 100000}).enum("unit", "g", "ml", "serving").
		enum("meal", "breakfast", "lunch", "dinner", "snacks").field("note", map[string]any{"type": "string", "maxLength": 500}).booleans("save")
	for _, n := range nutritionNumbers {
		nutritionItem[n] = map[string]any{"type": "number", "minimum": 0, "maximum": 20000}
	}
	nutritionItem["name"].(map[string]any)["description"] = "The food, one per item (\"egg\", \"white bread\", \"oats\"); matched to the person's foods, then the built-in list. Or give food_id / builtin from pocket_nutrition_foods_search."
	nutritionItem["qty"].(map[string]any)["description"] = "How many servings: two eggs = {\"name\": \"egg\", \"qty\": 2}; the food's first serving when none is named."
	nutritionItem["serving"].(map[string]any)["description"] = "Which serving qty counts, a label of the food's servings (\"1 slice\", \"1 cup\"); optional."
	nutritionItem["amount"].(map[string]any)["description"] = "An amount in grams or millilitres instead of qty, with unit: 60 g of oats = {\"name\": \"oats\", \"amount\": 60, \"unit\": \"g\"}."
	nutritionItem["unit"].(map[string]any)["description"] = "The unit of amount: g, ml or serving."
	nutritionItem["meal"].(map[string]any)["description"] = "Only when this item is in another meal than the rest."
	nutritionItem["kcal"].(map[string]any)["description"] = "Only for a food that is not known (an error says so): the kcal of the whole item as eaten. Then protein, carbs, fat, fiber and sugar in grams, for the whole item, if known."
	nutritionItem["save"].(map[string]any)["description"] = "With kcal: also keep it as one of the person's foods for next time."
	nutritionLog := mcpFields{}.field("date", map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "The local day YYYY-MM-DD it was eaten (today by default; not in the future)."}).
		enum("meal", "breakfast", "lunch", "dinner", "snacks").field("ref", map[string]any{"type": "string", "maxLength": 200}).
		field("items", map[string]any{"type": "array", "items": objectSchema(nutritionItem, ""), "minItems": 1, "maxItems": 40})
	nutritionLog["meal"].(map[string]any)["description"] = "breakfast, lunch, dinner or snacks (by the time of day when left out)."
	nutritionLog["ref"].(map[string]any)["description"] = "Optional: your own reference; logging the same one again changes nothing (no double entries on a retry)."
	// a change, not an addition (mcpChangePosts): the day's totals also go to Pocket Health
	b.write("nutrition", "log_food", "Log what the person ate in the food diary (\"two eggs and toast for breakfast\"). One item per food: {\"name\": \"egg\", \"qty\": 2}, {\"name\": \"toast\", \"qty\": 1}, with meal \"breakfast\". qty counts servings (2 eggs, 1 slice, 1 cup); amount + unit gives grams or millilitres instead (\"200 g of yoghurt\" = {\"name\": \"yoghurt\", \"amount\": 200, \"unit\": \"g\"}, \"300 ml milk\" = amount 300, unit ml). Known foods are worked out from the person's foods and the built-in list; a food not known (a dish, a restaurant meal) needs kcal for the whole item (and protein, carbs, fat in g if you can estimate them), else it is refused with a message saying so. The day's totals go to Pocket Health; the answer has the entries logged and the day (totals, goals, remaining kcal and macros, text).", "POST", "/entries/log", nutritionLog, "items")
	nutritionWater := mcpFields{}.field("ml", map[string]any{"type": "integer", "minimum": -5000, "maximum": 5000, "description": "Millilitres to add (a glass ≈ 250, a bottle 500); a negative amount takes some off."})
	// a change (mcpChangePosts): it alters the day's water total, which also goes to Pocket Health
	b.write("nutrition", "add_water", "Add water the person drank to a day (date YYYY-MM-DD, usually today) in millilitres: \"two glasses\" ≈ 500, \"a litre\" = 1000. The answer is the day's total water in ml.", "POST", "/water/{date}/add", nutritionWater, "ml")
}
