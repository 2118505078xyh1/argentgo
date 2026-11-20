package plugins

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnsafeArchive reports an archive entry that would leave the install
// directory ("zip slip").
var ErrUnsafeArchive = errors.New("plugins: archive entry escapes the target directory")

const (
	maxZipEntries = 10000
	maxZipBytes   = 256 << 20
	stagingPrefix = ".install-"
)

// InstallOptions tunes an install.
type InstallOptions struct {
	// Force replaces an existing installation of the same name.
	Force bool
}

// Install installs a local directory or a .zip into pluginsDir. An existing
// installation is an error; use InstallWith with Force to replace it.
func Install(source, pluginsDir string) (*Bundle, error) {
	return InstallWith(source, pluginsDir, InstallOptions{})
}

// InstallWith installs a bundle and enables it.
//
// A directory source must contain a manifest. A .zip source is extracted into
// a staging directory first; every entry is checked against pluginsDir, so an
// archive member named "../x" is refused instead of written outside.
func InstallWith(source, pluginsDir string, opts InstallOptions) (*Bundle, error) {
	source = filepath.Clean(source)
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("plugins: read source %s: %w", source, err)
	}
	if pluginsDir == "" {
		return nil, errors.New("plugins: install needs a plugins directory")
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("plugins: create %s: %w", pluginsDir, err)
	}

	bundleRoot := source
	if !info.IsDir() {
		staging, err := os.MkdirTemp(pluginsDir, stagingPrefix)
		if err != nil {
			return nil, fmt.Errorf("plugins: create staging directory: %w", err)
		}
		defer os.RemoveAll(staging)
		if err := extractZipFile(source, staging); err != nil {
			return nil, err
		}
		bundleRoot, err = locateBundleRoot(staging)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
	}

	manifest, err := LoadManifest(bundleRoot)
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(pluginsDir, manifest.Name)
	if filepath.Clean(bundleRoot) == destination {
		return nil, fmt.Errorf("plugins: %q is already installed at %s", manifest.Name, destination)
	}

	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(destination)
			if err != nil || !within(pluginsDir, resolved) {
				return nil, fmt.Errorf("plugins: install target %s is a symlink leaving %s", destination, pluginsDir)
			}
		}
		if !opts.Force {
			return nil, fmt.Errorf("plugins: %q is already installed at %s; set Force to replace it", manifest.Name, destination)
		}
		if err := os.RemoveAll(destination); err != nil {
			return nil, fmt.Errorf("plugins: remove %s: %w", destination, err)
		}
	}

	if err := copyTree(bundleRoot, destination); err != nil {
		return nil, err
	}
	installed, err := LoadManifest(destination)
	if err != nil {
		return nil, err
	}
	if err := SetEnabled(pluginsDir, installed.Name, true); err != nil {
		return nil, err
	}
	return &Bundle{
		Name:     installed.Name,
		Version:  installed.Version,
		Dir:      installed.Root,
		Manifest: installed,
		Enabled:  true,
		Problems: installed.Validate(),
	}, nil
}

// Uninstall removes an installed bundle by name, but only when the directory's
// manifest agrees with the name, so a typo cannot delete an unrelated folder.
func Uninstall(name, pluginsDir string) (bool, error) {
	if err := ValidateName(name); err != nil {
		return false, err
	}
	candidate := filepath.Join(pluginsDir, name)
	manifest, err := LoadManifest(candidate)
	if err != nil || manifest.Name != name {
		return false, nil
	}
	if err := os.RemoveAll(candidate); err != nil {
		return false, fmt.Errorf("plugins: remove %s: %w", candidate, err)
	}
	if err := SetEnabled(pluginsDir, name, false); err != nil {
		return true, err
	}
	return true, nil
}

// locateBundleRoot finds the directory holding the manifest inside an
// extracted archive: the root itself, or a single top-level folder.
func locateBundleRoot(dir string) (string, error) {
	if _, ok := FindManifest(dir); ok {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("plugins: read %s: %w", dir, err)
	}
	var candidates []string
	for _, entry := range entries {
		candidate := filepath.Join(dir, entry.Name())
		if !isDir(candidate) {
			continue
		}
		if _, ok := FindManifest(candidate); ok {
			candidates = append(candidates, candidate)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", ErrNoManifest
	default:
		return "", fmt.Errorf("plugins: archive contains %d plugin directories", len(candidates))
	}
}

func extractZipFile(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("plugins: open archive %s: %w", archivePath, err)
	}
	defer reader.Close()
	if len(reader.File) > maxZipEntries {
		return fmt.Errorf("plugins: archive %s has %d entries, more than %d", archivePath, len(reader.File), maxZipEntries)
	}

	var written int64
	for _, entry := range reader.File {
		target, err := safeArchivePath(destination, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("plugins: create %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("plugins: create %s: %w", filepath.Dir(target), err)
		}
		if err := copyZipEntry(entry, target, &written); err != nil {
			return err
		}
	}
	return nil
}

func copyZipEntry(entry *zip.File, target string, written *int64) error {
	in, err := entry.Open()
	if err != nil {
		return fmt.Errorf("plugins: open archive entry %q: %w", entry.Name, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("plugins: create %s: %w", target, err)
	}
	remaining := maxZipBytes - *written
	// One extra byte proves the archive is too large instead of silently
	// truncating a file.
	count, copyErr := io.Copy(out, io.LimitReader(in, remaining+1))
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("plugins: extract %q: %w", entry.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("plugins: close %s: %w", target, closeErr)
	}
	*written += count
	if *written > maxZipBytes {
		return fmt.Errorf("plugins: archive expands beyond %d bytes", int64(maxZipBytes))
	}
	return nil
}

// safeArchivePath maps one archive member into destination, refusing absolute
// paths, drive letters, ".." components and anything that resolves outside.
func safeArchivePath(destination, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty entry name", ErrUnsafeArchive)
	}
	if strings.ContainsRune(name, 0) || strings.ContainsRune(name, ':') {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	cleaned := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if cleaned == "." {
		return destination, nil
	}
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	target := filepath.Join(destination, filepath.FromSlash(cleaned))
	if !within(destination, target) || filepath.Clean(target) == filepath.Clean(destination) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	return target, nil
}

// copyTree copies a directory, skipping .git so a checkout installs cleanly.
func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("plugins: create %s: %w", destination, err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("plugins: read %s: %w", source, err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		info, err := os.Lstat(from)
		if err != nil {
			return fmt.Errorf("plugins: read %s: %w", from, err)
		}
		// Never follow symlinks: a link to an ancestor (or to itself) would
		// recurse forever under os.Stat, and a link to an outside directory
		// would copy files from beyond the bundle. Lstat reports the link
		// itself, so skip it rather than treating it as a dir/file.
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		switch {
		case info.IsDir():
			if err := copyTree(from, to); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("plugins: read %s: %w", source, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("plugins: create %s: %w", filepath.Dir(destination), err)
	}
	out, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("plugins: create %s: %w", destination, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("plugins: copy %s to %s: %w", source, destination, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("plugins: close %s: %w", destination, err)
	}
	return nil
}
