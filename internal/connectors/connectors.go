// Package connectors lets Core act on a third-party app it knows about: make
// or find a person's account there and hand back that person's own
// credential, turn on "Sign in with Pocket Core" in the app, and turn what
// happens in the app into events on Core's bus.
//
// A connector only talks HTTP to its app. It never imports the store or the
// server: Core hands it a Target (where the app is, the decrypted service
// fields, an HTTP client with timeouts and a directory for backups) and
// stores whatever comes back. See docs/connectors.md.
package connectors

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Account status values, stored per person in connector_accounts.
const (
	StatusOK     = "ok"     // Core made the account (or found it) and holds the person's credential
	StatusLinked = "linked" // an account with the person's email already existed; no credential minted
	StatusError  = "error"  // the last attempt failed; Error says why
)

// Capabilities says what a connector can do.
type Capabilities struct {
	Provision bool `json:"provision"`
	SSO       bool `json:"sso"`
	Events    bool `json:"events"`
	Link      bool `json:"link"` // an admin can tie a person to an existing account by hand (Linker)
}

// Target is one configured instance of the app.
type Target struct {
	BaseURL   string            // URL Core reaches the app at (the service's internal URL)
	PublicURL string            // URL people's browsers and phones use
	Fields    map[string]string // decrypted service fields (API_KEY…), box-wide admin values
	HTTP      *http.Client      // with timeouts; never nil
	DataDir   string            // where a connector may write backups (DATA_DIR/connectors)
	Now       func() time.Time  // clock, for backup names; nil = time.Now
}

// Clock returns the target's clock.
func (t Target) Clock() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Info is what Test learned about the app.
type Info struct {
	Version     string   `json:"version"`
	Admin       bool     `json:"admin"`                 // the stored credential has admin rights
	Users       int      `json:"users"`                 // accounts the admin read returned
	Permissions []string `json:"permissions,omitempty"` // what the stored key may do, when the app says
	Detail      string   `json:"detail,omitempty"`
}

// Person is the Core user an account is made for.
type Person struct {
	Email string
	Name  string
	// KeepOwnCredential: the person already set their own credential for
	// this service in Core. Make or find the account but mint nothing.
	KeepOwnCredential bool
}

// Account is the result of provisioning one person.
type Account struct {
	RemoteID string            `json:"remote_id"`
	Status   string            `json:"status"`         // StatusOK or StatusLinked
	Fields   map[string]string `json:"-"`              // the person's own credential (catalog field keys); never logged
	Note     string            `json:"note,omitempty"` // human explanation, e.g. why nothing was minted
	Created  bool              `json:"created"`        // the remote account was created just now
}

// SSOConfig is what Core asks the app to use for "Sign in with Pocket Core".
type SSOConfig struct {
	Enabled      bool
	Issuer       string
	ClientID     string
	ClientSecret string
	ButtonText   string
	// Links: for apps that match sign-ins by username (Jellyfin), the
	// username Core sends (preferred_username) → the app account each
	// linked person must land in; "" = Core has no link for that name.
	Links map[string]string
}

// Event is one thing that happened in the app. Core publishes it as
// "<type>.<Name>" with source "connector:<type>".
type Event struct {
	Name      string // e.g. asset_created
	Subject   string // free identifier; Core falls back to UserEmail
	UserEmail string // set when the event belongs to a person; copied into the payload as user_email
	// RemoteUserID: the app account the event belongs to, for apps without
	// emails; Core finds the person through the accounts it linked.
	RemoteUserID string
	Payload      map[string]any // event body
}

// RemoteAccount is an account in the app, offered for linking by hand.
type RemoteAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Admin    bool   `json:"admin"`
	Disabled bool   `json:"disabled"`
}

// Linker is implemented by connectors whose app cannot match people by
// email: an admin picks the account, Core stores it as linked.
type Linker interface {
	RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error)
	// Link checks the account exists and returns what to store for the
	// person (Status linked, non-secret fields like USER_ID).
	Link(ctx context.Context, t Target, remoteID string) (Account, error)
}

// ---- push: the app calls Core for each thing that happens ----

// PushHeader carries a push subscription's secret on every delivery.
const PushHeader = "X-Pocket-Hook"

// PushTrigger is one kind of delivery Core subscribes to.
type PushTrigger struct {
	Trigger string `json:"trigger"` // the app's own name, e.g. AssetCreate
	Name    string `json:"name"`    // what the subscription is called inside the app
}

// Subscription is what Core asks the app to set up: call URL with the
// header for every Trigger.
type Subscription struct {
	Trigger     string
	Name        string
	Description string
	URL         string
	HeaderName  string
	HeaderValue string // the per-subscription secret; never logged
}

// RemoteUser is who a credential acts as inside the app.
type RemoteUser struct {
	ID    string
	Email string
}

// Delivery is one decoded push.
type Delivery struct {
	OwnerID   string // the app account the delivery is about
	DedupeKey string // the same key inside the dedupe window is the same delivery again
	Event     Event  // Name, Subject and Payload; Core adds user_email
	Drop      string // set: do not publish, and why
	// At: when it happened in the app (zero = unknown). Core drops what
	// happened before it connected the person: an account's welcome files
	// are not news.
	At time.Time
}

// Pusher is implemented by connectors whose events arrive by push instead
// of polling. Every call acts with the credential in Target.Fields: the
// person's own key for a per-person subscription, the box-wide one otherwise.
type Pusher interface {
	PushTriggers() []PushTrigger
	// PushPerUser: the app runs a subscription only for its owner's things,
	// so Core keeps one set per person, made with that person's credential.
	PushPerUser() bool
	WhoAmI(ctx context.Context, t Target) (RemoteUser, error)
	// Subscribe creates the subscription and returns the app's id for it.
	Subscribe(ctx context.Context, t Target, s Subscription) (string, error)
	// Subscriptions lists what exists for the credential: app id → name.
	Subscriptions(ctx context.Context, t Target) (map[string]string, error)
	// Unsubscribe deletes one; an id that no longer exists is not an error.
	Unsubscribe(ctx context.Context, t Target, remoteID string) error
	// Decode turns one delivery body into an event (or a reason to drop it).
	Decode(t Target, trigger string, body []byte) (Delivery, error)
}

// Connector is implemented once per app type.
type Connector interface {
	Type() string
	Capabilities() Capabilities
	// Test checks reachability and that the stored credential can do the admin work.
	Test(ctx context.Context, t Target) (Info, error)
	// Provision finds the person's account by email (linking it) or creates it,
	// and returns the person's own credential when it could mint one.
	Provision(ctx context.Context, t Target, p Person) (Account, error)
	// ConfigureSSO turns the app's OpenID Connect login on or off.
	ConfigureSSO(ctx context.Context, t Target, c SSOConfig) error
	// SSORedirects lists the redirect URIs the app will use with Core.
	SSORedirects(publicURL string) []string
	// Poll returns what changed since state. An empty state is the first
	// poll: record a baseline and return no events. A Pusher still polls for
	// what its pushes do not cover (Immich: new accounts).
	Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Connector{}
)

// Register adds a connector; the last registration of a type wins.
func Register(c Connector) {
	mu.Lock()
	registry[strings.ToLower(c.Type())] = c
	mu.Unlock()
}

// Get returns the connector for a service type.
func Get(typ string) (Connector, bool) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := registry[strings.ToLower(strings.TrimSpace(typ))]
	return c, ok
}

// All lists registered connectors by type.
func All() []Connector {
	mu.RLock()
	out := make([]Connector, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// RandomPassword returns a strong throwaway password (letters, digits, symbols).
func RandomPassword(n int) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789-_.!@#%^*+="
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// redact removes every secret from s; error bodies sometimes echo requests.
func redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}
