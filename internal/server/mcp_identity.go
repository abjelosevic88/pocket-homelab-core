package server

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Project only the caller's public account fields, never the stored User model.
type mcpIdentityUser struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Role    string `json:"role"`
}

type mcpIdentity struct {
	User                 mcpIdentityUser `json:"user"`
	Scopes               []string        `json:"scopes"`
	AllowedApps          []string        `json:"allowed_apps"`
	CanReadAppContent    bool            `json:"can_read_app_content"`
	CanCreateUpdateItems bool            `json:"can_create_update_items"`
}

func (s *Server) addMCPIdentityTool(ms *mcp.Server, ctx context.Context) {
	// Operator status credentials have no person attached. Never infer one from
	// app visibility, an administrator account or a client-supplied user ID.
	if mcpUser(ctx).UserID == 0 {
		return
	}
	mcp.AddTool(ms, &mcp.Tool{
		Name:        "pocket_whoami",
		Description: "Identify the Pocket Core user signed in to this MCP connection: name, email, account ID, stable subject, Core role, approved scopes and currently permitted apps. Use this for questions about my user or account; do not infer identity from Hub preferences. Application tools use this user's identity and each app enforces its own item permissions. A Core admin role does not grant MCP deletion or service administration. Takes no user ID and cannot look up other people.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpIdentity, error) {
		account := mcpUser(ctx)
		if account.UserID == 0 {
			return nil, mcpIdentity{}, errors.New("this MCP credential is not connected to a Core user")
		}
		user, err := s.store.GetUser(ctx, account.UserID)
		if err != nil || user.Disabled {
			return nil, mcpIdentity{}, errors.New("signed-in Core account unavailable")
		}
		apps, err := s.store.MCPVisibleApps(ctx, user)
		if err != nil {
			return nil, mcpIdentity{}, errors.New("account permissions unavailable")
		}
		read := s.cfg.MCPAppToolsEnabled && hasMCPScope(account.Scope, "apps:read")
		return nil, mcpIdentity{
			User:   mcpIdentityUser{ID: user.ID, Subject: user.Subject, Email: user.Email, Name: user.Name, Role: user.Role},
			Scopes: strings.Fields(account.Scope), AllowedApps: apps,
			CanReadAppContent: read, CanCreateUpdateItems: read && hasMCPScope(account.Scope, "apps:write"),
		}, nil
	})
}
