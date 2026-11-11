package server

import (
	"net/http"
	"testing"
)

func TestDeviceWebSession(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil)
	var pair struct {
		Code string `json:"code"`
	}
	e.do("POST", "/api/v1/pair", "", nil, &pair)
	var prof Profile
	if c := e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "Hub", "platform": "ios"}, &prof); c != 201 {
		t.Fatalf("claim %d", c)
	}

	// A person's own session may not mint one: devices only.
	if c := e.do("POST", "/api/v1/auth/device/web-session", "", nil, nil); c != 403 {
		t.Fatalf("session caller: %d", c)
	}

	var ws struct {
		Cookie, Session, Domain string
		Secure                  bool
	}
	if c := e.do("POST", "/api/v1/auth/device/web-session", prof.Token, nil, &ws); c != 201 || ws.Session == "" || ws.Cookie != sessionCookie || ws.Domain != "core.test" {
		t.Fatalf("device: %d %+v", c, ws)
	}

	// The session is a real one: it signs the browser in as the device's owner.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: ws.Cookie, Value: ws.Session})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("session not accepted: %d", res.StatusCode)
	}

	// A revoked device gets nothing.
	var devs []struct {
		ID int64 `json:"id"`
	}
	e.do("GET", "/api/v1/devices", "", nil, &devs)
	for _, d := range devs {
		e.do("DELETE", "/api/v1/devices/"+itoa(d.ID), "", nil, nil)
	}
	if c := e.do("POST", "/api/v1/auth/device/web-session", prof.Token, nil, nil); c != 401 {
		t.Fatalf("revoked: %d", c)
	}
}
