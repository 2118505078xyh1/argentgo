package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
)

const (
	maxToolOutput = 40000
	maxMatches    = 200
)

// Tool is one callable capability.
type Tool interface {
	Name() string
	Description() string
	Schema() string
	Mutating() bool
	Run(ctx context.Context, root string, args json.RawMessage) (string, error)
}

// ToolMeta is the in-process reclamation hint a tool attaches to its result.
// It never crosses the wire: the agent loop uses it to stub superseded reads
// and empty searches while the run is alive.
type ToolMeta struct {
	SupersedeKey string
	Useless      bool
}

// metaTool is implemented by tools that can describe their own result for
// context reclamation. Output and error are passed in so a failed call never
// claims a supersede key or an "empty result" marker.
type metaTool interface {
	Meta(args json.RawMessage, output string, err error) ToolMeta
}

// Guard is consulted before every tool call. A non-nil error is returned to the
// model as a tool error; the tool never runs.
type Guard interface {
	Check(ctx context.Context, name string, args json.RawMessage) error
}

// Registry owns the tool set and enforces the workspace boundary.
type Registry struct {
	root       string
	yolo       bool
	maxOutput  int
	maxTimeout time.Duration
	sessionDir string
	jobs       *jobs.Registry
	guard      Guard
	tools      map[string]Tool
	order      []string
}
