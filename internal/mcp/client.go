package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 4 << 20
	maxHeaderBytes   = 16 << 10
	maxFieldBytes    = 64 << 10
	maxErrorBytes    = 512
)

// Config controls a bounded stdio MCP discovery.
type Config struct {
	Command []string
	Timeout time.Duration
}

type Client struct {
	config Config
}

type DiscoveryResult struct {
	ServerInfo ServerInfo        `json:"server_info"`
	Tools      []ToolSummary     `json:"tools"`
	Resources  []ResourceSummary `json:"resources"`
	Prompts    []PromptSummary   `json:"prompts"`
}

type ServerInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
}

type ToolSummary struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func (t *ToolSummary) UnmarshalJSON(data []byte) error {
	var wire struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	t.Name, t.Description, t.InputSchema = wire.Name, wire.Description, wire.InputSchema
	return nil
}

type ResourceSummary struct {
	Name        string `json:"name"`
	URI         string `json:"uri,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

func (r *ResourceSummary) UnmarshalJSON(data []byte) error {
	var wire struct {
		Name        string `json:"name"`
		URI         string `json:"uri"`
		Description string `json:"description"`
		MimeType    string `json:"mimeType"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.Name, r.URI, r.Description, r.MimeType = wire.Name, wire.URI, wire.Description, wire.MimeType
	return nil
}

type PromptSummary struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

func NewClient(cfg Config) (*Client, error) {
	if len(cfg.Command) == 0 || strings.TrimSpace(cfg.Command[0]) == "" {
		return nil, errors.New("stdio command is required")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("stdio timeout must be positive")
	}
	return &Client{config: Config{Command: append([]string(nil), cfg.Command...), Timeout: cfg.Timeout}}, nil
}

func (c *Client) Discover(ctx context.Context) (DiscoveryResult, error) {
	var result DiscoveryResult
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	cmd := exec.Command(c.config.Command[0], c.config.Command[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, boundedError("create stdio pipe", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, boundedError("create stdio pipe", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return result, boundedError("start stdio command", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	cleanup := func() {
		close(done)
		_ = stdin.Close()
		_ = stdout.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
	defer cleanup()

	reader := bufio.NewReader(stdout)
	writer := bufio.NewWriter(stdin)
	nextID := 1
	call := func(method string, params any) (json.RawMessage, error) {
		id := nextID
		nextID++
		request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		if err := writeFrame(writer, request); err != nil {
			return nil, err
		}
		for {
			frame, err := readFrame(ctx, reader)
			if err != nil {
				return nil, err
			}
			var response struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Result  json.RawMessage `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(frame, &response); err != nil {
				return nil, boundedError("decode response", err)
			}
			if response.JSONRPC != "2.0" {
				return nil, errors.New("invalid JSON-RPC version")
			}
			if len(response.ID) == 0 {
				continue // notification
			}
			var got int
			if err := json.Unmarshal(response.ID, &got); err != nil || got != id {
				return nil, errors.New("unexpected JSON-RPC response id")
			}
			if len(response.Error) != 0 && string(response.Error) != "null" {
				return nil, errors.New("MCP returned an error")
			}
			return response.Result, nil
		}
	}

	initialize, err := call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "siegeproof", "version": "0.1.0-dev"},
	})
	if err != nil {
		return result, boundedError("initialize", err)
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(initialize, &initResult); err != nil {
		return result, boundedError("decode initialize result", err)
	}
	result.ServerInfo = ServerInfo{Name: capString(initResult.ServerInfo.Name), Version: capString(initResult.ServerInfo.Version), ProtocolVersion: capString(initResult.ProtocolVersion)}
	if err := writeFrame(writer, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}}); err != nil {
		return result, boundedError("send initialized notification", err)
	}

	tools, err := call("tools/list", map[string]any{})
	if err != nil {
		return result, boundedError("list tools", err)
	}
	resources, err := call("resources/list", map[string]any{})
	if err != nil {
		return result, boundedError("list resources", err)
	}
	prompts, err := call("prompts/list", map[string]any{})
	if err != nil {
		return result, boundedError("list prompts", err)
	}
	var toolResult struct {
		Tools []ToolSummary `json:"tools"`
	}
	var resourceResult struct {
		Resources []ResourceSummary `json:"resources"`
	}
	var promptResult struct {
		Prompts []PromptSummary `json:"prompts"`
	}
	if err := json.Unmarshal(tools, &toolResult); err != nil {
		return result, boundedError("decode tools", err)
	}
	if err := json.Unmarshal(resources, &resourceResult); err != nil {
		return result, boundedError("decode resources", err)
	}
	if err := json.Unmarshal(prompts, &promptResult); err != nil {
		return result, boundedError("decode prompts", err)
	}
	result.Tools, result.Resources, result.Prompts = toolResult.Tools, resourceResult.Resources, promptResult.Prompts
	if err := capResult(&result); err != nil {
		return DiscoveryResult{}, err
	}
	return result, nil
}

func writeFrame(w *bufio.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return boundedError("encode request", err)
	}
	if len(body) > maxResponseBytes {
		return errors.New("request exceeds size limit")
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return boundedError("write request", err)
	}
	if _, err := w.Write(body); err != nil {
		return boundedError("write request", err)
	}
	if err := w.Flush(); err != nil {
		return boundedError("flush request", err)
	}
	return nil
}

func readFrame(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	headers := make(map[string]string)
	total := 0
	for {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		line, err := r.ReadString('\n')
		if err != nil {
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, boundedError("read MCP headers", err)
		}
		total += len(line)
		if total > maxHeaderBytes {
			return nil, errors.New("MCP headers exceed size limit")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, errors.New("malformed MCP header")
		}
		headers[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
	}
	length, err := strconv.Atoi(headers["content-length"])
	if err != nil || length < 0 {
		return nil, errors.New("missing or invalid Content-Length")
	}
	if length > maxResponseBytes {
		return nil, errors.New("MCP response exceeds size limit")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, boundedError("read MCP response", err)
	}
	return body, nil
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return errors.New("MCP command timed out")
	default:
		return nil
	}
}

func capResult(result *DiscoveryResult) error {
	for i := range result.Tools {
		result.Tools[i].Name = capString(result.Tools[i].Name)
		result.Tools[i].Description = capString(result.Tools[i].Description)
		result.Tools[i].InputSchema = capJSON(result.Tools[i].InputSchema)
	}
	for i := range result.Resources {
		result.Resources[i].Name = capString(result.Resources[i].Name)
		result.Resources[i].URI = capString(result.Resources[i].URI)
		result.Resources[i].Description = capString(result.Resources[i].Description)
		result.Resources[i].MimeType = capString(result.Resources[i].MimeType)
	}
	for i := range result.Prompts {
		result.Prompts[i].Name = capString(result.Prompts[i].Name)
		result.Prompts[i].Description = capString(result.Prompts[i].Description)
		result.Prompts[i].Arguments = capJSON(result.Prompts[i].Arguments)
	}
	return nil
}

func capString(s string) string {
	if len(s) <= maxFieldBytes {
		return s
	}
	return s[:maxFieldBytes]
}

func capJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) > maxFieldBytes {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func boundedError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	if len(message) > maxErrorBytes {
		message = message[:maxErrorBytes]
	}
	return fmt.Errorf("%s: %s", prefix, message)
}
