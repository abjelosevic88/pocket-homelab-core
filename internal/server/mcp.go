package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// These projections deliberately exclude URLs, manifests, error strings, and credentials.
type mcpAppStatus struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Status     string `json:"status"`
	CheckedAt  string `json:"checked_at,omitempty"`
	ObservedAt string `json:"observed_at"`
	Source     string `json:"source"`
	Stale      bool   `json:"stale"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

type mcpStatusInput struct {
	Slug  string `json:"slug" jsonschema:"Registered Pocket app slug, e.g. notes. Use pocket_list_apps to discover allowed slugs."`
	Fresh bool   `json:"fresh,omitempty" jsonschema:"Probe the registered health endpoint now instead of reading the cached health result."`
}

func (s *Server) mcpAppAllowed(ctx context.Context, slug string) bool {
	for _, allowed := range mcpClient(ctx).Apps {
		if allowed == "*" || allowed == slug {
			return true
		}
	}
	return false
}

func (s *Server) mcpCachedStatus(a store.App) mcpAppStatus {
	now := time.Now().UTC()
	checked, err := time.Parse(time.RFC3339, a.LastCheckAt)
	stale := err != nil || now.Sub(checked) > 2*s.cfg.HealthInterval || checked.After(now)
	status := a.Status
	if stale {
		status = "unknown"
	}
	return mcpAppStatus{Slug: a.Slug, Name: a.Name, Version: a.Version, Status: status,
		CheckedAt: a.LastCheckAt, ObservedAt: now.Format(time.RFC3339), Source: "core_health_cache", Stale: stale}
}

func (s *Server) mcpHandler() http.Handler {
	if s.cfg.MCPTokenHash == "" && s.cfg.MCPClientsFile == "" && !s.cfg.MCPOAuthEnabled {
		return http.NotFoundHandler()
	}
	transport := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		ms := s.mcpStatusServer()
		s.addMCPIdentityTool(ms, r.Context())
		s.addMCPAppTools(ms, r.Context())
		return ms
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 64 << 10, PropagateRequestCancellation: true})
	return s.mcpAccess(transport)
}

func (s *Server) mcpStatusServer() *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{Name: "pocket-core", Version: Version}, nil)
	readOnly := func(name, description string) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	}
	mcp.AddTool(ms, readOnly("pocket_core_status", "Check Core process and database readiness. Does not establish the health of other services."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			status := "ok"
			if err := s.store.DB.PingContext(ctx); err != nil {
				status = "degraded"
			}
			s.log.Info("mcp tool", "client", mcpClient(ctx).ID, "tool", "pocket_core_status", "status", status)
			return nil, map[string]any{"status": status, "version": Version, "uptime_s": int(time.Since(s.started).Seconds()), "observed_at": time.Now().UTC().Format(time.RFC3339)}, nil
		})
	mcp.AddTool(ms, readOnly("pocket_list_apps", "List allowed registered Pocket apps with cached health and freshness. Does not include third-party connectors or personal content."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			apps, err := s.store.ListApps(ctx)
			if err != nil {
				return nil, nil, errors.New("app registry unavailable")
			}
			out := []mcpAppStatus{}
			for _, app := range apps {
				if s.mcpAppAllowed(ctx, app.Slug) {
					out = append(out, s.mcpCachedStatus(app))
				}
			}
			s.log.Info("mcp tool", "client", mcpClient(ctx).ID, "tool", "pocket_list_apps", "count", len(out))
			return nil, map[string]any{"apps": out}, nil
		})
	mcp.AddTool(ms, readOnly("pocket_app_status", "Check one allowed Pocket app. Fresh checks use its registered health endpoint with a six-second deadline; they do not read content or prove that every feature works."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpStatusInput) (*mcp.CallToolResult, mcpAppStatus, error) {
			if !slugRe.MatchString(in.Slug) || !s.mcpAppAllowed(ctx, in.Slug) {
				return nil, mcpAppStatus{}, errors.New("app is not available to this MCP credential")
			}
			app, err := s.store.GetAppBySlug(ctx, in.Slug)
			if err != nil {
				return nil, mcpAppStatus{}, errors.New("app is not available to this MCP credential")
			}
			out := s.mcpCachedStatus(*app)
			if in.Fresh {
				out = s.mcpProbe(ctx, *app)
			}
			s.log.Info("mcp tool", "client", mcpClient(ctx).ID, "tool", "pocket_app_status", "app", in.Slug, "fresh", in.Fresh, "status", out.Status)
			return nil, out, nil
		})
	return ms
}

// Only the server-owned registry selects a target. Never follow redirects, send
// credentials, expose raw upstream errors/bodies, or write results back to the registry.
func (s *Server) mcpProbe(ctx context.Context, app store.App) (out mcpAppStatus) {
	out = s.mcpCachedStatus(app)
	out.Source, out.Stale, out.Status = "live_health_probe", false, "unknown"
	defer func() {
		out.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		out.ObservedAt = out.CheckedAt
	}()
	u, err := url.Parse(app.URL)
	p, pathErr := url.Parse(app.HealthPath)
	if err != nil || pathErr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(app.HealthPath, "/") || strings.HasPrefix(app.HealthPath, "//") || p.IsAbs() || p.Host != "" || p.RawQuery != "" || p.Fragment != "" {
		out.Diagnostic = "invalid_registered_health_endpoint"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(app.URL, "/")+app.HealthPath, nil)
	if err != nil {
		out.Diagnostic = "invalid_registered_health_endpoint"
		return out
	}
	client := *s.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		out.Status, out.Diagnostic = "unreachable", "connection_failed_or_timed_out"
		return out
	}
	defer res.Body.Close()
	out.HTTPStatus = res.StatusCode
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		out.Status, out.Diagnostic = "unhealthy", "health_http_error"
		return out
	}
	var body struct {
		Status string `json:"status"`
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, (16<<10)+1))
	if err != nil || len(payload) > 16<<10 || json.Unmarshal(payload, &body) != nil {
		out.Diagnostic = "unrecognized_health_response"
		return out
	}
	switch strings.ToLower(body.Status) {
	case "ok", "healthy", "up", "ready":
		out.Status = "ok"
	case "error", "down", "unhealthy", "degraded":
		out.Status, out.Diagnostic = "unhealthy", "service_reports_unhealthy"
	default:
		out.Diagnostic = "unrecognized_health_status"
	}
	return out
}
