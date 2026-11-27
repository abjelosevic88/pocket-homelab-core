package server

// MCP app catalog, money: money, wallet, rentals, renewals, wealth, split, invoicing.
// buildMCPAppCatalog (mcp_app_catalog.go) calls these in its own order, which is the order the tools are listed in.

func (b *mcpCatalogBuilder) moneyBills() {
	b.read("money", "bills", "Get your subscriptions and recurring bills; due dates are estimates until confirmed by a transaction.", "/subscriptions", "")
}

func (b *mcpCatalogBuilder) wallet() {
	// Pocket Wallet: loyalty, membership and gift cards, tickets and boarding passes, ID papers (ID card, passport, driving licence, insurance). Pass times are local where they happen; amounts are minor units.
	b.read("wallet", "summary", "Get a short overview of the wallet: how many cards, passes and IDs, today's tickets and flights, the next pass, what runs out soon (IDs, memberships, gift cards, with days_left) and gift cards with money left.", "/summary", "")
	b.read("wallet", "overview", "Get the wallet home page: cards in the person's order, ID papers, the next passes, what runs out in 60 days, counts.", "/overview", "")
	b.read("wallet", "cards", "List or filter the wallet. group cards|passes|ids; kind loyalty|membership|gift|ticket|boarding|id|passport|licence|insurance|other; q searches name, shop, number, holder, place, booking reference; archived only|all; from/to filter passes by day; trip_id a Pocket Travel trip; expiring=YYYY-MM-DD runs out on or before; sort used|name|recent|starts|expires. Each card has number, holder, code_format/code_value (the barcode), seat, gate, ref (flight or booking), starts_at, venue, destination, expires_on, balance and points.", "/cards", "group kind q tag archived from to trip_id expiring sort limit")
	b.read("wallet", "get_card", "Read one card, pass or ID with everything on it.", "/cards/{id}", "")
	b.read("wallet", "passes", "List tickets and boarding passes: from now on by default, or between from and to, or a Pocket Travel trip's (trip_id).", "/passes", "from to trip_id limit")
	b.read("wallet", "expiring", "List what runs out within days (default the person's reminder days), with days_left; group ids for documents only.", "/expiring", "days group")
	b.read("wallet", "search", "Search the wallet by name, shop, number, holder, place or booking reference.", "/search", "q limit")
	b.read("wallet", "day", "Read one day's tickets and flights.", "/day/{date}", "")
	b.read("wallet", "balance", "Read a gift card's history: what was spent or added and what was left.", "/cards/{id}/balance", "")
	cardFields := func() mcpFields {
		f := textFields("name issuer number holder code_value color note tags valid_from expires_on starts_at ends_at venue destination seat gate ref currency source_ref").
			integers("balance points trip_id").booleans("pinned archived").
			enum("kind", "loyalty", "membership", "gift", "ticket", "boarding", "id", "passport", "licence", "insurance", "other").
			enum("code_format", "", "qr", "code128", "ean13", "aztec", "pdf417", "datamatrix", "ean8", "upca", "upce", "code39", "code93", "itf", "codabar")
		f["starts_at"].(map[string]any)["description"] = "Local time where it happens, YYYY-MM-DDTHH:MM (or YYYY-MM-DD)."
		f["code_value"].(map[string]any)["description"] = "What the barcode carries; defaults to the number when a code_format is given."
		f["balance"].(map[string]any)["description"] = "Gift cards: what is on it, minor units (5000 = 50.00)."
		f["venue"].(map[string]any)["description"] = "Where it is, or where a flight or train leaves from (\"SJJ Sarajevo\")."
		f["ref"].(map[string]any)["description"] = "Flight or train number, or booking reference."
		return f
	}
	b.write("wallet", "add_card", "Add a card, ticket, boarding pass or ID to the wallet (\"add my Lidl card 4012345\", \"my flight OS 784 on 1 Dec at 06:40, seat 14C\"). Passes go on the calendar and with their trip by the person's settings.", "POST", "/cards", cardFields(), "kind")
	b.write("wallet", "update_card", "Update supplied fields of a card this assistant added (a new gate, seat or end date).", "PUT", "/cards/{id}", cardFields(), "")
	b.write("wallet", "spend_gift_card", "Record spending from a gift card (delta negative, minor units: -1250 = 12.50 spent) or money added (positive).", "POST", "/cards/{id}/balance", textFields("note").integers("delta"), "delta")
}

func (b *mcpCatalogBuilder) rentals() {
	// Pocket Rentals: the homes the person rents out (a private landlord). Places (a house, a block of flats) hold units; a lease lets a unit
	// to tenants for a rent every period; the app makes each period's rent as a charge and payments pay the oldest charges first.
	// Repairs (requests) and inspections belong to a place. Money: every amount is in CENTS of its currency (65000 = 650.00).
	b.read("rentals", "summary", "Get the state of the person's rentals in one call: places, units, how many are let and empty, leases with rent late, rent due soon, leases ending soon, draft leases, open repairs and urgent ones, the monthly rent roll and what tenants owe now ({currency: cents}: 65000 is 650.00).", "/summary", "")
	b.read("rentals", "today", "Read the Rentals Today page, what needs the landlord: rent late (late) and due in the next days (due), each with lease_id, lease, place, tenants, due_date, amount and paid in cents, state and days late; leases ending soon (ending, with days_left), open repairs (repairs, with priority low|normal|urgent and status), inspections planned in the next 30 days, empty units (vacant), draft leases, and this month's rent expected and received ({currency: cents}).", "/today", "")
	rentalsRent := b.read("rentals", "rent", "Read one month of rent (month = YYYY-MM, this month when left out): every charge due in it (items: lease_id, lease, place, tenants, title, due_date, amount and paid in cents, currency, state paid|part|due|late|upcoming|waived, paid_on), plus expected, received and outstanding ({currency: cents}).", "/rent", "month")
	rentalsRent.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)["month"] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}$`, "description": "The month, YYYY-MM; leave out for this month."}
	compileMCPAppTool(rentalsRent)
	rentalsLeases := b.read("rentals", "leases", "List the person's leases (status active|draft|ended; property = a place id from pocket_rentals_properties): id, title, the unit and place, tenants, start_date, end_date, rent in cents, currency, frequency, due_day, deposit and its state, balance (what is owed now, cents), overdue, next_due and days_left.", "/leases", "status property")
	rentalsLeases.Schema["properties"].(mcpFields)["query"].(map[string]any)["properties"].(mcpFields)["status"] = map[string]any{"type": "string", "enum": []string{"active", "draft", "ended"}, "description": "Only leases in this state; leave out for all."}
	compileMCPAppTool(rentalsLeases)
	b.read("rentals", "get_lease", "Read one lease in full (id = the lease): the lease with its tenants, rent, deposit and balance, every charge (amount and paid in cents, state), the payments, its timeline (notes, calls, emails, visits, what happened), its repairs and inspections, and what it has in other apps (made).", "/leases/{id}", "")
	b.read("rentals", "properties", "List the person's places (archived=1 to include archived ones): id, name, kind, address, city, currency, each unit with its rent (cents) and current lease, how many units are let, the monthly rent roll (cents) and rent late. Start here to find a place's or unit's id.", "/properties", "archived")
	b.read("rentals", "get_property", "Read one place in full (id = the place): the place with its units, its leases, repairs, inspections and files, and this year's money in and out (income, expenses and net in cents).", "/properties/{id}", "")
	b.read("rentals", "tenants", "List the person's tenants: id, name, email, phone, how many leases, the current place and lease_id, and balance (what they owe now on active leases, cents).", "/tenants", "")
	rentalsRepairs := b.read("rentals", "repairs", "List the open repairs (not done or cancelled), newest first: id, title, details, the place and unit (property_id, unit_id), lease_id, priority low|normal|urgent, status new|planned|in_progress|waiting, reported_by, reported_on, planned_on, the person fixing it (pro_name) and cost in cents. property or lease = only those of one place or lease.", "/requests", "property lease limit")
	rentalsRepairs.Query = map[string]string{"open": "1"}
	b.read("rentals", "inspections", "List the inspections (property or lease = only one place's or lease's): id, kind (moving in, moving out, routine), date, status planned|done, the place, unit and lease.", "/inspections", "property lease")
	b.read("rentals", "search", "Search the person's rentals: places, leases, tenants (name, email, phone) and repairs with these words (results: kind property|lease|tenant|repair, id, title, subtitle, url).", "/search", "q limit")
	b.read("rentals", "lookup", "Find a tenant by email, for a contact from another app: the tenant and their leases (place, status, dates, rent and balance in cents). tenant is null when the address rents nothing here.", "/lookup", "email")
	rentalsRef := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991), "description": description}
	}
	rentalsRepair := textFields("title details").enum("priority", "low", "normal", "urgent")
	rentalsRepair["title"].(map[string]any)["maxLength"] = 200
	rentalsRepair["title"].(map[string]any)["description"] = "What needs fixing, in a few words (\"Kitchen tap drips\")."
	rentalsRepair["details"].(map[string]any)["description"] = "What the tenant said, where exactly, since when."
	rentalsRepair["priority"].(map[string]any)["description"] = "low, normal (default) or urgent (water, heating, a lock, anything unsafe)."
	rentalsRepair["property_id"] = rentalsRef("The place (pocket_rentals_properties).")
	rentalsRepair["unit_id"] = rentalsRef("The unit at that place, when it is about one; its current lease and tenants are added by themselves.")
	b.write("rentals", "report_repair", "Report a repair at one of the person's places: which place (and unit), what needs fixing, details and how urgent. It starts as new; when the person has it on, Pocket Rentals also puts it in Pocket Tasks. Returns the repair (request) with its id.", "POST", "/requests", rentalsRepair, "property_id title")
	rentalsLine := textFields("title body").enum("kind", "note", "call", "email", "visit")
	rentalsLine["kind"].(map[string]any)["description"] = "note (default), or a call, email or visit that happened."
	rentalsLine["lease_id"] = rentalsRef("The lease it is about (pocket_rentals_leases).")
	rentalsLine["property_id"] = rentalsRef("The place it is about, when it is not about one lease.")
	rentalsLine["request_id"] = rentalsRef("The repair it is about (pocket_rentals_repairs).")
	rentalsNote := b.write("rentals", "add_note", "Write a line on a lease's, place's or repair's timeline (give exactly one of lease_id, property_id, request_id): a note, or a call, email or visit that happened (\"Called Ana about the rent\"), with a short title and what was said in body. It changes nothing else.", "POST", "/entries", rentalsLine, "")
	rentalsNote.Schema["properties"].(mcpFields)["data"].(map[string]any)["anyOf"] = []any{
		map[string]any{"required": []string{"lease_id"}}, map[string]any{"required": []string{"property_id"}}, map[string]any{"required": []string{"request_id"}}}
	compileMCPAppTool(rentalsNote)
}

func (b *mcpCatalogBuilder) renewals() {
	// Pocket Renewals: what runs out (passports, insurance, warranties, domains, subscriptions) and when. Costs are minor units.
	b.read("renewals", "summary", "Get the renewals overview in one call: counts (expired, coming up, found automatically and waiting for a check), what has expired, what is coming up, what was found in other apps, trips a passport does not cover, recent renewals and the yearly cost. Renewal costs can overlap Money: confirmed bookings carry money_tx_id in history; never add Renewals and Money totals without verifying it.", "/overview", "")
	b.read("renewals", "due", "List what runs out within the next days (default 30) and what has already run out, soonest first, each with status and days_left.", "/due", "days")
	b.read("renewals", "list", "List renewals (passports, ID cards, insurance, vehicle registration, warranties, domains, certificates, subscriptions, contracts), soonest first. status: expired, soon, ok, undated, archived or check (found automatically). group: identity, insurance, vehicle, warranty, online, subscription, contract, health, other.", "/renewals", "q kind group holder status source before after archived")
	b.read("renewals", "get", "Read one renewal: dates, reminders, the suggested next end date, its renewal history and travel warnings.", "/renewals/{id}", "")
	b.read("renewals", "history", "Read renewals done, newest first, with the old and new date and what each cost (minor units).", "/history", "renewal_id limit")
	b.read("renewals", "search", "Search renewals by name, whose, provider, number or notes.", "/search", "q")
	b.read("renewals", "travel", "Check coming trips in Pocket Travel against passports, ID cards and visas: which run out before or soon after a trip.", "/travel", "")
	renewal := func() mcpFields {
		f := textFields("title holder provider number issued_on expires_on remind_days currency renew_url notes").integers("renew_n lead_days cost").booleans("auto_renews").
			enum("kind", "passport", "id_card", "driving_licence", "visa", "insurance", "vehicle", "warranty", "domain", "certificate", "subscription", "membership", "contract", "health", "licence", "other").
			enum("renew_unit", "day", "week", "month", "year")
		f["expires_on"].(map[string]any)["description"] = "The day it runs out, YYYY-MM-DD; leave empty when unknown."
		f["cost"].(map[string]any)["description"] = "What a renewal costs, minor units (5500 = 55.00)."
		f["remind_days"].(map[string]any)["description"] = "Days before the date to remind, e.g. \"90,30,7\"; empty = the kind's usual reminders."
		return f
	}
	b.write("renewals", "add", "Add something that runs out (a passport, an insurance policy, a warranty, a domain, a subscription). The usual term and reminders follow from the kind.", "POST", "/renewals", renewal(), "title")
	b.write("renewals", "update", "Update supplied fields of a renewal this assistant added, or archive it.", "PUT", "/renewals/{id}", renewal().booleans("archived"), "")
	b.write("renewals", "renew", "Mark a renewal renewed: the new end date (default one usual term on), what it cost and a note. Optionally books the cost in Pocket Money.", "POST", "/renewals/{id}/renew", textFields("renewed_on expires_on note number issued_on").integers("cost").booleans("money"), "")
}

func (b *mcpCatalogBuilder) wealth() {
	// Pocket Wealth: everything owned and owed, net worth over time, investments, debts, the plan. Money is in minor units of the currency it is in.
	b.read("wealth", "summary", "Get net worth in one call: what you own and owe, cash at hand, the change this month and this year, the groups (cash, investments, pension, property, vehicles, valuables, debts) and what needs an update or matures soon, in the person's currency. Linked Pocket Money balances are included once: never add Wealth and Money totals together.", "/summary", "")
	b.read("wealth", "history", "Get net worth at each month end (months back, default 24) with what was owned and owed.", "/history", "months")
	b.read("wealth", "assets", "List assets and debts with their value (whole and the person's share, own currency and the base currency), the change this month, when it was last updated, the gain, and whether it is linked to another app. group: cash, investments, pension, property, vehicles, valuables, other, debts.", "/assets", "q group closed")
	b.read("wealth", "get", "Read one asset or debt: its values over time, holdings, activity, statements kept with it and, for a debt, when it is paid off.", "/assets/{id}", "")
	b.read("wealth", "positions", "List investment holdings (funds, shares, bonds, coins) with units, price, value, cost and gain, the mix by kind and income from dividends and interest in the last 12 months.", "/positions", "")
	b.read("wealth", "activity", "List buys, sells, dividends, interest, deposits and withdrawals, newest first.", "/activity", "asset kind from to limit")
	b.read("wealth", "debts", "List debts with what is owed, the rate, the monthly payment and when each is paid off (with extra = minor units more a month: how much sooner and the interest saved).", "/debts", "extra")
	b.read("wealth", "plan", "Get the projection: net worth year by year at the current saving and expected growth, the financial independence number, when it is reached (and at what age), what it would pay a month, how long cash at hand lasts, and the milestones.", "/plan", "")
	b.read("wealth", "search", "Search assets, debts and holdings by name, bank, symbol or notes.", "/search", "q")
	b.read("wealth", "maturing", "List deposits that mature, fixed rates that end and money due back, with the date and days left.", "/maturing", "days")
	b.write("wealth", "add", "Add something owned or owed (a savings account, a pension, a flat, a car, a loan) with its current value. kind decides the group; value is minor units of currency; debts are what is still owed.", "POST", "/assets",
		textFields("name currency institution number notes matures_on bought_on date").integers("value payment bought_price invested").numbers("rate share growth").booleans("include").
			enum("kind", "cash", "savings", "deposit", "investment", "pension", "crypto", "property", "vehicle", "valuables", "business", "receivable", "other", "mortgage", "loan", "credit_card", "debt").
			enum("value_mode", "manual", "positions", "estimate"), "name kind")
	b.write("wealth", "update_value", "Type in what an asset is worth (or a debt still owes) on a day, from a statement: the whole of it in minor units of its currency; a second value the same day replaces the first.", "POST", "/assets/{id}/valuations", textFields("date note").integers("value"), "value")
	b.write("wealth", "record", "Record a buy, sell, dividend, interest, deposit, withdrawal or fee on an asset. A buy or sell needs position_id and units and updates that holding; a deposit or withdrawal updates what was put in. amount is minor units, always positive.", "POST", "/activity",
		textFields("date note currency").integers("asset_id position_id amount").numbers("units").enum("kind", "buy", "sell", "dividend", "interest", "deposit", "withdrawal", "fee"), "asset_id kind amount")
}

func (b *mcpCatalogBuilder) money() {
	b.read("money", "accounts", "List financial accounts available to you.", "/accounts", "")
	b.read("money", "categories", "List transaction categories.", "/categories", "")
	b.read("money", "summary", "Get complete spending/income totals, category breakdowns, previous-period comparison and top expenses in ONE call. Prefer this for how much did I spend, monthly summaries and spending by category; do not fetch every transaction or query each category separately. All monetary values are integer minor units of the returned home currency (divide by 100 for EUR). With no dates, uses the current budget month; month=YYYY-MM follows the configured payday. Explicit from/to (inclusive YYYY-MM-DD) select calendar/custom ranges.", "/reports/overview", "from to month year account")
	b.read("money", "transactions", "Search individual financial transactions. For aggregate spending/income or category totals, prefer pocket_money_summary. Category filters require numeric IDs, not category names.", "/transactions", "q account kind from to category limit offset")
	b.read("money", "get_transaction", "Read one transaction.", "/transactions/{id}", "")
	tx := textFields("date description payee notes currency").integers("account_id amount category_id").strings("tags").enum("kind", "expense", "income")
	b.write("money", "create_transaction", "Record an expense or income. Amount is an integer in minor currency units (for example 1250 = 12.50 EUR). This records bookkeeping; it does not transfer funds.", "POST", "/transactions", tx, "account_id amount kind date description")
	b.write("money", "update_transaction", "Update supplied transaction fields. Amount uses minor currency units.", "PUT", "/transactions/{id}", tx, "")
}

func (b *mcpCatalogBuilder) split() {
	b.read("split", "groups", "List expense-sharing groups you belong to.", "/groups", "")
	b.read("split", "get_group", "Read a group's members and details.", "/groups/{id}/", "")
	b.read("split", "expenses", "Read expenses in a group.", "/groups/{group_id}/expenses", "q from to limit offset")
	b.read("split", "balances", "Read a group's balances.", "/groups/{group_id}/balances", "")
	b.write("split", "create_group", "Create an expense-sharing group; does not invite other users.", "POST", "/groups", textFields("name description currency"), "name currency")
	share := objectSchema(mcpFields{}.integers("member_id share"), "member_id share")
	b.write("split", "create_expense", "Record a shared expense. Amount and fixed shares use minor currency units; percentage shares total 10000; include every participant in shares (share=1 for even); paid_by is a group member ID. This does not move money.", "POST", "/groups/{group_id}/expenses", textFields("title date currency category notes").integers("amount paid_by").enum("split_mode", "even", "amount", "percent", "shares").field("shares", arraySchema(share)), "title amount paid_by date split_mode shares")
}

func (b *mcpCatalogBuilder) invoicing() {
	b.read("invoicing", "clients", "List your billing clients.", "/clients", "q archived")
	b.read("invoicing", "invoices", "List your invoices.", "/invoices", "q status client_id from to limit offset")
	b.read("invoicing", "get_invoice", "Read an invoice and line items.", "/invoices/{id}", "")
	b.write("invoicing", "create_client", "Create a billing client record without sending messages.", "POST", "/clients", textFields("name contact_name email phone address1 city postal_code country currency notes"), "name")
	b.write("invoicing", "update_client", "Update supplied billing client details without sending messages.", "PUT", "/clients/{id}", textFields("name contact_name email phone address1 city postal_code country currency notes"), "")
	line := objectSchema(textFields("description unit").numbers("quantity unit_price tax_rate"), "description quantity unit_price")
	b.write("invoicing", "create_draft", "Create a draft invoice without sending it. Line prices use major currency units, for example 12.50 EUR.", "POST", "/invoices", textFields("issue_date due_date currency notes terms").integers("client_id").field("items", arraySchema(line)), "client_id issue_date due_date items")
}
