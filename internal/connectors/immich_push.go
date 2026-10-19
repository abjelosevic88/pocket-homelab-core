package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Immich events by push.
//
// What Immich 3.3 does (server/src/services/workflow-execution.service.ts):
// on AssetCreate / AssetMetadataExtracted it looks up the workflows whose
// OWNER is the asset's owner (workflowRepository.search({userId: ownerId}))
// and runs each one's steps. A workflow belongs to whoever created it, so
// Core needs one set per person, made with that person's own key.
//
// The webhook step (immich-plugin-core#webhook) POSTs
//
//	{"type": "AssetV1", "trigger": "AssetCreate", "data": {"asset": {…}}}
//
// where asset is workflowRepository.getForAssetV1: id, ownerId, type
// (IMAGE|VIDEO|AUDIO|OTHER), originalFileName, originalPath, checksum,
// fileCreatedAt, localDateTime, createdAt, visibility, isFavorite, duration,
// livePhotoVideoId, tags and exifInfo (make, model, dateTimeOriginal, city,
// state, country, latitude, longitude, …; null until metadata extraction).
// Immich does not look at the answer and does not retry a failed call.

// ImmichWebhookMethod is the built-in plugin step that calls a URL.
const ImmichWebhookMethod = "immich-plugin-core#webhook"

// ImmichDetailsMaxAge: an AssetMetadataExtraction for an asset uploaded
// longer ago than this is a library re-scan, not news, and is dropped.
const ImmichDetailsMaxAge = 7 * 24 * time.Hour

var immichTriggers = []PushTrigger{
	{Trigger: "AssetCreate", Name: "Pocket Core · uploads"},
	{Trigger: "AssetMetadataExtraction", Name: "Pocket Core · details"},
}

func (immich) PushTriggers() []PushTrigger { return append([]PushTrigger(nil), immichTriggers...) }

func (immich) PushPerUser() bool { return true }

// personKey is the credential a push call acts with.
func personKey(t Target) (auth, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return auth{}, errors.New("no Immich API key for this person")
	}
	return auth{apiKey: k}, nil
}

func needWorkflow(err error) error {
	switch statusOf(err) {
	case http.StatusUnauthorized:
		return fmt.Errorf("Immich rejected this person's API key (HTTP 401): it is wrong or was deleted")
	case http.StatusForbidden:
		return fmt.Errorf("this person's Immich key may not manage workflows (HTTP 403): it needs %s, or \"all\"", strings.Join(ImmichWorkflowPermissions, ", "))
	}
	return err
}

func (c immich) WhoAmI(ctx context.Context, t Target) (RemoteUser, error) {
	a, err := personKey(t)
	if err != nil {
		return RemoteUser{}, err
	}
	var me immichUser
	if err := c.call(ctx, t, http.MethodGet, "/users/me", a, nil, &me); err != nil {
		return RemoteUser{}, needWorkflow(err)
	}
	return RemoteUser{ID: me.ID, Email: strings.ToLower(strings.TrimSpace(me.Email))}, nil
}

func (c immich) Subscribe(ctx context.Context, t Target, s Subscription) (string, error) {
	a, err := personKey(t)
	if err != nil {
		return "", err
	}
	if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("Core's hook URL %q is not usable: set BASE_URL to the address Immich reaches Core at", s.URL)
	}
	body := map[string]any{
		"trigger": s.Trigger, "name": s.Name, "description": s.Description, "enabled": true, "logging": false,
		"steps": []map[string]any{{
			"method":  ImmichWebhookMethod,
			"enabled": true,
			"config":  map[string]any{"url": s.URL, "method": "POST", "headerName": s.HeaderName, "headerValue": s.HeaderValue},
		}},
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, t, http.MethodPost, "/workflows", a, body, &out, s.HeaderValue); err != nil {
		return "", needWorkflow(err)
	}
	if out.ID == "" {
		return "", errors.New("Immich created the workflow but returned no id")
	}
	return out.ID, nil
}

func (c immich) Subscriptions(ctx context.Context, t Target) (map[string]string, error) {
	a, err := personKey(t)
	if err != nil {
		return nil, err
	}
	var list []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/workflows", a, nil, &list); err != nil {
		return nil, needWorkflow(err)
	}
	out := make(map[string]string, len(list))
	for _, w := range list {
		out[w.ID] = w.Name
	}
	return out, nil
}

func (c immich) Unsubscribe(ctx context.Context, t Target, remoteID string) error {
	a, err := personKey(t)
	if err != nil {
		return err
	}
	if remoteID == "" {
		return nil
	}
	err = c.call(ctx, t, http.MethodDelete, "/workflows/"+url.PathEscape(remoteID), a, nil, nil)
	switch statusOf(err) {
	case 0:
		return err
	case http.StatusNotFound, http.StatusBadRequest:
		// Immich answers 400 "Not found or no workflow.delete access" for an id that is gone
		return nil
	}
	return needWorkflow(err)
}

// immichDelivery is the webhook step's body.
type immichDelivery struct {
	Type    string `json:"type"`
	Trigger string `json:"trigger"`
	Data    struct {
		Asset *immichAsset `json:"asset"`
	} `json:"data"`
}

type immichAsset struct {
	ID               string      `json:"id"`
	OwnerID          string      `json:"ownerId"`
	Type             string      `json:"type"`
	OriginalFileName string      `json:"originalFileName"`
	FileCreatedAt    string      `json:"fileCreatedAt"`
	LocalDateTime    string      `json:"localDateTime"`
	CreatedAt        string      `json:"createdAt"`
	DeletedAt        *string     `json:"deletedAt"`
	Visibility       string      `json:"visibility"`
	IsFavorite       bool        `json:"isFavorite"`
	Duration         *string     `json:"duration"`
	LivePhotoVideoID *string     `json:"livePhotoVideoId"`
	ExifInfo         *immichExif `json:"exifInfo"`
}

type immichExif struct {
	Make             *string      `json:"make"`
	Model            *string      `json:"model"`
	LensModel        *string      `json:"lensModel"`
	DateTimeOriginal *string      `json:"dateTimeOriginal"`
	TimeZone         *string      `json:"timeZone"`
	City             *string      `json:"city"`
	State            *string      `json:"state"`
	Country          *string      `json:"country"`
	Width            *json.Number `json:"exifImageWidth"`
	Height           *json.Number `json:"exifImageHeight"`
	// latitude/longitude are deliberately not read: exact positions never go on the bus
}

func setStr(m map[string]any, k string, v *string) {
	if v != nil && strings.TrimSpace(*v) != "" {
		m[k] = strings.TrimSpace(*v)
	}
}

func (immich) Decode(t Target, trigger string, body []byte) (Delivery, error) {
	var in immichDelivery
	if err := json.Unmarshal(body, &in); err != nil {
		return Delivery{}, fmt.Errorf("not an Immich workflow delivery: %w", err)
	}
	a := in.Data.Asset
	if a == nil || a.ID == "" || a.OwnerID == "" {
		return Delivery{}, errors.New("not an Immich workflow delivery: no data.asset with id and ownerId")
	}
	d := Delivery{OwnerID: a.OwnerID, DedupeKey: a.ID + "|" + trigger}
	if in.Trigger != "" && in.Trigger != trigger {
		d.Drop = "delivery for trigger " + in.Trigger + " on the " + trigger + " hook"
		return d, nil
	}
	switch {
	case a.Visibility == "hidden":
		d.Drop = "hidden asset (the video half of a live photo)"
		return d, nil
	case a.Visibility == "locked":
		d.Drop = "asset in the locked folder"
		return d, nil
	case a.DeletedAt != nil:
		d.Drop = "asset in the trash"
		return d, nil
	}
	p := map[string]any{"immich_user_id": a.OwnerID, "asset_id": a.ID, "type": strings.ToLower(a.Type), "is_favorite": a.IsFavorite, "live_photo": a.LivePhotoVideoID != nil}
	setStr(p, "original_file_name", &a.OriginalFileName)
	setStr(p, "file_created_at", &a.FileCreatedAt)
	setStr(p, "local_date_time", &a.LocalDateTime)
	setStr(p, "uploaded_at", &a.CreatedAt)
	if a.Duration != nil && strings.Trim(*a.Duration, "0:.") != "" {
		p["duration"] = *a.Duration
	}
	if pub := strings.TrimRight(t.PublicURL, "/"); pub != "" {
		p["url"] = pub + "/photos/" + url.PathEscape(a.ID)
	}
	name := "asset_created"
	if trigger == "AssetMetadataExtraction" {
		name = "asset_processed"
		if up, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err == nil && t.Clock().Sub(up) > ImmichDetailsMaxAge {
			d.Drop = "metadata re-scan of an asset uploaded " + up.UTC().Format("2006-01-02")
			return d, nil
		}
		if x := a.ExifInfo; x != nil {
			setStr(p, "taken_at", x.DateTimeOriginal)
			setStr(p, "time_zone", x.TimeZone)
			setStr(p, "city", x.City)
			setStr(p, "state", x.State)
			setStr(p, "country", x.Country)
			setStr(p, "camera_make", x.Make)
			setStr(p, "camera_model", x.Model)
			setStr(p, "lens_model", x.LensModel)
			if x.Width != nil {
				if n, err := x.Width.Int64(); err == nil && n > 0 {
					p["width"] = n
				}
			}
			if x.Height != nil {
				if n, err := x.Height.Int64(); err == nil && n > 0 {
					p["height"] = n
				}
			}
		}
	} else if trigger != "AssetCreate" {
		d.Drop = "unknown trigger " + trigger
		return d, nil
	}
	d.Event = Event{Name: name, Subject: "asset:" + a.ID, Payload: p}
	return d, nil
}
