package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// boxService is one entry of SERVICES_FILE: a third-party service the Pocket
// Box runs and set up itself (an admin account and key made at first boot).
type boxService struct {
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	PublicURL string            `json:"public_url"`
	Fields    map[string]string `json:"fields"`
	Audience  string            `json:"audience"`
	Connector *struct {
		Enabled           bool `json:"enabled"`
		ProvisionOnCreate bool `json:"provision_on_create"`
	} `json:"connector"`
}

// applyServicesFile creates or updates the services listed in SERVICES_FILE.
//
// The box owns what it wrote: URL, public URL and the listed fields are set
// again at every start (the box may have rotated a key). Name and audience
// are only set when the service is created, and a connector only gets the
// file's switches when Core creates it, so an admin's later choices stand.
// A new connector set to make accounts provisions everyone already in Core.
func (s *Server) applyServicesFile(ctx context.Context) error {
	if s.cfg.ServicesFile == "" {
		return nil
	}
	raw, err := os.ReadFile(s.cfg.ServicesFile)
	if err != nil {
		return fmt.Errorf("SERVICES_FILE: %w", err)
	}
	var list []boxService
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("SERVICES_FILE: %w", err)
	}
	var fresh []int64
	for _, b := range list {
		typ := strings.ToLower(strings.TrimSpace(b.Type))
		if typ == "" {
			return errors.New("SERVICES_FILE: an entry has no type")
		}
		sv, err := s.store.GetServiceByType(ctx, typ)
		created := errors.Is(err, store.ErrNotFound)
		switch {
		case created:
			sv = &store.Service{Type: typ, Enabled: true, Fields: map[string]string{}, Audience: "all"}
			if b.Audience != "" {
				sv.Audience = b.Audience
			}
			sv.Name = strings.TrimSpace(b.Name)
		case err != nil:
			return fmt.Errorf("SERVICES_FILE: %s: %w", typ, err)
		case sv.AppID > 0:
			s.log.Warn("SERVICES_FILE: a registered app owns this type, skipped", "type", typ)
			continue
		}
		e := catalog.Resolve(typ, sv.Name, "", false)
		if sv.Name == "" {
			sv.Name = e.Name
		}
		sv.URL, sv.PublicURL = strings.TrimSpace(b.URL), strings.TrimSpace(b.PublicURL)
		if sv.Fields == nil {
			sv.Fields = map[string]string{}
		}
		for k, v := range b.Fields {
			sv.Fields[k] = v
		}
		if err := s.store.SaveService(ctx, sv, e.IsSecret); err != nil {
			return fmt.Errorf("SERVICES_FILE: %s: %w", typ, err)
		}
		if b.Connector == nil {
			continue
		}
		c, ok := connectors.Get(typ)
		if !ok {
			s.log.Warn("SERVICES_FILE: Core has no connector for this type", "type", typ)
			continue
		}
		row, exists, err := s.connectorRow(ctx, sv.ID)
		if err != nil {
			return fmt.Errorf("SERVICES_FILE: %s connector: %w", typ, err)
		}
		if exists {
			continue
		}
		row.Enabled = b.Connector.Enabled
		row.ProvisionOnCreate = b.Connector.ProvisionOnCreate && c.Capabilities().Provision
		if err := s.store.SaveConnector(ctx, row); err != nil {
			return fmt.Errorf("SERVICES_FILE: %s connector: %w", typ, err)
		}
		if row.Enabled && row.ProvisionOnCreate {
			fresh = append(fresh, sv.ID)
		}
	}
	s.log.Info("services from SERVICES_FILE applied", "count", len(list), "new_connectors", len(fresh))
	if len(fresh) > 0 {
		go s.provisionEveryone(fresh)
	}
	return nil
}

// provisionEveryone makes accounts in the given services for every person
// in their audience who has none yet.
func (s *Server) provisionEveryone(serviceIDs []int64) {
	ctx := s.ctx
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return
	}
	for _, id := range serviceIDs {
		sv, err := s.store.GetService(ctx, id)
		if err != nil {
			continue
		}
		c, ok := connectors.Get(sv.Type)
		if !ok {
			continue
		}
		for i := range users {
			res := s.provisionOne(ctx, sv, c, &users[i])
			s.log.Info("connector provisioned person", "type", c.Type(), "user", users[i].Email, "status", res.Status)
		}
	}
}
