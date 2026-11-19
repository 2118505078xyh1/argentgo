package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// telemetryPreference is the no-op value written by `telemetry off`. This
// personal build never collects or transmits anything; the file only records
// the user's explicit choice so a GUI can show the same state.
type telemetryPreference struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	Reason  string `json:"reason"`
}

const personalBuildTelemetry = "telemetry disabled in personal build"

// runTelemetry implements `telemetry <status|off>`. It deliberately has no
// network path: status reports, off persists a no-op preference.
func runTelemetry(app App, _ context.Context, args []string) int {
	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if len(args) > 1 {
		telemetryUsage(app.IO.Err)
		return 2
	}
	switch action {
	case "-h", "--help":
		telemetryUsage(app.IO.Out)
		return 0
	case "status":
		fmt.Fprintf(app.IO.Out, "%s; no data is collected or sent\n", personalBuildTelemetry)
		return 0
	case "off":
		return telemetryOff(app)
	default:
		fmt.Fprintf(app.IO.Err, "argent: unknown telemetry action %q\n", action)
		telemetryUsage(app.IO.Err)
		return 2
	}
}

func telemetryOff(app App) int {
	dir := app.storageDir()
	path := filepath.Join(dir, "telemetry.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(app.IO.Err, "error: create telemetry storage:", err)
		return 1
	}
	data, err := json.MarshalIndent(telemetryPreference{
		Enabled: false,
		Mode:    "off",
		Reason:  "personal-build-noop",
	}, "", "  ")
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error: encode telemetry preference:", err)
		return 1
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintln(app.IO.Err, "error: write telemetry preference:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "%s; no-op preference saved to %s\n", personalBuildTelemetry, path)
	return 0
}

func telemetryUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent telemetry status|off

Reports the personal-build telemetry state, or saves a no-op "off" preference
under the storage directory. Nothing is ever transmitted.
`)
}
