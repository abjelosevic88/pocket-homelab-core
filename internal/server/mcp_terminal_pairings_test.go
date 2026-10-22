package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

type terminalPairingStart struct {
	Secret string `json:"request_secret"`
	Code   string `json:"user_code"`
	URL    string `json:"verification_uri"`
}

func pairingRequest(f *oauthFixture, action string, body any, origin string, cookie bool, expected int, out any) {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	res, result := f.request("POST", "/mcp/pair/"+action, string(raw), origin, cookie)
	if res.StatusCode != expected {
		f.t.Fatalf("%s: status %d, expected %d", action, res.StatusCode, expected)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		f.t.Fatal("pairing response can be cached")
	}
	if out != nil && json.Unmarshal([]byte(result), out) != nil {
		f.t.Fatal("invalid pairing response")
	}
}
func startTerminalPairing(f *oauthFixture, scope string) terminalPairingStart {
	f.t.Helper()
	var p terminalPairingStart
	pairingRequest(f, "start", map[string]any{"name": "Remote terminal", "scope": scope, "expires_in_days": 7}, "", false, 201, &p)
	if p.Secret == "" || len(p.Code) != 11 || p.URL != oauthTestOrigin+"/ai-connections/pair" {
		f.t.Fatal("invalid start response")
	}
	return p
}
func approveTerminalPairing(f *oauthFixture, p terminalPairingStart) string {
	f.t.Helper()
	var result map[string]string
	pairingRequest(f, "approve", map[string]string{"user_code": p.Code}, oauthTestOrigin, true, 200, &result)
	if result["completion_code"] == "" {
		f.t.Fatal("no completion code")
	}
	return result["completion_code"]
}
func exchangeTerminalPairing(f *oauthFixture, p terminalPairingStart, completion string, status int) createdMCPToken {
	f.t.Helper()
	var result createdMCPToken
	pairingRequest(f, "exchange", map[string]string{"request_secret": p.Secret, "completion_code": completion}, "", false, status, &result)
	return result
}
func TestMCPTerminalPairingFullFlow(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	f.app("notes", "http://127.0.0.1:1", "all")
	p := startTerminalPairing(f, "status:read apps:read apps:write")
	var info store.MCPTerminalPairing
	pairingRequest(f, "inspect", map[string]string{"user_code": strings.ToLower(strings.ReplaceAll(p.Code, "-", ""))}, oauthTestOrigin, true, 200, &info)
	if info.Name != "Remote terminal" || info.Days != 7 || info.Scope != "status:read apps:read apps:write" {
		t.Fatal("consent details mismatch")
	}
	completion := approveTerminalPairing(f, p)
	var secretHash, codeHash, completionHash string
	if err := f.st.DB.QueryRow(`SELECT request_hash,user_code_hash,completion_hash FROM mcp_terminal_pairings`).Scan(&secretHash, &codeHash, &completionHash); err != nil {
		t.Fatal(err)
	}
	if secretHash != crypto.Hash(p.Secret) || completionHash != crypto.Hash(completion) || codeHash == p.Code {
		t.Fatal("pairing secrets not hashed")
	}
	grants, _ := f.st.ListMCPGrants(context.Background(), f.user.ID)
	if len(grants) != 0 {
		t.Fatal("approval alone created a credential")
	}
	created := exchangeTerminalPairing(f, p, completion, 200)
	if created.Token == "" || created.Connection.Scope != info.Scope {
		t.Fatal("missing credential or wrong scope")
	}
	names := toolNames(t, mcpSessionWithToken(t, f.env, created.Token))
	if !names["pocket_notes_create"] || !names["pocket_notes_get"] {
		t.Fatal("approved tools unavailable")
	}
	secret, err := f.st.GetMCPSecret(context.Background(), created.Token, "access", "")
	if err != nil || secret.UserID != f.user.ID {
		t.Fatal("credential not bound to approving account")
	}
	exchangeTerminalPairing(f, p, completion, 400)
	pairingRequest(f, "approve", map[string]string{"user_code": p.Code}, oauthTestOrigin, true, 400, nil)
	if err := f.st.DeleteMCPGrant(context.Background(), created.Connection.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if f.do("POST", "/mcp", created.Token, nil, nil) != 401 {
		t.Fatal("revoked pairing still works")
	}
}
func TestMCPTerminalPairingConsentAndBinding(t *testing.T) {
	f := newOAuthFixture(t)
	p := startTerminalPairing(f, "status:read")
	for _, action := range []string{"inspect", "approve", "deny"} {
		body := map[string]string{"user_code": p.Code}
		pairingRequest(f, action, body, oauthTestOrigin, false, 401, nil)
		pairingRequest(f, action, body, "", true, 403, nil)
		pairingRequest(f, action, body, "https://other.test", true, 403, nil)
	}
	exchangeTerminalPairing(f, p, "", 400)
	completion := approveTerminalPairing(f, p)
	other := startTerminalPairing(f, "status:read")
	exchangeTerminalPairing(f, other, completion, 400)
	exchangeTerminalPairing(f, p, "pcmp_done_wrong", 400)
	pairingRequest(f, "approve", map[string]string{"user_code": p.Code}, oauthTestOrigin, true, 400, nil)
	created := exchangeTerminalPairing(f, p, completion, 200)
	if len(toolNames(t, mcpSessionWithToken(t, f.env, created.Token))) != 4 {
		t.Fatal("status pairing escalated scopes")
	}
	pairingRequest(f, "deny", map[string]string{"user_code": other.Code}, oauthTestOrigin, true, 200, nil)
	pairingRequest(f, "approve", map[string]string{"user_code": other.Code}, oauthTestOrigin, true, 400, nil)
}
func TestMCPTerminalPairingInvalidation(t *testing.T) {
	for _, change := range []string{"expiry", "account disabled", "account revision", "app access disabled", "connections disabled"} {
		t.Run(change, func(t *testing.T) {
			f := newOAuthFixture(t)
			f.s.cfg.MCPAppToolsEnabled = true
			p := startTerminalPairing(f, "status:read apps:read")
			completion := approveTerminalPairing(f, p)
			status := 400
			var err error
			switch change {
			case "expiry":
				_, err = f.st.DB.Exec(`UPDATE mcp_terminal_pairings SET expires_at='2000-01-01T00:00:00Z'`)
			case "account disabled":
				_, err = f.st.DB.Exec(`UPDATE users SET disabled=1 WHERE id=?`, f.user.ID)
			case "account revision":
				_, err = f.st.DB.Exec(`UPDATE users SET auth_revision=auth_revision+1 WHERE id=?`, f.user.ID)
			case "app access disabled":
				f.s.cfg.MCPAppToolsEnabled = false
			case "connections disabled":
				f.s.cfg.MCPOAuthEnabled = false
				status = 403
			}
			if err != nil {
				t.Fatal(err)
			}
			exchangeTerminalPairing(f, p, completion, status)
			grants, _ := f.st.ListMCPGrants(context.Background(), f.user.ID)
			if len(grants) != 0 {
				t.Fatal("invalidated pairing issued credential")
			}
			if change == "expiry" {
				if err := f.st.PurgeExpired(context.Background()); err != nil {
					t.Fatal(err)
				}
				var count int
				f.st.DB.QueryRow(`SELECT COUNT(*) FROM mcp_terminal_pairings`).Scan(&count)
				if count != 0 {
					t.Fatal("expired pairing retained")
				}
			}
		})
	}
}
func TestMCPTerminalPairingAtomicRedemption(t *testing.T) {
	f := newOAuthFixture(t)
	p := startTerminalPairing(f, "status:read")
	completion := approveTerminalPairing(f, p)
	body, _ := json.Marshal(map[string]string{"request_secret": p.Secret, "completion_code": completion})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Post(f.srv.URL+"/mcp/pair/exchange", "application/json", strings.NewReader(string(body)))
			if err != nil {
				statuses <- 0
				return
			}
			defer res.Body.Close()
			statuses <- res.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	seen := map[int]int{}
	for code := range statuses {
		seen[code]++
	}
	if seen[200] != 1 || seen[400] != 1 {
		t.Fatalf("duplicate redemption: %v", seen)
	}
	grants, _ := f.st.ListMCPGrants(context.Background(), f.user.ID)
	if len(grants) != 1 {
		t.Fatal("duplicate grants")
	}
}
func TestMCPTerminalPairingValidationAndLimits(t *testing.T) {
	f := newOAuthFixture(t)
	for _, body := range []map[string]any{
		{"name": "", "scope": "status:read", "expires_in_days": 7},
		{"name": "terminal", "scope": "admin", "expires_in_days": 7},
		{"name": "terminal", "scope": "status:read apps:read", "expires_in_days": 7},
		{"name": "terminal", "scope": "status:read", "expires_in_days": 0},
		{"name": "terminal", "scope": "status:read", "expires_in_days": 7, "user_id": f.user.ID},
	} {
		pairingRequest(f, "start", body, "", false, 400, nil)
	}
	for range 5 {
		startTerminalPairing(f, "status:read")
	}
	pairingRequest(f, "start", map[string]any{"name": "limited", "scope": "status:read", "expires_in_days": 7}, "", false, 429, nil)
	// Authenticated approval has a separate budget from public initiation.
	var code string
	_, code, err := f.st.CreateMCPTerminalPairing(context.Background(), "direct", "status:read", 7)
	if err != nil {
		t.Fatal(err)
	}
	pairingRequest(f, "inspect", map[string]string{"user_code": code}, oauthTestOrigin, true, 200, nil)
}
func TestMCPTerminalPairingGrantLimitRollback(t *testing.T) {
	f := newOAuthFixture(t)
	for range 20 {
		if _, _, err := f.st.CreateMCPConnectionToken(context.Background(), f.user, "existing", "status:read", 7, mcpAuthorization{Resource: f.s.mcpResource(), Scope: "status:read"}); err != nil {
			t.Fatal(err)
		}
	}
	p := startTerminalPairing(f, "status:read")
	completion := approveTerminalPairing(f, p)
	exchangeTerminalPairing(f, p, completion, 409)
	grants, _ := f.st.ListMCPGrants(context.Background(), f.user.ID)
	if len(grants) != 20 {
		t.Fatal("token limit bypass")
	}
	if err := f.st.DeleteMCPGrant(context.Background(), grants[0].ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	exchangeTerminalPairing(f, p, completion, 200)
}
func TestMCPTerminalPairingHelperDownload(t *testing.T) {
	f := newOAuthFixture(t)
	res, err := http.Get(f.srv.URL + "/mcp/connect.py")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Disposition"), "pocket-connect.py") {
		t.Fatal(fmt.Sprint("helper unavailable: ", res.StatusCode))
	}
	page, err := http.Get(f.srv.URL + "/ai-connections/pair")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	if page.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(page.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("pairing page is frameable")
	}
}

func TestMCPTerminalPairingStorageBound(t *testing.T) {
	f := newOAuthFixture(t)
	_, err := f.st.DB.Exec(`WITH RECURSIVE numbers(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<1000)
INSERT INTO mcp_terminal_pairings(request_hash,user_code_hash,name,scope,days,expires_at)
SELECT 'request-'||n,'code-'||n,'pending','status:read',1,'2999-01-01T00:00:00Z' FROM numbers`)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.st.CreateMCPTerminalPairing(context.Background(), "limited", "status:read", 1)
	if err != store.ErrConflict {
		t.Fatal("unbounded pairing storage")
	}
	if _, err = f.st.DB.Exec(`UPDATE mcp_terminal_pairings SET expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.st.CreateMCPTerminalPairing(context.Background(), "new", "status:read", 1); err != nil {
		t.Fatal("expired requests blocked new pairing")
	}
}
