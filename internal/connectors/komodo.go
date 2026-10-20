package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Komodo (2.x) connector: events, polled, and people linked by hand.
// Komodo is the household's deployment manager: what it runs (deploys,
// builds, procedures) and what it alerts on (servers, stacks, containers)
// matters to everyone, so its events are household events; an operation run
// by a Komodo user linked to a person also names them.
//
// Every request carries the stored API key pair (X-Api-Key, X-Api-Secret).
// Read requests are POST /read with {"type": "<Request>", "params": {…}}
// (Komodo 2.x also takes POST /read/<Request> with the params as the body).
//
//	GET  /user                                    Test, RemoteAccounts, Link (who the key belongs to)  read
//	POST /read GetVersion                         Test                                                  read
//	POST /read ListUsers                          Test, RemoteAccounts, Link (admins only)              read
//	POST /read GetServersSummary, GetStacksSummary, GetDeploymentsSummary   Test                        read
//	POST /read ListUpdates                        Poll (newest first, 100 a page)                       read
//	POST /read GetUpdate                          Poll (duration, commit, failed stage)                 read
//	POST /read ListAlerts                         Poll (newest first, and the open ones)                read
//	POST /read GetAlert                           Poll (an open alert that left the newest pages)       read
//	POST /read ListServers, ListStacks, …         Poll (resource names, once per poll and type)         read
//
// Nothing is written. Komodo has no per-person keys Core could mint (its
// accounts sign in with a password or Komodo's own OpenID login), and its
// OpenID settings are environment variables read at start, so there is no
// provisioning and no sign-in switch. Logs and resource TOML never go on the
// bus: they can hold secrets.
type komodo struct{}

func init() { Register(komodo{}) }

const (
	kmOverlap      = int64(10 * time.Minute / time.Millisecond) // re-read window behind the newest update/alert seen
	kmUpdatePages  = 5                                          // ListUpdates pages a poll may read (100 each)
	kmAlertPages   = 3                                          // ListAlerts pages a poll may read (100 each)
	kmMaxEvents    = 50                                         // update events per poll; the rest wait for the next poll
	kmMaxAlerts    = 50                                         // alert events per poll; more are dropped
	kmMaxLookups   = 20                                         // GetUpdate/GetAlert calls per poll for things that left the newest pages
	kmMaxPending   = 100                                        // running updates remembered
	kmPendingAge   = 24 * time.Hour                             // a running update older than this is forgotten
	kmMaxSeen      = 2000                                       // ids remembered inside the window
	kmMaxOpen      = 500                                        // open alerts remembered
	kmOneShotDelay = int64(5000)                                // an alert stored resolved within 5 s of opening is a one-shot notice
)

func (komodo) Type() string               { return "komodo" }
func (komodo) Capabilities() Capabilities { return Capabilities{Events: true, Link: true} }

// SSORedirects: Komodo sends {KOMODO_HOST}/auth/oidc/callback.
func (komodo) SSORedirects(publicURL string) []string {
	return []string{strings.TrimRight(publicURL, "/") + "/auth/oidc/callback"}
}

func (komodo) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Core does not make Komodo accounts: create the user in Komodo (or let them sign in once with Komodo's OpenID login) and link it here")
}

func (c komodo) ConfigureSSO(_ context.Context, t Target, sc SSOConfig) error {
	if !sc.Enabled {
		return nil
	}
	issuer, client := sc.Issuer, sc.ClientID
	if issuer == "" {
		issuer = "<Core's issuer URL>"
	}
	if client == "" {
		client = "<client id>"
	}
	host := strings.TrimRight(t.PublicURL, "/")
	if host == "" {
		host = "<Komodo's public URL>"
	}
	return fmt.Errorf("Komodo reads OpenID from its environment: on the Komodo Core container set KOMODO_OIDC_ENABLED=true, KOMODO_OIDC_PROVIDER=%s, KOMODO_OIDC_CLIENT_ID=%s, KOMODO_OIDC_CLIENT_SECRET=<the client secret> and KOMODO_HOST=%s (Komodo signs in through %s/auth/oidc/callback), then restart it", issuer, client, host, host)
}

// ---- requests ----

func (komodo) creds(t Target) (string, string, error) {
	k, s := strings.TrimSpace(t.Fields["API_KEY"]), strings.TrimSpace(t.Fields["API_SECRET"])
	if k == "" || s == "" {
		return "", "", errors.New("the Komodo service in Core needs API_KEY and API_SECRET: create a key in Komodo (Profile → API Keys, or Settings → Users → a service user) and save both under Services → Komodo → Edit")
	}
	return k, s, nil
}

func (c komodo) call(ctx context.Context, t Target, method, path string, body, out any) error {
	k, s, err := c.creds(t)
	if err != nil {
		return err
	}
	_, err = doJSON(ctx, t, "Komodo", method, path, map[string]string{"X-Api-Key": k, "X-Api-Secret": s}, body, out, k, s)
	var ae *appError
	if errors.As(err, &ae) {
		msg := ae.Msg
		var kb struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(msg), &kb) == nil && kb.Error != "" {
			msg = kb.Error
		}
		if ae.Status == http.StatusUnauthorized {
			return errors.New("Komodo rejected the stored API key and secret (HTTP 401): the key was deleted or its user is disabled; create a new key in Komodo and save both values again")
		}
		return &appError{App: "Komodo", Status: ae.Status, Msg: redact(msg, k, s)}
	}
	return err
}

// read calls one read request.
func (c komodo) read(ctx context.Context, t Target, typ string, params, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	return c.call(ctx, t, http.MethodPost, "/read", map[string]any{"type": typ, "params": params}, out)
}

// kmGone: the update or alert no longer exists.
func kmGone(err error) bool {
	var ae *appError
	if !errors.As(err, &ae) {
		return false
	}
	m := strings.ToLower(ae.Msg)
	return ae.Status == http.StatusNotFound || strings.Contains(m, "no update exists") || strings.Contains(m, "no alert found")
}

// kmID reads a Mongo id written as "hex" or {"$oid": "hex"}.
type kmID string

func (k *kmID) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*k = kmID(s)
		return nil
	}
	var o struct {
		OID string `json:"$oid"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	*k = kmID(o.OID)
	return nil
}

// kmVersion reads a version written as {"major":1,"minor":2,"patch":3} or "1.2.3".
type kmVersion string

func (v *kmVersion) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*v = kmVersion(s)
		return nil
	}
	var o struct{ Major, Minor, Patch int }
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	if o.Major != 0 || o.Minor != 0 || o.Patch != 0 {
		*v = kmVersion(fmt.Sprintf("%d.%d.%d", o.Major, o.Minor, o.Patch))
	}
	return nil
}

type kmUser struct {
	ID         kmID   `json:"_id"`
	Username   string `json:"username"`
	Enabled    bool   `json:"enabled"`
	Admin      bool   `json:"admin"`
	SuperAdmin bool   `json:"super_admin"`
	Config     struct {
		Type string `json:"type"` // Local, Google, Github, Oidc, Service
	} `json:"config"`
}

func (u kmUser) admin() bool   { return u.Admin || u.SuperAdmin }
func (u kmUser) service() bool { return u.Config.Type == "Service" }

func (c komodo) me(ctx context.Context, t Target) (kmUser, error) {
	var u kmUser
	err := c.call(ctx, t, http.MethodGet, "/user", nil, &u)
	return u, err
}

// users lists Komodo's users; only an admin's key may.
func (c komodo) users(ctx context.Context, t Target) ([]kmUser, error) {
	me, err := c.me(ctx, t)
	if err != nil {
		return nil, err
	}
	if !me.admin() {
		return nil, fmt.Errorf("the Komodo API key belongs to %q, who is not an admin: listing Komodo's users needs an admin's key (or make %q an admin in Settings → Users)", me.Username, me.Username)
	}
	var list []kmUser
	if err := c.read(ctx, t, "ListUsers", map[string]any{"service_users": "Include"}, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ---- Test ----

func (c komodo) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	me, err := c.me(ctx, t)
	if err != nil {
		return info, err
	}
	if !me.Enabled {
		return info, fmt.Errorf("the Komodo API key belongs to %q, a disabled user: enable it in Komodo (Settings → Users)", me.Username)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := c.read(ctx, t, "GetVersion", nil, &v); err != nil {
		return info, err
	}
	info.Version = v.Version
	info.Admin = me.admin()
	kind := "user"
	if me.service() {
		kind = "service user"
	}
	switch {
	case me.SuperAdmin:
		info.Permissions = []string{"super_admin"}
	case me.Admin:
		info.Permissions = []string{"admin"}
	}
	parts := []string{fmt.Sprintf("key of %s %q", kind, me.Username)}
	if info.Admin {
		var list []kmUser
		if err := c.read(ctx, t, "ListUsers", map[string]any{"service_users": "Include"}, &list); err != nil {
			return info, err
		}
		svc := 0
		for _, u := range list {
			if u.service() {
				svc++
			}
		}
		info.Users = len(list) - svc
		parts = append(parts, fmt.Sprintf("%d users and %d service users", info.Users, svc))
	}
	var servers struct{ Total, Healthy int64 }
	var stacks struct{ Total, Running int64 }
	var deps struct{ Total, Running int64 }
	if c.read(ctx, t, "GetServersSummary", nil, &servers) == nil {
		parts = append(parts, fmt.Sprintf("%d servers (%d healthy)", servers.Total, servers.Healthy))
	}
	if c.read(ctx, t, "GetStacksSummary", nil, &stacks) == nil {
		parts = append(parts, fmt.Sprintf("%d stacks (%d running)", stacks.Total, stacks.Running))
	}
	if c.read(ctx, t, "GetDeploymentsSummary", nil, &deps) == nil {
		parts = append(parts, fmt.Sprintf("%d deployments (%d running)", deps.Total, deps.Running))
	}
	if !info.Admin {
		parts = append(parts, "not an admin: linking people needs an admin's key, and events cover only what this user may read")
	}
	info.Detail = strings.Join(parts, "; ")
	return info, nil
}

// ---- linking ----

// RemoteAccounts lists Komodo's people. Service users are left out: they
// are API identities for automation, never a person.
func (c komodo) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		if u.service() || u.ID == "" {
			continue
		}
		out = append(out, RemoteAccount{ID: string(u.ID), Name: u.Username, Admin: u.admin(), Disabled: !u.Enabled})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Link ties a person to a Komodo user chosen by an admin.
func (c komodo) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	for _, u := range list {
		if string(u.ID) != remoteID {
			continue
		}
		if u.service() {
			return Account{}, fmt.Errorf("%q is a Komodo service user (automation), not a person", u.Username)
		}
		return Account{RemoteID: remoteID, Status: StatusLinked, Fields: map[string]string{"USER_ID": remoteID},
			Note: fmt.Sprintf("linked by hand to the Komodo user %q: operations they run carry this person's email", u.Username)}, nil
	}
	return Account{}, fmt.Errorf("Komodo has no user %s", remoteID)
}

// ---- updates ----

type kmTarget struct {
	Type string `json:"type"` // System, Server, Stack, Deployment, Build, Repo, Procedure, Action, ResourceSync, Builder, Alerter, Swarm
	ID   string `json:"id"`
}

type kmLog struct {
	Stage   string `json:"stage"`
	Success bool   `json:"success"`
}

// kmUpdate is a ListUpdates item (id) or a whole Update from GetUpdate (_id).
type kmUpdate struct {
	ID         string    `json:"id"`
	OID        kmID      `json:"_id"`
	Operation  string    `json:"operation"`
	StartTS    int64     `json:"start_ts"`
	EndTS      *int64    `json:"end_ts"`
	Success    bool      `json:"success"`
	Username   string    `json:"username"`
	Operator   string    `json:"operator"`
	Target     kmTarget  `json:"target"`
	Status     string    `json:"status"` // Queued, InProgress, Complete
	Version    kmVersion `json:"version"`
	CommitHash string    `json:"commit_hash"`
	Logs       []kmLog   `json:"logs"`
}

func (u kmUpdate) key() string {
	if u.ID != "" {
		return u.ID
	}
	return string(u.OID)
}

// kmSystemOperators: Komodo's built-in operators (id or name) → name.
// Their operations were not run by a person (schedules, webhooks, procedures).
var kmSystemOperators = map[string]string{
	"000000000000000000000000": "System", "System": "System",
	"000000000000000000000001": "Procedure", "Procedure": "Procedure",
	"000000000000000000000002": "Action", "Action": "Action",
	"000000000000000000000003": "Git Webhook", "Git Webhook": "Git Webhook", "Github": "Git Webhook",
	"000000000000000000000004": "Auto Redeploy", "Auto Redeploy": "Auto Redeploy",
	"000000000000000000000005": "Resource Sync", "Resource Sync": "Resource Sync",
	"000000000000000000000006": "Stack Wizard", "Stack Wizard": "Stack Wizard",
	"000000000000000000000007": "Build Manager", "Build Manager": "Build Manager",
	"000000000000000000000008": "Repo Manager", "Repo Manager": "Repo Manager",
}

// kmOp is how an operation is announced: event name and past-tense verb.
type kmOp struct{ event, verb string }

var kmOps = map[string]kmOp{
	"DeployStack": {"stack_deployed", "deployed"}, "DeployStackService": {"stack_deployed", "deployed"},
	"PullStack": {"stack_pulled", "pulled"}, "PullStackService": {"stack_pulled", "pulled"},
	"StartStack": {"stack_started", "started"}, "StartStackService": {"stack_started", "started"},
	"RestartStack": {"stack_restarted", "restarted"}, "RestartStackService": {"stack_restarted", "restarted"},
	"StopStack": {"stack_stopped", "stopped"}, "StopStackService": {"stack_stopped", "stopped"},
	"PauseStack": {"stack_paused", "paused"}, "PauseStackService": {"stack_paused", "paused"},
	"UnpauseStack": {"stack_unpaused", "unpaused"}, "UnpauseStackService": {"stack_unpaused", "unpaused"},
	"DestroyStack": {"stack_destroyed", "taken down"}, "DestroyStackService": {"stack_destroyed", "taken down"},
	"RunStackService": {"stack_service_run", "ran a one-off service"},
	"Deploy":          {"deployment_deployed", "deployed"},
	"PullDeployment":  {"deployment_pulled", "pulled"}, "StartDeployment": {"deployment_started", "started"},
	"RestartDeployment": {"deployment_restarted", "restarted"}, "StopDeployment": {"deployment_stopped", "stopped"},
	"PauseDeployment": {"deployment_paused", "paused"}, "UnpauseDeployment": {"deployment_unpaused", "unpaused"},
	"DestroyDeployment": {"deployment_destroyed", "destroyed"},
	"RunBuild":          {"build_finished", "built"}, "CancelBuild": {"build_cancelled", "build cancelled"},
	"CloneRepo": {"repo_cloned", "cloned"}, "PullRepo": {"repo_pulled", "pulled"}, "BuildRepo": {"repo_built", "built"},
	"CancelRepoBuild": {"repo_build_cancelled", "build cancelled"},
	"RunProcedure":    {"procedure_run", "ran"}, "CancelProcedure": {"procedure_cancelled", "cancelled"},
	"RunAction": {"action_run", "ran"}, "CancelAction": {"action_cancelled", "cancelled"},
	"RunSync": {"sync_run", "ran"}, "CommitSync": {"sync_committed", "committed"},
	"GlobalAutoUpdate":   {"auto_update_run", "auto-update ran"},
	"BackupCoreDatabase": {"database_backed_up", "database backed up"},
}

// kmResourceKinds: Create/Update/Rename/Delete<kind> are resource changes.
var kmResourceKinds = map[string]bool{
	"Swarm": true, "Server": true, "Stack": true, "Deployment": true, "Build": true, "Repo": true,
	"Procedure": true, "Action": true, "ResourceSync": true, "Builder": true, "Alerter": true,
	"Variable": true, "GitProviderAccount": true, "DockerRegistryAccount": true,
}

// kmClassify returns the event for a successful operation, its verb and,
// for resource changes, the kind changed.
func kmClassify(op string) (event, verb, kind string) {
	if o, ok := kmOps[op]; ok {
		return o.event, o.verb, ""
	}
	for _, p := range []struct{ prefix, event, verb string }{
		{"Create", "resource_created", "created"}, {"Update", "resource_updated", "updated"},
		{"Rename", "resource_renamed", "renamed"}, {"Delete", "resource_deleted", "deleted"},
	} {
		if k := strings.TrimPrefix(op, p.prefix); k != op && kmResourceKinds[k] {
			return p.event, p.verb, k
		}
	}
	switch op {
	case "UpdateVariableValue":
		return "resource_updated", "updated", "Variable"
	case "WriteStackContents", "WriteDockerfile", "WriteSyncContents":
		return "resource_updated", "files updated", ""
	}
	return kmSnake(op), "", ""
}

// kmSnake: DeployStack → deploy_stack.
func kmSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// kmWords: DeployStack → Deploy Stack.
func kmWords(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// kmKindLabel: ResourceSync → Resource sync.
func kmKindLabel(typ string) string {
	w := strings.ToLower(kmWords(typ))
	if w == "" {
		return ""
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// kmPaths: the UI page of each resource type.
var kmPaths = map[string]string{
	"Server": "servers", "Stack": "stacks", "Deployment": "deployments", "Build": "builds", "Repo": "repos",
	"Procedure": "procedures", "Action": "actions", "ResourceSync": "resource-syncs", "Builder": "builders",
	"Alerter": "alerters", "Swarm": "swarms",
}

// kmLists: the list request naming each resource type.
var kmLists = map[string]string{
	"Server": "ListServers", "Stack": "ListStacks", "Deployment": "ListDeployments", "Build": "ListBuilds",
	"Repo": "ListRepos", "Procedure": "ListProcedures", "Action": "ListActions", "ResourceSync": "ListResourceSyncs",
	"Builder": "ListBuilders", "Alerter": "ListAlerters", "Swarm": "ListSwarms",
}

// kmNames resolves resource ids to names, one list call per type and poll.
type kmNames struct {
	c      komodo
	ctx    context.Context
	t      Target
	byType map[string]map[string]string
}

func (n *kmNames) name(typ, id string) string {
	ep := kmLists[typ]
	if ep == "" || id == "" {
		return ""
	}
	m, ok := n.byType[typ]
	if !ok {
		m = map[string]string{}
		var items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := n.c.read(n.ctx, n.t, ep, map[string]any{"limit": 0}, &items); err == nil {
			for _, it := range items {
				m[it.ID] = it.Name
			}
		}
		n.byType[typ] = m
	}
	return m[id]
}

func kmTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

func (komodo) link(t Target, path string) string {
	pub := strings.TrimRight(t.PublicURL, "/")
	if pub == "" {
		return ""
	}
	return pub + path
}

func (c komodo) targetLink(t Target, tg kmTarget) string {
	if p := kmPaths[tg.Type]; p != "" && tg.ID != "" {
		return c.link(t, "/"+p+"/"+tg.ID)
	}
	return ""
}

// updates reads ListUpdates pages, newest first, until one reaches back
// past cutoff (ms).
func (c komodo) updates(ctx context.Context, t Target, cutoff int64, pages int) ([]kmUpdate, error) {
	var all []kmUpdate
	for p := 0; p < pages; p++ {
		var res struct {
			Updates  []kmUpdate `json:"updates"`
			NextPage *int64     `json:"next_page"`
		}
		if err := c.read(ctx, t, "ListUpdates", map[string]any{"page": p}, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Updates...)
		if len(res.Updates) == 0 || res.NextPage == nil || res.Updates[len(res.Updates)-1].StartTS < cutoff {
			break
		}
	}
	return all, nil
}

func (c komodo) getUpdate(ctx context.Context, t Target, id string) (kmUpdate, error) {
	var u kmUpdate
	err := c.read(ctx, t, "GetUpdate", map[string]any{"id": id}, &u)
	return u, err
}

// kmPending is an update Core saw running.
type kmPending struct {
	TS int64  `json:"ts"`
	By string `json:"by,omitempty"` // username from the list (GetUpdate has none)
}

func (c komodo) updateEvent(t Target, u kmUpdate, full *kmUpdate, n *kmNames) Event {
	id := u.key()
	event, verb, kind := kmClassify(u.Operation)
	name := n.name(u.Target.Type, u.Target.ID)
	p := map[string]any{
		"update_id":   id,
		"operation":   u.Operation,
		"target_type": u.Target.Type,
		"success":     u.Success,
		"started_at":  kmTime(u.StartTS),
	}
	if u.Target.Type != "System" && u.Target.ID != "" {
		p["target_id"] = u.Target.ID
	}
	if name != "" {
		p["target"] = name
	}
	if kind != "" {
		p["resource_type"] = kind
	}
	if v := u.Version; v != "" {
		p["version"] = string(v)
	}
	if full != nil {
		if full.EndTS != nil && *full.EndTS >= u.StartTS {
			p["finished_at"] = kmTime(*full.EndTS)
			p["duration_seconds"] = (*full.EndTS - u.StartTS) / 1000
		}
		if full.CommitHash != "" {
			p["commit"] = full.CommitHash
		}
		if full.Version != "" {
			p["version"] = string(full.Version)
		}
		if !u.Success {
			for _, l := range full.Logs {
				if !l.Success && l.Stage != "" {
					p["failed_stage"] = l.Stage
					break
				}
			}
		}
	}
	ev := Event{Name: event, Subject: "update:" + id, Payload: p}
	sys, isSys := kmSystemOperators[u.Operator]
	switch {
	case isSys || u.Operator == "":
		if sys == "" {
			sys = "System"
		}
		p["operator"] = sys
		p["operator_kind"] = "system"
	default:
		p["operator_id"] = u.Operator
		p["operator_kind"] = "user"
		if u.Username != "" && u.Username != "unknown" {
			p["operator"] = u.Username
		}
		ev.RemoteUserID = u.Operator
	}
	// title: "Stack immich deployed by alice", "Deploy Stack failed: immich"
	label := kmKindLabel(u.Target.Type)
	if u.Target.Type == "System" {
		label = kmKindLabel(kind) // variables and accounts live on System
	}
	subject := strings.TrimSpace(label + " " + name)
	if subject == "" {
		subject = "Komodo"
	}
	what := name
	if what == "" {
		what = subject
	}
	var title string
	switch {
	case !u.Success:
		ev.Name = "operation_failed"
		p["event"] = event
		title = fmt.Sprintf("%s failed: %s", kmWords(u.Operation), what)
	case verb != "":
		title = subject + " " + verb
		if v, ok := p["version"].(string); ok && strings.HasPrefix(event, "build_") {
			title += " (" + v + ")"
		}
	default:
		title = fmt.Sprintf("%s: %s", kmWords(u.Operation), what)
	}
	if op, ok := p["operator"].(string); ok && p["operator_kind"] == "user" {
		title += " by " + op
	} else if ok && sys != "System" {
		title += " (" + op + ")"
	}
	p["title"] = title
	if l := c.link(t, "/updates/"+id); l != "" {
		p["url"] = l
	}
	if l := c.targetLink(t, u.Target); l != "" {
		p["target_url"] = l
	}
	return ev
}

func (c komodo) pollUpdates(ctx context.Context, t Target, st *komodoState, n *kmNames, now time.Time) ([]Event, error) {
	cutoff := st.UpdTS - kmOverlap
	items, err := c.updates(ctx, t, cutoff, kmUpdatePages)
	if err != nil {
		return nil, err
	}
	byID := map[string]kmUpdate{}
	for _, u := range items {
		byID[u.key()] = u
	}
	type cand struct {
		u       kmUpdate
		full    *kmUpdate
		pending bool
	}
	var cands []cand
	newest := st.UpdTS
	for _, u := range items {
		id := u.key()
		if u.StartTS < cutoff || id == "" {
			continue
		}
		if _, ok := st.UpdSeen[id]; ok {
			continue
		}
		if _, ok := st.Pending[id]; ok {
			continue
		}
		switch {
		case u.Operation == "None" || u.Operation == "":
			st.UpdSeen[id] = u.StartTS
		case u.Status != "Complete":
			st.Pending[id] = kmPending{TS: u.StartTS, By: u.Username}
		default:
			cands = append(cands, cand{u: u})
			continue
		}
		newest = max(newest, u.StartTS)
	}
	// running before: done now?
	pend := make([]string, 0, len(st.Pending))
	for id := range st.Pending {
		pend = append(pend, id)
	}
	sort.Slice(pend, func(i, j int) bool { return st.Pending[pend[i]].TS < st.Pending[pend[j]].TS })
	lookups := 0
	for _, id := range pend {
		pd := st.Pending[id]
		u, ok := byID[id]
		var full *kmUpdate
		if !ok || u.Status == "Complete" {
			if lookups >= kmMaxLookups {
				continue
			}
			lookups++
			f, err := c.getUpdate(ctx, t, id)
			switch {
			case err == nil:
				full = &f
				if !ok {
					u = f
					u.Username = pd.By // GetUpdate has no username
				}
			case kmGone(err):
				delete(st.Pending, id)
				continue
			case !ok:
				continue
			}
		}
		if u.Status == "Complete" {
			cands = append(cands, cand{u: u, full: full, pending: true})
		} else if now.Sub(time.UnixMilli(pd.TS)) > kmPendingAge {
			delete(st.Pending, id)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].u.StartTS != cands[j].u.StartTS {
			return cands[i].u.StartTS < cands[j].u.StartTS
		}
		return cands[i].u.key() < cands[j].u.key()
	})
	var out []Event
	deferredFrom := int64(-1)
	for _, cd := range cands {
		id := cd.u.key()
		if len(out) >= kmMaxEvents {
			if !cd.pending && deferredFrom < 0 {
				deferredFrom = cd.u.StartTS // stays inside the next poll's window
			}
			continue
		}
		full := cd.full
		if full == nil && lookups < kmMaxLookups+kmMaxEvents {
			lookups++
			if f, err := c.getUpdate(ctx, t, id); err == nil {
				full = &f
			}
		}
		out = append(out, c.updateEvent(t, cd.u, full, n))
		delete(st.Pending, id)
		st.UpdSeen[id] = cd.u.StartTS
		if !cd.pending {
			newest = max(newest, cd.u.StartTS)
		}
	}
	if deferredFrom >= 0 && deferredFrom < newest {
		newest = deferredFrom
	}
	st.UpdTS = max(newest, st.UpdTS-kmOverlap)
	kmPrune(st.UpdSeen, st.UpdTS-kmOverlap, kmMaxSeen)
	if len(st.Pending) > kmMaxPending {
		ids := make([]string, 0, len(st.Pending))
		for id := range st.Pending {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return st.Pending[ids[i]].TS > st.Pending[ids[j]].TS })
		for _, id := range ids[kmMaxPending:] {
			delete(st.Pending, id)
		}
	}
	return out, nil
}

// kmPrune drops ids older than floor, and the oldest beyond limit.
func kmPrune(m map[string]int64, floor int64, limit int) {
	for id, ts := range m {
		if ts < floor {
			delete(m, id)
		}
	}
	if len(m) <= limit {
		return
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return m[ids[i]] > m[ids[j]] })
	for _, id := range ids[limit:] {
		delete(m, id)
	}
}

// ---- alerts ----

type kmAlert struct {
	ID         kmID     `json:"_id"`
	TS         int64    `json:"ts"`
	Resolved   bool     `json:"resolved"`
	Level      string   `json:"level"` // OK, WARNING, CRITICAL
	Target     kmTarget `json:"target"`
	ResolvedTS *int64   `json:"resolved_ts"`
	Data       struct {
		Type string      `json:"type"`
		Data kmAlertData `json:"data"`
	} `json:"data"`
}

type kmAlertData struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Region        *string                 `json:"region"`
	Err           *struct{ Error string } `json:"err"`
	Percentage    float64                 `json:"percentage"`
	UsedGB        float64                 `json:"used_gb"`
	TotalGB       float64                 `json:"total_gb"`
	Path          string                  `json:"path"`
	ServerVersion string                  `json:"server_version"`
	CoreVersion   string                  `json:"core_version"`
	ServerName    *string                 `json:"server_name"`
	SwarmName     *string                 `json:"swarm_name"`
	From          string                  `json:"from"`
	To            string                  `json:"to"`
	Image         string                  `json:"image"`
	Images        []string                `json:"images"`
	Service       string                  `json:"service"`
	InstanceID    string                  `json:"instance_id"`
	Message       string                  `json:"message"`
	Details       string                  `json:"details"`
	ResourceType  string                  `json:"resource_type"`
	Version       kmVersion               `json:"version"`
}

// kmOneShot: alerts Komodo stores already resolved, a notice rather than a state.
var kmOneShot = map[string]bool{
	"ContainerStateChange": true, "StackStateChange": true, "DeploymentImageUpdateAvailable": true,
	"StackImageUpdateAvailable": true, "DeploymentAutoUpdated": true, "StackAutoUpdated": true,
	"BuildFailed": true, "RepoBuildFailed": true, "ProcedureFailed": true, "ActionFailed": true,
	"ScheduleRun": true, "Custom": true, "Test": true, "AwsBuilderTerminationFailed": true,
}

func (a kmAlert) oneShot() bool {
	if !a.Resolved {
		return false
	}
	if kmOneShot[a.Data.Type] {
		return true
	}
	return a.ResolvedTS != nil && *a.ResolvedTS-a.TS <= kmOneShotDelay
}

func kmPct(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(int(1000*used/total+0.5)) / 10
}

// alertText: title for the alert (opened or resolved) and a one-line message.
func kmAlertText(a kmAlert, name string, resolved bool) (string, string) {
	d := a.Data.Data
	if name == "" {
		name = d.Name
	}
	if name == "" {
		name = kmKindLabel(a.Target.Type)
	}
	where := ""
	if d.SwarmName != nil && *d.SwarmName != "" {
		where = " on swarm " + *d.SwarmName
	} else if d.ServerName != nil && *d.ServerName != "" {
		where = " on " + *d.ServerName
	}
	errMsg := ""
	if d.Err != nil {
		errMsg = d.Err.Error
	}
	switch a.Data.Type {
	case "ServerUnreachable":
		if resolved {
			return name + " is reachable again", ""
		}
		return name + " is unreachable", errMsg
	case "SwarmUnhealthy":
		if resolved {
			return "Swarm " + name + " is healthy again", ""
		}
		return "Swarm " + name + " is unhealthy", errMsg
	case "ServerCpu":
		if resolved {
			return name + ": CPU usage back to normal", ""
		}
		return fmt.Sprintf("%s: CPU at %.1f%%", name, d.Percentage), ""
	case "ServerMem":
		if resolved {
			return name + ": memory usage back to normal", ""
		}
		return fmt.Sprintf("%s: memory at %.1f%%", name, kmPct(d.UsedGB, d.TotalGB)), fmt.Sprintf("using %.1f of %.1f GiB", d.UsedGB, d.TotalGB)
	case "ServerDisk":
		if resolved {
			return fmt.Sprintf("%s: disk %s back to normal", name, d.Path), ""
		}
		return fmt.Sprintf("%s: disk %s at %.1f%%", name, d.Path, kmPct(d.UsedGB, d.TotalGB)), fmt.Sprintf("using %.1f of %.1f GiB", d.UsedGB, d.TotalGB)
	case "ServerVersionMismatch":
		if resolved {
			return name + ": Periphery version matches Core again", ""
		}
		return fmt.Sprintf("%s: Periphery %s does not match Core %s", name, d.ServerVersion, d.CoreVersion), ""
	case "ContainerStateChange":
		return fmt.Sprintf("Deployment %s is now %s%s", name, strings.ReplaceAll(d.To, "_", " "), where), "was " + strings.ReplaceAll(d.From, "_", " ")
	case "StackStateChange":
		return fmt.Sprintf("Stack %s is now %s%s", name, strings.ReplaceAll(d.To, "_", " "), where), "was " + strings.ReplaceAll(d.From, "_", " ")
	case "DeploymentImageUpdateAvailable":
		return fmt.Sprintf("Deployment %s: update available", name), d.Image
	case "StackImageUpdateAvailable":
		return fmt.Sprintf("Stack %s: update available for %s", name, d.Service), d.Image
	case "DeploymentAutoUpdated":
		return fmt.Sprintf("Deployment %s was auto-updated", name), d.Image
	case "StackAutoUpdated":
		return fmt.Sprintf("Stack %s was auto-updated", name), strings.Join(d.Images, ", ")
	case "AwsBuilderTerminationFailed":
		return "AWS builder " + d.InstanceID + " failed to terminate", d.Message
	case "ResourceSyncPendingUpdates":
		return "Resource sync " + name + " has pending changes", ""
	case "BuildFailed":
		return "Build " + name + " failed", string(d.Version)
	case "RepoBuildFailed":
		return "Repo " + name + " build failed", ""
	case "ProcedureFailed":
		return "Procedure " + name + " failed", ""
	case "ActionFailed":
		return "Action " + name + " failed", ""
	case "ScheduleRun":
		return fmt.Sprintf("Scheduled %s %s ran", strings.ToLower(kmWords(d.ResourceType)), name), ""
	case "Custom":
		return d.Message, d.Details
	case "Test":
		return "Alerter " + name + " test", ""
	}
	t := fmt.Sprintf("%s alert: %s", kmWords(a.Data.Type), name)
	if resolved {
		t += " resolved"
	}
	return t, errMsg
}

func (c komodo) alertEvent(t Target, a kmAlert, n *kmNames, resolved bool) Event {
	d := a.Data.Data
	name := n.name(a.Target.Type, a.Target.ID)
	if name == "" {
		name = d.Name
	}
	title, msg := kmAlertText(a, name, resolved && !a.oneShot())
	id := string(a.ID)
	p := map[string]any{
		"alert_id":    id,
		"alert_type":  a.Data.Type,
		"level":       strings.ToLower(a.Level),
		"target_type": a.Target.Type,
		"opened_at":   kmTime(a.TS),
		"resolved":    a.Resolved,
		"title":       title,
	}
	if a.Target.Type != "System" && a.Target.ID != "" {
		p["target_id"] = a.Target.ID
	}
	if name != "" {
		p["target"] = name
	}
	if msg != "" {
		p["message"] = msg
	}
	if a.oneShot() {
		p["one_shot"] = true
	}
	if a.Resolved && a.ResolvedTS != nil {
		p["resolved_at"] = kmTime(*a.ResolvedTS)
		if resolved && !a.oneShot() {
			p["open_seconds"] = (*a.ResolvedTS - a.TS) / 1000
		}
	}
	if d.ServerName != nil && *d.ServerName != "" {
		p["server"] = *d.ServerName
	}
	if d.SwarmName != nil && *d.SwarmName != "" {
		p["swarm"] = *d.SwarmName
	}
	if d.Region != nil && *d.Region != "" {
		p["region"] = *d.Region
	}
	switch a.Data.Type {
	case "ServerCpu":
		p["percentage"] = d.Percentage
	case "ServerMem", "ServerDisk":
		p["percentage"] = kmPct(d.UsedGB, d.TotalGB)
		p["used_gb"], p["total_gb"] = d.UsedGB, d.TotalGB
		if d.Path != "" {
			p["path"] = d.Path
		}
	case "ContainerStateChange", "StackStateChange":
		p["from"], p["to"] = d.From, d.To
	case "DeploymentImageUpdateAvailable", "DeploymentAutoUpdated":
		p["image"] = d.Image
	case "StackImageUpdateAvailable":
		p["image"], p["service"] = d.Image, d.Service
	case "StackAutoUpdated":
		p["images"] = d.Images
	case "ServerVersionMismatch":
		p["server_version"], p["core_version"] = d.ServerVersion, d.CoreVersion
	}
	if l := c.link(t, "/alerts/"+id); l != "" {
		p["url"] = l
	}
	if l := c.targetLink(t, a.Target); l != "" {
		p["target_url"] = l
	}
	ev := Event{Name: "alert_opened", Subject: "alert:" + id, Payload: p}
	if resolved {
		ev.Name = "alert_resolved"
	}
	return ev
}

// alerts reads ListAlerts pages (newest first) until one reaches back past cutoff.
func (c komodo) alerts(ctx context.Context, t Target, query map[string]any, cutoff int64, pages int) ([]kmAlert, error) {
	var all []kmAlert
	for p := 0; p < pages; p++ {
		params := map[string]any{"page": p}
		if query != nil {
			params["query"] = query
		}
		var res struct {
			Alerts   []kmAlert `json:"alerts"`
			NextPage *int64    `json:"next_page"`
		}
		if err := c.read(ctx, t, "ListAlerts", params, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Alerts...)
		if len(res.Alerts) == 0 || res.NextPage == nil || res.Alerts[len(res.Alerts)-1].TS < cutoff {
			break
		}
	}
	return all, nil
}

func (c komodo) pollAlerts(ctx context.Context, t Target, st *komodoState, n *kmNames) ([]Event, error) {
	cutoff := st.AlertTS - kmOverlap
	newest, err := c.alerts(ctx, t, nil, cutoff, kmAlertPages)
	if err != nil {
		return nil, err
	}
	open, err := c.alerts(ctx, t, map[string]any{"resolved": false}, 0, kmAlertPages)
	if err != nil {
		return nil, err
	}
	openNow := map[string]bool{}
	for _, a := range open {
		openNow[string(a.ID)] = true
	}
	byID := map[string]kmAlert{}
	for _, a := range newest {
		byID[string(a.ID)] = a
	}
	sort.SliceStable(newest, func(i, j int) bool { return newest[i].TS < newest[j].TS })
	var out []Event
	handled := map[string]bool{}
	top := st.AlertTS
	for _, a := range newest {
		id := string(a.ID)
		top = max(top, a.TS)
		if a.TS < cutoff || id == "" {
			continue
		}
		if _, ok := st.AlertSeen[id]; ok {
			continue
		}
		st.AlertSeen[id] = a.TS
		handled[id] = true
		if _, known := st.Open[id]; known {
			continue // tracked since the baseline; its resolution is handled below
		}
		out = append(out, c.alertEvent(t, a, n, false))
		switch {
		case !a.Resolved:
			st.Open[id] = a.TS
		case !a.oneShot():
			out = append(out, c.alertEvent(t, a, n, true)) // opened and resolved between two polls
		}
	}
	ids := make([]string, 0, len(st.Open))
	for id := range st.Open {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return st.Open[ids[i]] < st.Open[ids[j]] })
	lookups := 0
	for _, id := range ids {
		if openNow[id] || (handled[id] && !byID[id].Resolved) {
			continue
		}
		a, ok := byID[id]
		if !ok {
			if lookups >= kmMaxLookups {
				continue
			}
			lookups++
			if err := c.read(ctx, t, "GetAlert", map[string]any{"id": id}, &a); err != nil {
				if kmGone(err) {
					delete(st.Open, id)
				}
				continue
			}
		}
		if !a.Resolved {
			continue // open, but past the open list's pages
		}
		out = append(out, c.alertEvent(t, a, n, true))
		delete(st.Open, id)
	}
	// open alerts Core never saw open (past the newest pages): their resolution is news
	for _, a := range open {
		if _, ok := st.Open[string(a.ID)]; !ok && string(a.ID) != "" && !handled[string(a.ID)] {
			st.Open[string(a.ID)] = a.TS
		}
	}
	if len(out) > kmMaxAlerts {
		out = out[:kmMaxAlerts]
	}
	st.AlertTS = top
	kmPrune(st.AlertSeen, st.AlertTS-kmOverlap, kmMaxSeen)
	kmPrune(st.Open, 0, kmMaxOpen)
	return out, nil
}

// ---- Poll ----

type komodoState struct {
	Init      bool                 `json:"init"`
	UpdTS     int64                `json:"upd_ts,omitempty"`     // newest update start (ms) handled
	UpdSeen   map[string]int64     `json:"upd_seen,omitempty"`   // updates handled inside the window → start
	Pending   map[string]kmPending `json:"pending,omitempty"`    // updates seen running
	AlertTS   int64                `json:"alert_ts,omitempty"`   // newest alert (ms) seen
	AlertSeen map[string]int64     `json:"alert_seen,omitempty"` // alerts handled inside the window → opened
	Open      map[string]int64     `json:"open,omitempty"`       // alerts known open → opened
}

func (s *komodoState) fill() {
	if s.UpdSeen == nil {
		s.UpdSeen = map[string]int64{}
	}
	if s.Pending == nil {
		s.Pending = map[string]kmPending{}
	}
	if s.AlertSeen == nil {
		s.AlertSeen = map[string]int64{}
	}
	if s.Open == nil {
		s.Open = map[string]int64{}
	}
}

// baseline records what exists now. Running updates are remembered: their
// end is news. Open alerts too: their resolution is news.
func (c komodo) baseline(ctx context.Context, t Target) (json.RawMessage, error) {
	st := komodoState{Init: true}
	st.fill()
	items, err := c.updates(ctx, t, 0, 1)
	if err != nil {
		return nil, err
	}
	for _, u := range items {
		st.UpdTS = max(st.UpdTS, u.StartTS)
	}
	for _, u := range items {
		id := u.key()
		switch {
		case id == "":
		case u.Status != "Complete" && u.Operation != "None":
			st.Pending[id] = kmPending{TS: u.StartTS, By: u.Username}
		case u.StartTS >= st.UpdTS-kmOverlap:
			st.UpdSeen[id] = u.StartTS
		}
	}
	newest, err := c.alerts(ctx, t, nil, 0, 1)
	if err != nil {
		return nil, err
	}
	open, err := c.alerts(ctx, t, map[string]any{"resolved": false}, 0, kmAlertPages)
	if err != nil {
		return nil, err
	}
	for _, a := range newest {
		st.AlertTS = max(st.AlertTS, a.TS)
	}
	for _, a := range newest {
		if a.TS >= st.AlertTS-kmOverlap && a.ID != "" {
			st.AlertSeen[string(a.ID)] = a.TS
		}
	}
	for _, a := range open {
		if a.ID != "" {
			st.Open[string(a.ID)] = a.TS
		}
	}
	kmPrune(st.Open, 0, kmMaxOpen)
	b, _ := json.Marshal(st)
	return b, nil
}

func (c komodo) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	if _, _, err := c.creds(t); err != nil {
		return nil, state, err
	}
	var st komodoState
	if len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &st) != nil || !st.Init {
		next, err := c.baseline(ctx, t)
		if err != nil {
			return nil, state, err
		}
		return nil, next, nil // first poll: baseline only
	}
	st.fill()
	n := &kmNames{c: c, ctx: ctx, t: t, byType: map[string]map[string]string{}}
	upd, err := c.pollUpdates(ctx, t, &st, n, t.Clock())
	if err != nil {
		return nil, state, err
	}
	al, err := c.pollAlerts(ctx, t, &st, n)
	if err != nil {
		return nil, state, err
	}
	next, _ := json.Marshal(st)
	return append(upd, al...), next, nil
}
