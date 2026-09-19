package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/withbrian-technologies/siegeproof/internal/mcp"
)

const (
	Schema       = "siegeproof.discovery/v1"
	ToolName     = "siegeproof"
	MaxPathBytes = 4096
)

// Report is the versioned, complete output of a discovery run.
type Report struct {
	Schema          string       `json:"schema"`
	Tool            ToolMetadata `json:"tool"`
	RunTimestamp    string       `json:"run_timestamp"`
	TargetTransport string       `json:"target_transport"`
	Server          ServerInfo   `json:"server"`
	Tools           []Tool       `json:"tools"`
	Resources       []Resource   `json:"resources"`
	Prompts         []Prompt     `json:"prompts"`
	Counts          Counts       `json:"counts"`
	Complete        bool         `json:"complete"`
}

type ToolMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ServerInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type Resource struct {
	Name        string `json:"name"`
	URI         string `json:"uri,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

type Prompt struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

type Counts struct {
	Tools     int `json:"tools"`
	Resources int `json:"resources"`
	Prompts   int `json:"prompts"`
}

func New(result mcp.DiscoveryResult, transport, version string, now time.Time) Report {
	r := Report{
		Schema: Schema, Tool: ToolMetadata{Name: ToolName, Version: version},
		RunTimestamp: now.UTC().Format(time.RFC3339Nano), TargetTransport: cap(transport),
		Server: ServerInfo{Name: result.ServerInfo.Name, Version: result.ServerInfo.Version, ProtocolVersion: result.ServerInfo.ProtocolVersion},
		Tools:  make([]Tool, 0, len(result.Tools)), Resources: make([]Resource, 0, len(result.Resources)),
		Prompts:  make([]Prompt, 0, len(result.Prompts)),
		Complete: true,
	}
	for _, item := range result.Tools {
		r.Tools = append(r.Tools, Tool{Name: cap(item.Name), Description: cap(item.Description), InputSchema: cloneJSON(item.InputSchema)})
	}
	for _, item := range result.Resources {
		r.Resources = append(r.Resources, Resource{Name: cap(item.Name), URI: cap(item.URI), Description: cap(item.Description), MimeType: cap(item.MimeType)})
	}
	for _, item := range result.Prompts {
		r.Prompts = append(r.Prompts, Prompt{Name: cap(item.Name), Description: cap(item.Description), Arguments: cloneJSON(item.Arguments)})
	}
	sort.Slice(r.Tools, func(i, j int) bool { return r.Tools[i].Name < r.Tools[j].Name })
	sort.Slice(r.Resources, func(i, j int) bool { return r.Resources[i].Name < r.Resources[j].Name })
	sort.Slice(r.Prompts, func(i, j int) bool { return r.Prompts[i].Name < r.Prompts[j].Name })
	r.Counts = Counts{Tools: len(r.Tools), Resources: len(r.Resources), Prompts: len(r.Prompts)}
	return r
}

func (r Report) Validate() error {
	if r.Schema != Schema {
		return fmt.Errorf("unsupported report schema %q", r.Schema)
	}
	if r.Tool.Name == "" || r.Tool.Version == "" || r.RunTimestamp == "" ||
		r.TargetTransport == "" || r.Server.Name == "" || r.Server.Version == "" ||
		r.Server.ProtocolVersion == "" {
		return errors.New("report is missing required fields")
	}
	if r.TargetTransport != "stdio" {
		return fmt.Errorf("unsupported report target transport %q", r.TargetTransport)
	}
	if !r.Complete {
		return errors.New("report is not complete")
	}
	if r.Counts.Tools != len(r.Tools) || r.Counts.Resources != len(r.Resources) || r.Counts.Prompts != len(r.Prompts) {
		return errors.New("report counts do not match enumerated items")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.RunTimestamp); err != nil {
		return errors.New("report run_timestamp must be RFC3339")
	}
	return nil
}

func Marshal(r Report) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode report: %w", err)
	}
	return append(data, '\n'), nil
}

func Parse(data []byte) (Report, error) {
	var r Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return Report{}, fmt.Errorf("parse report: %w", err)
	}
	if err := r.Validate(); err != nil {
		return Report{}, err
	}
	return r, nil
}

func WriteAtomic(path string, r Report) error {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || len(path) > MaxPathBytes {
		return errors.New("report path must be a non-empty relative path")
	}
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return errors.New("report parent directory does not exist")
		}
		return fmt.Errorf("inspect report parent: %w", err)
	} else if !info.IsDir() {
		return errors.New("report parent is not a directory")
	}
	if err := rejectSymlinkParents(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to overwrite symlink report path")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect report path: %w", err)
	}
	data, err := Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create report temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set report permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close report: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install report: %w", err)
	}
	return nil
}

func ValidateFile(path string) (Report, error) {
	if strings.TrimSpace(path) == "" {
		return Report{}, errors.New("report path must be non-empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read report: %w", err)
	}
	return Parse(data)
}

func cloneJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return normalized
}

func cap(value string) string {
	if len(value) > 64<<10 {
		return value[:64<<10]
	}
	return value
}

func rejectSymlinkParents(dir string) error {
	volume := filepath.VolumeName(dir)
	rest := strings.TrimPrefix(dir, volume)
	current := volume
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r == filepath.Separator || r == '/' || r == '\\' }) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect report parent: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing report path through symlink parent")
		}
	}
	return nil
}
