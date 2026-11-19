package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

var testPortable = &Portable{
	Exports: []PortableExport{{Path: "/api/v1/export.json", File: "notes.json", Kind: KindPocket}},
	Imports: []PortableImport{{Path: "/api/v1/import", Kind: KindPocket, ContentType: "application/json"}},
}

func TestPortableLinkAllows(t *testing.T) {
	req := func(m, p string) *http.Request { return httptest.NewRequest(m, p, nil) }
	ex, im := []string{ScopeMoveExport}, []string{ScopeMoveImport}
	cases := []struct {
		name   string
		scopes []string
		r      *http.Request
		want   bool
	}{
		{"describe with export", ex, req("GET", PortablePath), true},
		{"describe with import", im, req("GET", PortablePath), true},
		{"describe without scope", []string{"notes:read"}, req("GET", PortablePath), false},
		{"export GET", ex, req("GET", "/api/v1/export.json"), true},
		{"export needs export scope", im, req("GET", "/api/v1/export.json"), false},
		{"export never POST", ex, req("POST", "/api/v1/export.json"), false},
		{"import POST", im, req("POST", "/api/v1/import"), true},
		{"import needs import scope", ex, req("POST", "/api/v1/import"), false},
		{"import never GET", im, req("GET", "/api/v1/import"), false},
		{"other routes closed", append(ex, im...), req("GET", "/api/v1/settings"), false},
		{"no prefix tricks", ex, req("GET", "/api/v1/export.json/../settings"), false},
	}
	for _, c := range cases {
		if got := testPortable.LinkAllows(c.scopes, c.r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	var none *Portable
	if none.LinkAllows(ex, req("GET", PortablePath)) {
		t.Error("nil Portable allowed a request")
	}
	if (&Portable{Imports: testPortable.Imports}).LinkAllows(ex, req("GET", PortablePath)) {
		t.Error("export scope may describe an app that exports nothing")
	}
}

func TestPortableOffersAddedOnce(t *testing.T) {
	m := Manifest{Portable: testPortable, Offers: []Offer{{Scope: ScopeMoveExport, Name: "own words"}}}
	addPortableOffers(&m)
	addPortableOffers(&m)
	got := map[string]int{}
	for _, o := range m.Offers {
		got[o.Scope]++
	}
	if got[ScopeMoveExport] != 1 || got[ScopeMoveImport] != 1 || m.Offers[0].Name != "own words" {
		t.Fatalf("offers = %+v", m.Offers)
	}
	empty := Manifest{}
	addPortableOffers(&empty)
	if len(empty.Offers) != 0 {
		t.Fatalf("offers without Portable: %+v", empty.Offers)
	}
}

func TestPortableHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	testPortable.Handler()(rec, httptest.NewRequest("GET", PortablePath, nil))
	var out Portable
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Exports) != 1 || out.Imports[0].Path != "/api/v1/import" {
		t.Fatalf("body %s err %v", rec.Body.String(), err)
	}
}
