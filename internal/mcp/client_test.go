package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscoverSuccess(t *testing.T) {
	client := testClient(t, "success")
	got, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerInfo.Name != "fake" || len(got.Tools) != 1 || got.Tools[0].Name != "echo" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if string(got.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("schema was not preserved: %s", got.Tools[0].InputSchema)
	}
}

func TestDiscoverRejectsMalformedFraming(t *testing.T) {
	client := testClient(t, "malformed")
	_, err := client.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("expected framing error, got %v", err)
	}
}

func TestDiscoverTimeoutKillsServer(t *testing.T) {
	client := testClient(t, "timeout")
	start := time.Now()
	_, err := client.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout was not bounded: %s", time.Since(start))
	}
}

func TestDiscoverRejectsOversizedResponse(t *testing.T) {
	client := testClient(t, "oversized")
	_, err := client.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestDiscoverDoesNotShellExpandArguments(t *testing.T) {
	client := testClient(t, "arg", "literal&not-expanded")
	got, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerInfo.Name != "literal&not-expanded" {
		t.Fatalf("argument was altered: %q", got.ServerInfo.Name)
	}
}

func testClient(t *testing.T, mode string, extra ...string) *Client {
	t.Helper()
	args := []string{"-test.run=TestFakeStdioServer", "--", mode}
	args = append(args, extra...)
	timeout := 2 * time.Second
	if mode == "timeout" {
		timeout = 150 * time.Millisecond
	}
	client, err := NewClient(Config{Command: append([]string{os.Args[0]}, args...), Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestFakeStdioServer(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "--" {
			marker = i
			break
		}
	}
	if marker < 0 || marker+1 >= len(os.Args) {
		return
	}
	args := os.Args[marker+1:]
	mode := args[0]
	if mode == "arg" {
		if len(args) < 2 {
			os.Exit(2)
		}
	}
	if mode == "timeout" {
		time.Sleep(2 * time.Second)
		return
	}
	if mode == "malformed" {
		fmt.Print("not-a-header\r\n\r\n")
		return
	}
	if mode == "oversized" {
		fmt.Printf("Content-Length: %d\r\n\r\n", maxResponseBytes+1)
		return
	}
	r := bufio.NewReader(os.Stdin)
	for i := 0; i < 4; i++ {
		body, err := readTestFrame(r)
		if err != nil {
			return
		}
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &request) != nil {
			return
		}
		if request.Method == "notifications/initialized" {
			i--
			continue
		}
		var response any
		switch request.Method {
		case "initialize":
			name := "fake"
			if mode == "arg" {
				name = args[1]
			}
			response = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]string{"name": name, "version": "1"}, "capabilities": map[string]any{}}
		case "tools/list":
			response = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "safe", "inputSchema": map[string]string{"type": "object"}}}}
		case "resources/list":
			response = map[string]any{"resources": []any{}}
		case "prompts/list":
			response = map[string]any{"prompts": []any{}}
		}
		writeTestFrame(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": response})
	}
}

func readTestFrame(r *bufio.Reader) ([]byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			_, _ = fmt.Sscanf(line, "Content-Length: %d", &length)
			if length == 0 {
				_, _ = fmt.Sscanf(line, "content-length: %d", &length)
			}
		}
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

func writeTestFrame(value any) {
	body, _ := json.Marshal(value)
	fmt.Printf("Content-Length: %d\r\n\r\n%s", len(body), body)
}
