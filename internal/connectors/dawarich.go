package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Dawarich (1.x) connector: events, polled, for the account whose API key
// is saved on the service. Dawarich has no API to list or create users, and
// it reads OpenID settings from its environment, so there is no
// provisioning or sign-in switch; the account is linked to a person by hand
// and its events carry that person's email.
//
//	GET /api/v1/health          Test (version header)            read
//	GET /api/v1/users/me        Test, linking, Poll              read
//	GET /api/v1/points          Poll: the newest point only      read
//	GET /api/v1/visits          Poll: visits of the last 14 days read
//
// Exact positions never go on the bus: no coordinates, no addresses; only
// visit names, city and country.
type dawarich struct{}

func init() { Register(dawarich{}) }

// dawarichStale: no new point for this long means the phone stopped tracking.
const dawarichStale = 12 * time.Hour

const dawarichVisitWindow = 14 * 24 * time.Hour

func (dawarich) Type() string { return "dawarich" }
func (dawarich) Capabilities() Capabilities {
	return Capabilities{Events: true, Link: true}
}
func (dawarich) SSORedirects(string) []string { return nil }

func (dawarich) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Dawarich has no API to make accounts: create it in Dawarich (Settings → Users) and link it here")
}

func (dawarich) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Dawarich reads OpenID from its environment (OIDC_CLIENT_ID, OIDC_CLIENT_SECRET, OIDC_ISSUER, OIDC_REDIRECT_URI): set them on its container and restart it")
	}
	return nil
}

func (dawarich) get(ctx context.Context, t Target, path string, out any) (http.Header, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return nil, errors.New("the Dawarich service in Core has no API_KEY: copy it from Dawarich (Account → API key) under Services → Dawarich → Edit")
	}
	h, err := doJSON(ctx, t, "Dawarich", http.MethodGet, path, map[string]string{"Authorization": "Bearer " + k}, nil, out, k)
	if appStatus(err) == http.StatusUnauthorized {
		return h, errors.New("Dawarich rejected the stored API key (HTTP 401): copy it again from Account → API key")
	}
	return h, err
}

type dwUser struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

func (c dawarich) me(ctx context.Context, t Target) (dwUser, error) {
	var out struct {
		User dwUser `json:"user"`
	}
	_, err := c.get(ctx, t, "/api/v1/users/me", &out)
	return out.User, err
}

func (c dawarich) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	h, err := doJSON(ctx, t, "Dawarich", http.MethodGet, "/api/v1/health", nil, nil, nil)
	if err != nil {
		return info, fmt.Errorf("cannot reach Dawarich at %s: %w", t.BaseURL, err)
	}
	info.Version = h.Get("X-Dawarich-Version")
	u, err := c.me(ctx, t)
	if err != nil {
		return info, err
	}
	info.Users = 1
	info.Detail = fmt.Sprintf("the API key is %s's account: link it to its person under People so its events carry their email; accounts and sign-in are not managed from Core", u.Email)
	return info, nil
}

// RemoteAccounts: the one account Core can see, the API key's.
func (c dawarich) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	u, err := c.me(ctx, t)
	if err != nil {
		return nil, err
	}
	return []RemoteAccount{{ID: fmt.Sprint(u.ID), Name: u.Email}}, nil
}

func (c dawarich) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	u, err := c.me(ctx, t)
	if err != nil {
		return Account{}, err
	}
	if fmt.Sprint(u.ID) != remoteID {
		return Account{}, fmt.Errorf("Core only sees the Dawarich account of the saved API key (%s)", u.Email)
	}
	return Account{RemoteID: remoteID, Status: StatusLinked, Fields: map[string]string{"USER_ID": remoteID},
		Note: fmt.Sprintf("linked by hand to the Dawarich account %s", u.Email)}, nil
}

type dwPoint struct {
	Timestamp   int64  `json:"timestamp"`
	City        string `json:"city"`
	Country     string `json:"country"`
	CountryName string `json:"country_name"`
}

type dwVisit struct {
	ID        int64  `json:"id"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Duration  int64  `json:"duration"` // minutes
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type dwState struct {
	Init      bool              `json:"init"`
	LastPoint int64             `json:"last_point"`
	Stale     bool              `json:"stale"`
	City      string            `json:"city,omitempty"`
	Country   string            `json:"country,omitempty"`
	Visits    map[string]string `json:"visits"` // id → status, inside the window
}

func (c dawarich) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	u, err := c.me(ctx, t)
	if err != nil {
		return nil, state, err
	}
	uid := fmt.Sprint(u.ID)
	var pts []dwPoint
	if _, err := c.get(ctx, t, "/api/v1/points?per_page=1&order=desc", &pts); err != nil {
		return nil, state, err
	}
	now := t.Clock()
	q := url.Values{"start_at": {now.Add(-dawarichVisitWindow).UTC().Format(time.RFC3339)}, "end_at": {now.UTC().Format(time.RFC3339)}}
	var visits []dwVisit
	if _, err := c.get(ctx, t, "/api/v1/visits?"+q.Encode(), &visits); err != nil {
		return nil, state, err
	}
	var prev dwState
	_ = json.Unmarshal(state, &prev)
	cur := dwState{Init: true, LastPoint: prev.LastPoint, Stale: prev.Stale, City: prev.City, Country: prev.Country, Visits: map[string]string{}}
	var latest *dwPoint
	if len(pts) > 0 {
		latest = &pts[0]
		if latest.Timestamp > cur.LastPoint {
			cur.LastPoint = latest.Timestamp
		}
	}
	for _, v := range visits {
		cur.Visits[fmt.Sprint(v.ID)] = v.Status
	}
	base := func() map[string]any { return map[string]any{"dawarich_user_id": uid} }
	link := ""
	if pub := strings.TrimRight(t.PublicURL, "/"); pub != "" {
		link = pub + "/map"
	}
	var out []Event
	emit := func(name, subject string, p map[string]any) {
		if link != "" {
			p["url"] = link
		}
		out = append(out, Event{Name: name, Subject: subject, RemoteUserID: uid, Payload: p})
	}

	// tracking: the newest point's age
	if cur.LastPoint > 0 {
		age := now.Sub(time.Unix(cur.LastPoint, 0))
		last := time.Unix(cur.LastPoint, 0).UTC().Format(time.RFC3339)
		switch {
		case age >= dawarichStale && !prev.Stale:
			cur.Stale = true
			if prev.Init {
				p := base()
				p["last_point_at"], p["silent_hours"] = last, int64(age.Hours())
				p["title"] = fmt.Sprintf("No location from %s for %d hours", u.Email, int64(age.Hours()))
				emit("tracking_stopped", "tracking:"+uid, p)
			}
		case age < dawarichStale && prev.Stale:
			cur.Stale = false
			if prev.Init {
				p := base()
				p["last_point_at"] = last
				p["title"] = "Location tracking resumed"
				emit("tracking_resumed", "tracking:"+uid, p)
			}
		}
	}
	// city and country, when Dawarich reverse-geocodes points
	if latest != nil {
		country := strings.TrimSpace(latest.CountryName)
		if country == "" {
			country = strings.TrimSpace(latest.Country)
		}
		city := strings.TrimSpace(latest.City)
		if country != "" && country != prev.Country {
			if prev.Init && prev.Country != "" {
				p := base()
				p["country"], p["from_country"], p["title"] = country, prev.Country, "Arrived in "+country
				if city != "" {
					p["city"] = city
				}
				emit("country_entered", "country:"+country, p)
			}
			cur.Country = country
		}
		if city != "" && city != prev.City {
			if prev.Init && prev.City != "" {
				p := base()
				p["city"], p["from_city"], p["title"] = city, prev.City, "Arrived in "+city
				if country != "" {
					p["country"] = country
				}
				emit("city_entered", "city:"+city, p)
			}
			cur.City = city
		}
	}
	// visits: new ones, and ones confirmed since
	if prev.Init {
		sort.Slice(visits, func(i, j int) bool { return visits[i].StartedAt < visits[j].StartedAt })
		for _, v := range visits {
			id := fmt.Sprint(v.ID)
			was, known := prev.Visits[id]
			p := base()
			p["visit_id"], p["name"], p["status"], p["started_at"], p["ended_at"] = v.ID, v.Name, v.Status, v.StartedAt, v.EndedAt
			if v.Duration > 0 {
				p["duration_minutes"] = v.Duration
			}
			switch {
			case !known:
				p["title"] = "Visit: " + v.Name
				emit("visit_detected", "visit:"+id, p)
			case v.Status == "confirmed" && was != "confirmed":
				p["title"] = "Confirmed visit: " + v.Name
				emit("visit_confirmed", "visit:"+id, p)
			}
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}
