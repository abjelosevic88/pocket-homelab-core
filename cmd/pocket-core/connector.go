package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const connectorUsage = `pocket-core connector [-url URL] [-token TOKEN] <type> <action>

Drives a connector through the RUNNING server's admin API, so locking,
logging and the event bus behave exactly as from the web UI. Nothing here
touches the database directly.

  -url    Core's address (default http://127.0.0.1:$PORT, i.e. from inside the container)
  -token  an admin's personal API token (pcu_…, Users → My API tokens); default $POCKET_CORE_TOKEN

Actions:
  status                          switches, last poll, last error and every person's account
  test                            reachability + the stored admin credential (read-only)
  enable | disable                the master switch (off = Core does nothing on its own)
  enable-events | disable-events  publish <type>.* events (Immich: per-person workflows; Nextcloud: box-wide webhooks)
  poll                            check once now (new accounts; re-create workflows deleted in the app)
  recreate-hooks                  delete and create every push workflow again (new secrets)
  enable-sso | disable-sso        "Sign in with Pocket Core" in the app (backs up the app's config first)
  enable-provisioning | disable-provisioning
                                  make accounts for people an admin creates from now on
  provision <email>               make or link that person's account now
  provision-all                   everyone in the service's audience who has no account yet
  remote-accounts                 the app's accounts (connectors that link by hand: Jellyfin, Nextcloud)
  link <email> <account>          tie that person to an existing app account (name or id); provisioning leaves it alone
  unlink <email>                  forget the link (and the credential Core saved for it)

Example (inside the container):
  docker exec -e POCKET_CORE_TOKEN=pcu_… pocket-core pocket-core connector immich test`

type coreClient struct {
	base, token string
	http        *http.Client
}

func (c *coreClient) do(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return res.StatusCode, fmt.Errorf("HTTP %d: %s", res.StatusCode, e.Error)
	}
	if out != nil && len(raw) > 0 {
		return res.StatusCode, json.Unmarshal(raw, out)
	}
	return res.StatusCode, nil
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func runConnector(args []string) {
	fs := flag.NewFlagSet("connector", flag.ExitOnError)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	base := fs.String("url", "http://127.0.0.1:"+port, "Core's address")
	token := fs.String("token", os.Getenv("POCKET_CORE_TOKEN"), "admin personal API token (pcu_…)")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, connectorUsage) }
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		fs.Usage()
		os.Exit(2)
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "an admin token is required: -token pcu_… or POCKET_CORE_TOKEN (create one under Users → My API tokens)")
		os.Exit(2)
	}
	c := &coreClient{base: strings.TrimRight(*base, "/"), token: *token, http: &http.Client{Timeout: 3 * time.Minute}}
	if err := connectorAction(c, strings.ToLower(rest[0]), rest[1], rest[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func connectorAction(c *coreClient, typ, action string, args []string) error {
	var list []struct {
		Type      string `json:"type"`
		ServiceID int64  `json:"service_id"`
	}
	if _, err := c.do("GET", "/connectors", nil, &list); err != nil {
		return err
	}
	var sid int64
	known := []string{}
	for _, it := range list {
		known = append(known, it.Type)
		if it.Type == typ {
			sid = it.ServiceID
			if sid == 0 {
				return fmt.Errorf("Core has a %s connector but no %s service: add it under Services first", typ, typ)
			}
		}
	}
	if sid == 0 {
		return fmt.Errorf("no connector for %q (known: %s)", typ, strings.Join(known, ", "))
	}
	base := fmt.Sprintf("/services/%d/connector", sid)
	put := func(body map[string]any) error {
		var out any
		if _, err := c.do("PUT", base, body, &out); err != nil {
			return err
		}
		printJSON(out)
		return nil
	}
	post := func(path string, body any) error {
		var out any
		if _, err := c.do("POST", base+path, body, &out); err != nil {
			return err
		}
		printJSON(out)
		return nil
	}
	switch action {
	case "status":
		var conn, accts any
		if _, err := c.do("GET", base, nil, &conn); err != nil {
			return err
		}
		if _, err := c.do("GET", base+"/accounts", nil, &accts); err != nil {
			return err
		}
		printJSON(map[string]any{"connector": conn, "accounts": accts})
		return nil
	case "test":
		return post("/test", nil)
	case "poll":
		return post("/poll", nil)
	case "recreate-hooks":
		return post("/hooks/recreate", nil)
	case "enable":
		return put(map[string]any{"enabled": true})
	case "disable":
		return put(map[string]any{"enabled": false})
	case "enable-events":
		return put(map[string]any{"enabled": true, "events_enabled": true})
	case "disable-events":
		return put(map[string]any{"events_enabled": false})
	case "enable-sso":
		return put(map[string]any{"enabled": true, "sso_enabled": true})
	case "disable-sso":
		return put(map[string]any{"sso_enabled": false})
	case "enable-provisioning":
		return put(map[string]any{"enabled": true, "provision_on_create": true})
	case "disable-provisioning":
		return put(map[string]any{"provision_on_create": false})
	case "provision-all":
		return post("/provision", map[string]any{"all": true})
	case "remote-accounts":
		var out any
		if _, err := c.do("GET", base+"/remote-accounts", nil, &out); err != nil {
			return err
		}
		printJSON(out)
		return nil
	case "provision", "link", "unlink":
		want := map[string]int{"provision": 1, "link": 2, "unlink": 1}[action]
		if len(args) != want {
			return fmt.Errorf("usage: %s <email>%s", action, map[bool]string{true: " <account>", false: ""}[action == "link"])
		}
		uid, err := userID(c, args[0])
		if err != nil {
			return err
		}
		switch action {
		case "provision":
			return post("/provision", map[string]any{"user_id": uid})
		case "unlink":
			var out any
			if _, err := c.do("PUT", fmt.Sprintf("%s/accounts/%d", base, uid), map[string]string{"remote_id": ""}, &out); err != nil {
				return err
			}
			printJSON(out)
			return nil
		}
		var remote []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if _, err := c.do("GET", base+"/remote-accounts", nil, &remote); err != nil {
			return err
		}
		pick := ""
		for _, a := range remote {
			if a.ID == args[1] || strings.EqualFold(a.Name, args[1]) {
				pick = a.ID
			}
		}
		if pick == "" {
			return fmt.Errorf("no %s account named or with id %q (see remote-accounts)", typ, args[1])
		}
		var out any
		if _, err := c.do("PUT", fmt.Sprintf("%s/accounts/%d", base, uid), map[string]string{"remote_id": pick}, &out); err != nil {
			return err
		}
		printJSON(out)
		return nil
	}
	return fmt.Errorf("unknown action %q\n\n%s", action, connectorUsage)
}

// userID finds a Core person by email.
func userID(c *coreClient, email string) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var users []struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	if _, err := c.do("GET", "/users", nil, &users); err != nil {
		return 0, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return u.ID, nil
		}
	}
	return 0, fmt.Errorf("no Core user with email %s: create the person under Users first", email)
}
