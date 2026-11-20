package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// yamlExportHeader marks a transcript export as a YAML document. A leading
// comment is legal YAML and makes the otherwise JSON body self-describing.
const yamlExportHeader = "# argent session export - YAML 1.2 (JSON-compatible)\n"

// jsonExportToYAML renders a session JSON transcript as a YAML document without
// any third-party YAML dependency. JSON is a proper subset of YAML 1.2, so the
// pretty-printed JSON produced by session.ExportProject is already a valid YAML
// document: it preserves every field (including CJK text as raw UTF-8) and is
// parseable by both JSON and YAML readers. We only validate the payload and
// prefix a YAML comment marker; no data is rewritten.
func jsonExportToYAML(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("session: invalid JSON export cannot be rendered as YAML")
	}
	out := make([]byte, 0, len(yamlExportHeader)+len(data))
	out = append(out, yamlExportHeader...)
	out = append(out, data...)
	return out, nil
}

// isYAMLExport reports whether a user-supplied --format value selects the YAML
// export. Case and surrounding whitespace are ignored, matching the other
// structured formats handled by session.ExportProject.
func isYAMLExport(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "yaml")
}
