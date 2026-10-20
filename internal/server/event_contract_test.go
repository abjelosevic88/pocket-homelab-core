package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

func TestEventPrivacyAndPermissionExpansion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	settings, _ := e.st.GetSettings(ctx)
	type registration struct {
		Token string
		App   store.App
	}
	var money, clips registration
	for _, item := range []struct {
		slug string
		out  *registration
	}{{"money", &money}, {"clips", &clips}} {
		m := map[string]any{"slug": item.slug, "name": item.slug, "url": "http://" + item.slug, "offers": []map[string]string{{"scope": "events:read"}}}
		if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, item.out); c != 201 {
			t.Fatal(c)
		}
	}
	ev := &store.Event{Topic: "money.transaction_created", Source: "money", Payload: json.RawMessage(`{"user_email":"owner@example.com","amount":500}`)}
	if err := e.st.InsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	e.cj = nil
	read := func(want int) {
		t.Helper()
		var got []store.Event
		if c := e.do("GET", "/api/v1/events?topic=money.*", clips.Token, nil, &got); c != 200 || len(got) != want {
			t.Fatalf("events: %d %+v; want %d", c, got, want)
		}
	}
	read(0)
	if c := e.do("GET", fmt.Sprintf("/api/v1/events/%d", ev.ID), clips.Token, nil, nil); c != 404 {
		t.Fatalf("private event by id: %d", c)
	}
	m := map[string]any{"slug": "clips", "name": "Clips", "url": "http://clips", "uses": []map[string]any{{"app": "money", "scopes": []string{"events:read"}}}}
	if c := e.do("POST", "/api/v1/apps/register", clips.Token, m, nil); c != 200 {
		t.Fatal(c)
	}
	l, err := e.st.FindLink(ctx, clips.App.ID, "money", "events:read")
	if err != nil {
		t.Fatal(err)
	}
	if l.Enabled {
		t.Fatal("manifest update approved its own new permission")
	}
	read(0)
	if err := e.st.SetLinkEnabled(ctx, l.ID, true); err != nil {
		t.Fatal(err)
	}
	read(1)
	u, err := e.st.GetUserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetLinkOptOut(ctx, l.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	read(0)
	// A manifest cannot remove and re-add a permission to clear a person's denial.
	delete(m, "uses")
	e.do("POST", "/api/v1/apps/register", clips.Token, m, nil)
	m["uses"] = []map[string]any{{"app": "money", "scopes": []string{"events:read"}}}
	e.do("POST", "/api/v1/apps/register", clips.Token, m, nil)
	read(0)
}

func TestPublishDeliveryAtomicityAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	wh := &store.Webhook{Name: "test", URL: "http://127.0.0.1:1", Topics: "money.*", Enabled: true}
	if err := e.st.SaveWebhook(ctx, wh); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`CREATE TRIGGER fail_delivery BEFORE INSERT ON deliveries BEGIN SELECT RAISE(FAIL, 'simulated enqueue failure'); END`); err != nil {
		t.Fatal(err)
	}
	ev := &store.Event{Topic: "money.transaction_created", Source: "money", Payload: json.RawMessage(`{}`), PublishKey: "operation-1"}
	if err := e.s.hub.Publish(ctx, ev); err == nil {
		t.Fatal("acknowledged failed enqueue")
	}
	var n int
	if err := e.st.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE source='money'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("event escaped rollback: %d %v", n, err)
	}
	if _, err := e.st.DB.Exec(`DROP TRIGGER fail_delivery`); err != nil {
		t.Fatal(err)
	}
	if err := e.s.hub.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	id := ev.ID
	if err := e.s.hub.Publish(ctx, ev); err != nil || ev.ID != id {
		t.Fatalf("retry: %d %v", ev.ID, err)
	}
	if err := e.st.DB.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE event_id=?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate/missing delivery: %d %v", n, err)
	}
	ev.Payload = json.RawMessage(`{"changed":true}`)
	if err := e.s.hub.Publish(ctx, ev); err == nil {
		t.Fatal("accepted conflicting retry")
	}
}

// blockStream stalls the first live event while a burst overflows the wakeup
// buffer. The durable cursor must still deliver every event, once, in order.
type blockStream struct {
	*httptest.ResponseRecorder
	ready, blocked, release chan struct{}
	block                   bool
	complete                chan struct{}
	count                   int
}

func (r *blockStream) Write(b []byte) (int, error) {
	if strings.Contains(string(b), ": connected topics=") {
		close(r.ready)
	}
	if r.block && strings.HasPrefix(string(b), "id:") {
		r.block = false
		close(r.blocked)
		<-r.release
	}
	n, err := r.ResponseRecorder.Write(b)
	if strings.HasPrefix(string(b), "id:") {
		r.count++
		if r.count == 1201 {
			close(r.complete)
		}
	}
	return n, err
}
func TestSSEDurableCursor(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint("live=", live), func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
			insert := func(i int) {
				t.Helper()
				ev := &store.Event{Topic: []string{"review.a", "review.b"}[i%2], Source: "review", Payload: json.RawMessage(`{}`)}
				if err := e.s.hub.Publish(ctx, ev); err != nil {
					t.Fatal(err)
				}
			}
			if !live {
				for i := 0; i < 1201; i++ {
					insert(i)
				}
			}
			run, cancel := context.WithCancel(ctx)
			defer cancel()
			req := httptest.NewRequest("GET", "http://core.test/api/v1/events/stream?topics=review.*,review.a&after=0", nil).WithContext(run)
			for _, cookie := range e.cj {
				req.AddCookie(cookie)
			}
			w := &blockStream{ResponseRecorder: httptest.NewRecorder(), ready: make(chan struct{}), blocked: make(chan struct{}), release: make(chan struct{}), block: live, complete: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); e.s.handleStream(w, req) }()
			select {
			case <-w.ready:
			case <-time.After(10 * time.Second):
				t.Fatal("stream failed to connect")
			}
			if live {
				insert(0)
				select {
				case <-w.blocked:
				case <-time.After(5 * time.Second):
					t.Fatal("no first event")
				}
				for i := 1; i < 1201; i++ {
					insert(i)
				}
				close(w.release)
			}
			select {
			case <-w.complete:
			case <-time.After(10 * time.Second):
				cancel()
				<-done
				t.Fatal("incomplete stream")
			}
			cancel()
			<-done
			ids := []int64{}
			sc := bufio.NewScanner(strings.NewReader(w.Body.String()))
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "id: ") {
					id, err := strconv.ParseInt(strings.TrimPrefix(sc.Text(), "id: "), 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					ids = append(ids, id)
				}
			}
			if len(ids) != 1201 {
				t.Fatalf("received %d of 1201", len(ids))
			}
			for i := 1; i < len(ids); i++ {
				if ids[i] != ids[i-1]+1 {
					t.Fatalf("gap or duplicate: %v", ids[i-1:i+1])
				}
			}
		})
	}
}

func TestEventReceiptsSurviveRetention(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	ev := &store.Event{Topic: "receipt.created", Source: "receipt", PublishKey: "stable", Payload: json.RawMessage(`{"value":1}`)}
	if err := e.s.hub.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	original := ev.ID
	if _, err := e.st.DB.Exec(`DELETE FROM events WHERE source='receipt'`); err != nil {
		t.Fatal(err)
	}
	next := &store.Event{Topic: "receipt.created", Source: "receipt", Payload: json.RawMessage(`{}`)}
	if err := e.s.hub.Publish(ctx, next); err != nil || next.ID <= original {
		t.Fatal("sequence rewound", next.ID, err)
	}
	if err := e.s.hub.Publish(ctx, ev); err != nil || ev.ID != original {
		t.Fatal("retained receipt lost", ev.ID, err)
	}
	var n int
	e.st.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE source='receipt'`).Scan(&n)
	if n != 1 {
		t.Fatal("retry recreated expired event", n)
	}
}
