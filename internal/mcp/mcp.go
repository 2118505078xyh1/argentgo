// Package mcp speaks the Model Context Protocol to external tool servers over
// stdio or HTTP/SSE and exposes their tools through one small Client interface.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// defaultTimeout bounds one request when the context carries no deadline.
	defaultTimeout = 30 * time.Second
	// protocolVersion is the MCP revision this client announces.
	protocolVersion = "2024-11-05"
	// clientName is reported to servers in clientInfo.
	clientName = "argentgo"
	// clientVersion is reported to servers in clientInfo.
	clientVersion = "0.7.0"
)

// ErrUnsupportedTransport reports a transport this package cannot speak.
var ErrUnsupportedTransport = errors.New(`mcp: transport must be "stdio" or "http"`)

// Spec describes how to reach one server.
type Spec struct {
	// Name identifies the server; it namespaces its tools.
	Name string
	// Transport is "stdio" or "http" (an empty value is inferred).
	Transport string
	// Command is the stdio program to spawn.
	Command string
	// Args are the stdio program's arguments.
	Args []string
	// Env entries ("KEY=VALUE") are added to the child's inherited environment.
	Env []string
	// URL is the HTTP endpoint. A legacy SSE endpoint announces where requests
	// are POSTed; a streamable endpoint takes JSON-RPC POSTs directly.
	URL string
	// Headers are extra HTTP headers (ignored for stdio).
	Headers map[string]string
	// Timeout bounds one request; zero means 30s.
	Timeout time.Duration
	// DiscoveryTimeout is an optional per-server discovery timeout override.
	// Zero selects the graded default (30s stdio / 5s remote); a positive value
	// is clamped to [1s, 2m]. See EffectiveDiscoveryTimeout.
	DiscoveryTimeout time.Duration
	// Enabled defaults to true when nil. An explicit false disables the server:
	// Connect refuses it and it is omitted from Specs/Tools views. A pointer is
	// used so every pre-existing Spec literal keeps its zero-value meaning.
	Enabled *bool
	// ReadOnly blocks calls to tools that look mutating before any request is
	// sent. See MarkMutating/IsMutating.
	ReadOnly bool
	// AllowedTools is a tool allowlist; empty means every tool. Entries may be
	// plain ("search") or qualified ("server__search").
	AllowedTools []string
	// DeniedTools removes names from the allowed set; deny wins over allow.
	DeniedTools []string
}

// ServerInfo is the metadata returned by initialize.
type ServerInfo struct {
	Name            string
	Version         string
	ProtocolVersion string
	Instructions    string
	Capabilities    json.RawMessage
}

// Tool is a tool advertised by a server.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Schema is the normalized inputSchema: it always has a "type" and carries
	// no malformed "properties"/"required". It is safe to put straight into a
	// provider tool spec (see internal/app's mcpBinding wiring).
	Schema json.RawMessage `json:"schema,omitempty"`
	Server string          `json:"server,omitempty"`
	// ReadOnly mirrors the MCP annotations.readOnlyHint bit.
	ReadOnly bool `json:"read_only,omitempty"`

	// RawInputSchema is the unmodified inputSchema as advertised by the server.
	// Additive and omitted when empty; callers building provider specs should
	// keep using Schema, which is already cleaned.
	RawInputSchema json.RawMessage `json:"raw_input_schema,omitempty"`
	// OutputSchema is the server's outputSchema member, passed through as-is.
	// Additive and omitted when empty.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// Resource is a resource advertised by a server.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// Client is one connected server. Initialize must succeed before the other
// calls are meaningful; Close is safe to call more than once.
type Client interface {
	Initialize(ctx context.Context) (ServerInfo, error)
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)
	ListResources(ctx context.Context) ([]Resource, error)
	Close() error
}

// transport is the wire behind a Client.
type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	Close() error
}

// Namespaced joins a server and tool name into the name shown to the model.
func Namespaced(server, tool string) string {
	return server + "__" + tool
}

// Connect opens the transport named by spec. The MCP handshake is not part of
// Connect: call Initialize before ListTools or CallTool.
func Connect(ctx context.Context, spec Spec) (Client, error) {
	if !spec.IsEnabled() {
		return nil, fmt.Errorf("%w: %q", ErrDisabled, spec.Name)
	}
	kind := strings.ToLower(strings.TrimSpace(spec.Transport))
	if kind == "" {
		if spec.Command != "" {
			kind = "stdio"
		} else {
			kind = "http"
		}
	}
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = "mcp"
	}

	var tr transport
	switch kind {
	case "stdio":
		if strings.TrimSpace(spec.Command) == "" {
			return nil, fmt.Errorf("mcp %q: stdio transport needs a command", spec.Name)
		}
		stdio, err := newStdioTransport(spec)
		if err != nil {
			return nil, err
		}
		tr = stdio
	case "http", "sse":
		if strings.TrimSpace(spec.URL) == "" {
			return nil, fmt.Errorf("mcp %q: http transport needs a URL", spec.Name)
		}
		httpTransport, err := newHTTPTransport(ctx, spec)
		if err != nil {
			return nil, err
		}
		tr = httpTransport
	default:
		return nil, fmt.Errorf("%w (got %q)", ErrUnsupportedTransport, spec.Transport)
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &client{spec: spec, tr: tr, timeout: timeout}, nil
}

// client implements Client on top of a transport.
type client struct {
	spec    Spec
	tr      transport
	timeout time.Duration

	// mu guards the last successfully listed tool view exposed by Tools.
	mu    sync.RWMutex
	tools []Tool
}

func (c *client) Initialize(ctx context.Context) (ServerInfo, error) {
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	params := map[string]any{
		"protocolVersion": protocolVersion,
		// Advertise only capabilities we actually implement. handleOne drops
		// every server-initiated request (it has no roots/list or
		// sampling/createMessage handler), so claiming roots or sampling here
		// invites a compliant server to send requests we silently ignore and
		// then wait forever for a reply.
		"capabilities": map[string]any{},
		"clientInfo":   map[string]any{"name": clientName, "version": clientVersion},
	}
	raw, err := c.tr.call(ctx, "initialize", params)
	if err != nil {
		return ServerInfo{}, c.wrap(err, "initialize")
	}
	var result struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Instructions    string          `json:"instructions"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ServerInfo{}, fmt.Errorf("mcp %q: decode initialize result: %w", c.spec.Name, err)
	}
	info := ServerInfo{
		Name:            result.ServerInfo.Name,
		Version:         result.ServerInfo.Version,
		ProtocolVersion: result.ProtocolVersion,
		Instructions:    result.Instructions,
		Capabilities:    result.Capabilities,
	}
	if err := c.tr.notify(ctx, "notifications/initialized", nil); err != nil {
		return ServerInfo{}, c.wrap(err, "notifications/initialized")
	}
	return info, nil
}

func (c *client) ListTools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	raw, err := c.tr.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, c.wrap(err, "tools/list")
	}
	// Decode the tool list one entry at a time. A single malformed tool must
	// not sink the whole table: previously a whole-slice Unmarshal failed the
	// entire tools/list call when any entry had a non-object shape.
	var result struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp %q: decode tools/list result: %w", c.spec.Name, err)
	}
	tools := make([]Tool, 0, len(result.Tools))
	for _, itemRaw := range result.Tools {
		var item struct {
			Name         string          `json:"name"`
			Description  string          `json:"description"`
			InputSchema  json.RawMessage `json:"inputSchema"`
			OutputSchema json.RawMessage `json:"outputSchema"`
			Annotations  struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			continue // skip only the bad entry, keep the rest of the table
		}
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		tools = append(tools, Tool{
			Name:           item.Name,
			Description:    item.Description,
			Schema:         normalizeInputSchema(item.InputSchema),
			Server:         c.spec.Name,
			ReadOnly:       item.Annotations.ReadOnlyHint,
			RawInputSchema: append(json.RawMessage(nil), item.InputSchema...),
			OutputSchema:   append(json.RawMessage(nil), item.OutputSchema...),
		})
	}
	// Apply per-server permissions here so callers never see tools that the
	// server config disallows. Keep the advertising order for stable output.
	tools = FilterTools(c.spec, tools)
	c.mu.Lock()
	c.tools = append([]Tool(nil), tools...)
	c.mu.Unlock()
	return tools, nil
}

// CallTool invokes one tool and returns its text content, joined by newlines.
// An isError result is returned as the text plus an error, so a caller that
// wants to show the server's explanation still has it.
func (c *client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	// Fail closed before touching the transport: read-only servers must never
	// send a mutating call, and filtered-out tools must never be invoked.
	if c.spec.ReadOnly && IsMutating(name) {
		return "", fmt.Errorf("%w: tool %q on server %q looks mutating; call blocked before contacting the server, no request was sent (set readOnly=false in the server config if writes are intended)", ErrReadOnly, name, c.spec.Name)
	}
	if !toolPermitted(c.spec, Tool{Name: name, Server: c.spec.Name}) {
		return "", fmt.Errorf("%w: tool %q on server %q is excluded by allowedTools/deniedTools; no request was sent", ErrToolNotAllowed, name, c.spec.Name)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return "", fmt.Errorf("mcp %q: tool %q arguments are not valid JSON", c.spec.Name, name)
	}
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	params := map[string]any{"name": name, "arguments": args}
	raw, err := c.tr.call(ctx, "tools/call", params)
	if err != nil {
		return "", c.wrap(err, "tools/call "+name)
	}
	var result struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MIMEType string `json:"mimeType"`
			Resource struct {
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("mcp %q: decode tools/call result: %w", c.spec.Name, err)
	}

	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "resource":
			if block.Resource.Text != "" {
				parts = append(parts, block.Resource.Text)
			}
		case "image":
			mime := block.MIMEType
			if mime == "" {
				mime = "unknown"
			}
			parts = append(parts, "[image: "+mime+"]")
		}
	}
	out := strings.Join(parts, "\n")
	if out == "" {
		out = string(raw)
	}
	if result.IsError {
		return out, fmt.Errorf("mcp %q: tool %q reported an error: %s", c.spec.Name, name, out)
	}
	return out, nil
}

// ListResources returns the server's resources. A server that does not
// implement resources/list yields an empty list rather than an error.
func (c *client) ListResources(ctx context.Context) ([]Resource, error) {
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	raw, err := c.tr.call(ctx, "resources/list", nil)
	if err != nil {
		if methodNotFound(err) {
			return nil, nil
		}
		return nil, c.wrap(err, "resources/list")
	}
	var result struct {
		Resources []Resource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp %q: decode resources/list result: %w", c.spec.Name, err)
	}
	return result.Resources, nil
}

func (c *client) Close() error {
	return c.tr.Close()
}

// timeoutContext bounds a request without overriding the caller's deadline.
func (c *client) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *client) wrap(err error, method string) error {
	return fmt.Errorf("mcp %q: %s: %w", c.spec.Name, method, err)
}

// Stderr returns the tail of a stdio server's stderr, for diagnostics. It is
// empty for HTTP clients or clients built by other packages.
func Stderr(c Client) string {
	concrete, ok := c.(*client)
	if !ok {
		return ""
	}
	stdio, ok := concrete.tr.(*stdioTransport)
	if !ok {
		return ""
	}
	return stdio.diag.String()
}
