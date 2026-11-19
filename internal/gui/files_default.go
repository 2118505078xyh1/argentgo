package gui

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The injected-only pages still have useful local implementations: workflow
// definitions are JSON files and office documents are ZIP containers with XML
// text. Both use only the standard library, so the GUI does not need another
// backend to show real workspace data.
type fileWorkflows struct{ dir string }

// NewFileWorkflows returns a Workflows backend that discovers *.json workflow
// definitions below dir. Hidden and dependency directories are skipped.
func NewFileWorkflows(dir string) Workflows { return &fileWorkflows{dir: dir} }

type workflowProbe struct {
	Name  string            `json:"name"`
	Steps []json.RawMessage `json:"steps"`
}

func (f *fileWorkflows) List() ([]WorkflowRow, error) {
	rows := []WorkflowRow{}
	err := filepath.WalkDir(f.dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != f.dir && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "target") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var probe workflowProbe
		if err := json.Unmarshal(data, &probe); err != nil || len(probe.Steps) == 0 {
			return nil // not a workflow definition
		}
		relative, err := filepath.Rel(f.dir, path)
		if err != nil {
			relative = path
		}
		name := strings.TrimSpace(probe.Name)
		if name == "" {
			name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		rows = append(rows, WorkflowRow{
			Path:       filepath.ToSlash(relative),
			Name:       name,
			Validation: WorkflowValidation{OK: true, Steps: len(probe.Steps)},
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows, nil
}

func (f *fileWorkflows) Show(path string) (string, error) {
	target, err := resolve(f.dir, path, true)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(target)
	return string(data), err
}

type fileDocuments struct{ dir string }

var documentExtensions = map[string]bool{
	".docx": true, ".odt": true, ".txt": true, ".md": true,
}

// NewFileDocuments returns a Documents backend that lists text and office
// documents below dir. Read extracts text from OOXML / ODF containers and
// returns plain files verbatim, with no external converter.
func NewFileDocuments(dir string) Documents { return &fileDocuments{dir: dir} }

func (f *fileDocuments) List() ([]DocumentRow, error) {
	rows := []DocumentRow{}
	err := filepath.WalkDir(f.dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != f.dir && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !documentExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		relative, err := filepath.Rel(f.dir, path)
		if err != nil {
			relative = path
		}
		rows = append(rows, DocumentRow{
			Name: entry.Name(),
			Path: filepath.ToSlash(relative),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows, nil
}

func (f *fileDocuments) Read(path string) (string, error) {
	target, err := resolve(f.dir, path, true)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(target))
	if ext == ".txt" || ext == ".md" {
		data, err := os.ReadFile(target)
		return string(data), err
	}
	return readZipText(target)
}

// readZipText concatenates text nodes from an OOXML / ODF package. The XML
// namespaces differ, so matching is done on the local name only.
func readZipText(path string) (string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("documents: open %s: %w", path, err)
	}
	defer archive.Close()
	var out strings.Builder
	for _, file := range archive.File {
		ext := strings.ToLower(filepath.Ext(file.Name))
		if ext != ".xml" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return "", err
		}
		decoder := xml.NewDecoder(reader)
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = reader.Close()
				return "", err
			}
			if start, ok := token.(xml.StartElement); ok && xmlLocal(start.Name.Local) == "t" {
				var value string
				if err := decoder.DecodeElement(&value, &start); err == nil {
					out.WriteString(value)
					out.WriteByte('\n')
				}
			}
		}
		_ = reader.Close()
	}
	return out.String(), nil
}

func xmlLocal(name string) string {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		return name[i+1:]
	}
	return name
}
