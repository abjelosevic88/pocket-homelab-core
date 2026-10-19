package connectors

import (
	"strings"
	"testing"
)

func TestNextcloudCalendarDecode(t *testing.T) {
	tg := Target{PublicURL: "https://cloud.example"}
	cal := func(class, ics, calURI, classification string) []byte {
		return []byte(`{"user":{"uid":"aleks"},"time":1760000000,"event":{"class":"` + strings.ReplaceAll(class, `\`, `\\`) + `",
			"calendarId":3,"calendarData":{"principaluri":"principals/users/aleks","uri":"` + calURI + `","{DAV:}displayname":"Personal"},
			"objectData":{"id":9,"uri":"x.ics","lastmodified":1760000000,"calendardata":` + quote(ics) + `,"component":"","classification":` + classification + `}}}`)
	}
	ev := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:ev-1\r\nDTSTART;TZID=Europe/Sarajevo:20261024T190000\r\nDTEND;TZID=Europe/Sarajevo:20261024T21\r\n 0000\r\nSUMMARY:Dinner\\, Belgrade\r\nRRULE:FREQ=WEEKLY\r\nBEGIN:VALARM\r\nSUMMARY:alarm\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	d, err := nextcloud{}.Decode(tg, ncCalCreated, cal(ncCalCreated, ev, "personal", "0"))
	if err != nil || d.Drop != "" || d.OwnerID != "aleks" || d.At.IsZero() {
		t.Fatalf("%+v %v", d, err)
	}
	p := d.Event.Payload
	if d.Event.Name != "calendar_event_created" || p["title"] != "Dinner, Belgrade" || p["start"] != "2026-10-24T19:00:00+02:00" || p["end"] != "2026-10-24T21:00:00+02:00" || p["recurring"] != true || p["calendar"] != "Personal" {
		t.Fatalf("event %+v", d.Event)
	}
	// private: no title on the bus
	d, _ = nextcloud{}.Decode(tg, ncCalUpdated, cal(ncCalUpdated, ev, "personal", "1"))
	if d.Event.Payload["title"] != nil || d.Event.Payload["private"] != true || d.Event.Name != "calendar_event_updated" {
		t.Fatalf("private %+v", d.Event)
	}
	// birthdays are skipped
	if d, _ = (nextcloud{}).Decode(tg, ncCalCreated, cal(ncCalCreated, ev, "contact_birthdays", "0")); d.Drop == "" {
		t.Fatal("birthday calendar not dropped")
	}
	todo := "BEGIN:VCALENDAR\nBEGIN:VTODO\nUID:t-1\nSUMMARY:Renew registration\nDUE;VALUE=DATE:20261101\nSTATUS:COMPLETED\nEND:VTODO\nEND:VCALENDAR\n"
	d, _ = nextcloud{}.Decode(tg, ncCalUpdated, cal(ncCalUpdated, todo, "tasks", "0"))
	if d.Event.Name != "task_completed" || d.Event.Payload["due"] != "2026-11-01" || d.Event.Payload["url"] != "https://cloud.example/apps/tasks" {
		t.Fatalf("task %+v", d.Event)
	}
	if d, _ = (nextcloud{}).Decode(tg, ncCalCreated, cal(ncCalUpdated, todo, "tasks", "0")); d.Drop == "" {
		t.Fatal("a delivery for another trigger must be dropped")
	}
}

func TestNextcloudFileDecode(t *testing.T) {
	tg := Target{PublicURL: "https://cloud.example/"}
	body := `{"user":{"uid":"kid"},"time":1760000000,"event":{"class":"OCP\\Files\\Events\\Node\\NodeCreatedEvent","node":{"id":7,"path":"/aleks/files/Shared/Trip/beach.jpg"}}}`
	d, err := nextcloud{}.Decode(tg, ncFileCreated, []byte(body))
	if err != nil || d.OwnerID != "aleks" {
		t.Fatalf("%+v %v", d, err)
	}
	p := d.Event.Payload
	if p["path"] != "Shared/Trip/beach.jpg" || p["folder"] != "Shared/Trip" || p["name"] != "beach.jpg" || p["by"] != "kid" || p["url"] != "https://cloud.example/f/7" {
		t.Fatalf("%+v", p)
	}
	for _, path := range []string{"/aleks/files/~$report.docx", "/aleks/files/a.txt.part", "/appdata_x/preview/1.png", "/aleks/files_trashbin/files/a"} {
		b := strings.Replace(body, "/aleks/files/Shared/Trip/beach.jpg", path, 1)
		if d, _ := (nextcloud{}).Decode(tg, ncFileCreated, []byte(b)); d.Drop == "" {
			t.Fatalf("%s not dropped", path)
		}
	}
}

func TestNcUID(t *testing.T) {
	for in, want := range map[string]string{"Sani@x.y": "sani", "a.b+tag@x": "a.b-tag", "  -x-@y": "x"} {
		if got := ncUID(strings.TrimSpace(in)); got != want {
			t.Errorf("ncUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", `\r`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
