package deviceprofilecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddc"
)

const Schema = 2

type Identity struct {
	FormFactor     string `json:"formFactor,omitempty"`
	SysVendor      string `json:"sysVendor,omitempty"`
	ProductName    string `json:"productName,omitempty"`
	ProductVersion string `json:"productVersion,omitempty"`
	BoardVendor    string `json:"boardVendor,omitempty"`
	BoardName      string `json:"boardName,omitempty"`
	BoardVersion   string `json:"boardVersion,omitempty"`
}

type Source struct {
	Schema            int    `json:"schema"`
	ODDCKind          string `json:"oddcKind"`
	ODDCRepository    string `json:"oddcRepository"`
	ODDCRevision      string `json:"oddcRevision"`
	ODDCIntegrity     string `json:"oddcIntegrity,omitempty"`
	GjallarOSRevision string `json:"gjallarOSRevision"`
	NixOSRelease      string `json:"nixOSRelease"`
}

type FileIntegrity struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Schema           int             `json:"schema"`
	ModelID          string          `json:"modelId,omitempty"`
	Files            []FileIntegrity `json:"files"`
	GenerationReason string          `json:"generationReason"`
}

type Capsule struct {
	Identity Identity
	Source   Source
	Manifest Manifest
}

type MaterializeInput struct {
	Destination       string
	Identity          oddc.Identity
	Resolved          oddc.Resolved
	Source            oddc.EmbeddedSource
	GjallarOSRevision string
	NixOSRelease      string
	GenerationReason  string
}

func Materialize(input MaterializeInput) (Capsule, error) {
	if strings.TrimSpace(input.Destination) == "" {
		return Capsule{}, fmt.Errorf("device-profile capsule destination is required")
	}
	if strings.TrimSpace(input.GjallarOSRevision) == "" {
		return Capsule{}, fmt.Errorf("gjallarOS revision is required")
	}
	if strings.TrimSpace(input.NixOSRelease) == "" {
		return Capsule{}, fmt.Errorf("NixOS release is required")
	}
	if strings.TrimSpace(input.GenerationReason) == "" {
		input.GenerationReason = "initial"
	}

	parent := filepath.Dir(filepath.Clean(input.Destination))
	if err := os.MkdirAll(parent, 0700); err != nil {
		return Capsule{}, fmt.Errorf("create device-profile capsule parent: %w", err)
	}

	stage, err := os.MkdirTemp(parent, ".device-profile-stage-")
	if err != nil {
		return Capsule{}, fmt.Errorf("create device-profile capsule staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	if err := os.Chmod(stage, 0700); err != nil {
		return Capsule{}, fmt.Errorf("protect device-profile capsule staging directory: %w", err)
	}

	oddcRoot := filepath.Join(stage, "oddc")
	if err := os.MkdirAll(oddcRoot, 0700); err != nil {
		return Capsule{}, fmt.Errorf(
			"create cached canonical ODDC directory: %w",
			err,
		)
	}

	materializedFiles, err := cachePortableODDCSource(
		input.Source.Root,
		oddcRoot,
	)
	if err != nil {
		return Capsule{}, err
	}

	if strings.TrimSpace(input.Resolved.ModelID) != "" {
		resolvedSnapshot := filepath.Join(
			oddcRoot,
			"resolved.json",
		)

		if err := writeJSON(
			resolvedSnapshot,
			input.Resolved.Canonical,
		); err != nil {
			return Capsule{}, err
		}

		materializedFiles = append(
			materializedFiles,
			resolvedSnapshot,
		)
	}

	files := make([]FileIntegrity, 0, len(materializedFiles))
	for _, path := range materializedFiles {
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return Capsule{}, fmt.Errorf(
				"resolve cached oddc file path: %w",
				err,
			)
		}

		integrity, err := fileIntegrity(path)
		if err != nil {
			return Capsule{}, err
		}
		integrity.Path = filepath.ToSlash(rel)
		files = append(files, integrity)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	identity := Identity{
		FormFactor:     input.Identity.FormFactor,
		SysVendor:      input.Identity.SysVendor,
		ProductName:    input.Identity.ProductName,
		ProductVersion: input.Identity.ProductVersion,
		BoardVendor:    input.Identity.BoardVendor,
		BoardName:      input.Identity.BoardName,
		BoardVersion:   input.Identity.BoardVersion,
	}

	sourceMetadata := input.Resolved.Source
	if strings.TrimSpace(sourceMetadata.Kind) == "" {
		sourceMetadata = input.Source.Metadata()
	}
	if strings.TrimSpace(sourceMetadata.Revision) == "" {
		return Capsule{}, fmt.Errorf("oddc source revision is required")
	}

	source := Source{
		Schema:            Schema,
		ODDCKind:          sourceMetadata.Kind,
		ODDCRepository:    sourceMetadata.Repository,
		ODDCRevision:      sourceMetadata.Revision,
		ODDCIntegrity:     sourceMetadata.Integrity,
		GjallarOSRevision: input.GjallarOSRevision,
		NixOSRelease:      input.NixOSRelease,
	}

	manifest := Manifest{
		Schema:           Schema,
		ModelID:          input.Resolved.ModelID,
		Files:            files,
		GenerationReason: input.GenerationReason,
	}

	if err := writeJSON(filepath.Join(stage, "identity.json"), identity); err != nil {
		return Capsule{}, err
	}
	if err := writeJSON(filepath.Join(stage, "source.json"), source); err != nil {
		return Capsule{}, err
	}
	if err := writeJSON(filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return Capsule{}, err
	}

	if err := activateCapsule(stage, input.Destination); err != nil {
		return Capsule{}, err
	}

	return Capsule{
		Identity: identity,
		Source:   source,
		Manifest: manifest,
	}, nil
}

func activateCapsule(stage, destination string) error {
	backup := destination + ".previous"

	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove stale device-profile capsule backup: %w", err)
	}

	hadPrevious := false
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("preserve previous device-profile capsule: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect previous device-profile capsule: %w", err)
	}

	if err := os.Rename(stage, destination); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, destination); restoreErr != nil {
				return fmt.Errorf(
					"activate device-profile capsule: %v; restore previous capsule: %w",
					err,
					restoreErr,
				)
			}
		}
		return fmt.Errorf("activate device-profile capsule: %w", err)
	}

	if hadPrevious {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf(
				"remove previous device-profile capsule backup: %w",
				err,
			)
		}
	}

	return nil
}

func Load(root string) (Capsule, error) {
	var capsule Capsule

	if err := readJSON(filepath.Join(root, "identity.json"), &capsule.Identity); err != nil {
		return Capsule{}, err
	}
	if err := readJSON(filepath.Join(root, "source.json"), &capsule.Source); err != nil {
		return Capsule{}, err
	}
	if err := readJSON(filepath.Join(root, "manifest.json"), &capsule.Manifest); err != nil {
		return Capsule{}, err
	}

	if capsule.Source.Schema != Schema {
		return Capsule{}, fmt.Errorf(
			"unsupported device-profile source schema %d",
			capsule.Source.Schema,
		)
	}

	if capsule.Manifest.Schema != Schema {
		return Capsule{}, fmt.Errorf(
			"unsupported device-profile manifest schema %d",
			capsule.Manifest.Schema,
		)
	}

	if strings.TrimSpace(capsule.Manifest.ModelID) != "" &&
		!strings.HasPrefix(
			strings.TrimSpace(capsule.Manifest.ModelID),
			"model/",
		) {
		return Capsule{}, fmt.Errorf(
			"canonical recovery capsule has invalid model id %q",
			capsule.Manifest.ModelID,
		)
	}

	return capsule, nil
}

func Verify(root string) (Capsule, error) {
	capsule, err := Load(root)
	if err != nil {
		return Capsule{}, err
	}

	expectedFiles := make(map[string]bool, len(capsule.Manifest.Files))

	for _, expected := range capsule.Manifest.Files {
		if strings.TrimSpace(expected.Path) == "" {
			return Capsule{}, fmt.Errorf("device-profile manifest contains empty file path")
		}

		canonical := filepath.ToSlash(
			filepath.Clean(filepath.FromSlash(expected.Path)),
		)
		if canonical != expected.Path {
			return Capsule{}, fmt.Errorf(
				"device-profile manifest contains non-canonical file path %q",
				expected.Path,
			)
		}

		if !allowedODDCCapsulePath(expected.Path) {
			return Capsule{}, fmt.Errorf(
				"device-profile manifest contains non-ODDC path %q",
				expected.Path,
			)
		}
		if expectedFiles[expected.Path] {
			return Capsule{}, fmt.Errorf(
				"device-profile manifest contains duplicate file path %q",
				expected.Path,
			)
		}
		expectedFiles[expected.Path] = true

		path := filepath.Join(root, filepath.FromSlash(expected.Path))
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return Capsule{}, err
		}
		if rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return Capsule{}, fmt.Errorf(
				"device-profile manifest path escapes capsule: %q",
				expected.Path,
			)
		}

		actual, err := fileIntegrity(path)
		if err != nil {
			return Capsule{}, err
		}

		if actual.SHA256 != expected.SHA256 || actual.Size != expected.Size {
			return Capsule{}, fmt.Errorf(
				"device-profile capsule integrity mismatch for %s",
				expected.Path,
			)
		}
	}

	hasModel := strings.TrimSpace(
		capsule.Manifest.ModelID,
	) != ""

	hasResolved :=
		expectedFiles["oddc/resolved.json"]

	if hasModel && !hasResolved {
		return Capsule{}, fmt.Errorf(
			"canonical recovery capsule with model id is missing resolved.json",
		)
	}

	if !hasModel && hasResolved {
		return Capsule{}, fmt.Errorf(
			"canonical recovery capsule without model id unexpectedly contains resolved.json",
		)
	}

	hasEntity := false
	for path := range expectedFiles {
		if strings.HasPrefix(
			path,
			"oddc/catalog/entities/",
		) {
			hasEntity = true
			break
		}
	}

	if hasModel && !hasEntity {
		return Capsule{}, fmt.Errorf(
			"canonical recovery capsule contains no entity registry",
		)
	}

	oddcRoot := filepath.Join(root, "oddc")
	err = filepath.WalkDir(
		oddcRoot,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)

			if !expectedFiles[rel] {
				return fmt.Errorf(
					"device-profile capsule contains unlisted ODDC file %s",
					rel,
				)
			}
			return nil
		},
	)
	if err != nil {
		return Capsule{}, err
	}

	return capsule, nil
}

func NeedsRebind(
	cached Capsule,
	current oddc.Identity,
	resolved oddc.Resolved,
) bool {
	if resolved.ModelID != "" {
		if normalize(cached.Manifest.ModelID) !=
			normalize(resolved.ModelID) {
			return true
		}
	} else if normalize(cached.Manifest.ModelID) != "" {
		return true
	}

	return normalize(cached.Identity.FormFactor) != normalize(current.FormFactor) ||
		normalize(cached.Identity.SysVendor) != normalize(current.SysVendor) ||
		normalize(cached.Identity.ProductName) != normalize(current.ProductName) ||
		normalize(cached.Identity.BoardVendor) != normalize(current.BoardVendor) ||
		normalize(cached.Identity.BoardName) != normalize(current.BoardName)
}

func copyRegularFile(source, destination string) error {
	entry, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect source file %s: %w", source, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source file is a symlink: %s", source)
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf("source file is not regular: %s", source)
	}

	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file %s: %w", source, err)
	}
	defer input.Close()

	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened source file %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("opened source file is not regular: %s", source)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return fmt.Errorf(
			"create cached source directory: %w",
			err,
		)
	}

	output, err := os.OpenFile(
		destination,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0600,
	)
	if err != nil {
		return fmt.Errorf(
			"create cached source file %s: %w",
			destination,
			err,
		)
	}

	copied := false
	defer func() {
		output.Close()
		if !copied {
			os.Remove(destination)
		}
	}()

	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf(
			"copy cached source file %s: %w",
			source,
			err,
		)
	}

	if err := output.Close(); err != nil {
		return fmt.Errorf(
			"close cached source file %s: %w",
			destination,
			err,
		)
	}

	copied = true
	return nil
}

func fileIntegrity(path string) (FileIntegrity, error) {
	entry, err := os.Lstat(path)
	if err != nil {
		return FileIntegrity{}, fmt.Errorf("inspect device-profile capsule file %s: %w", path, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return FileIntegrity{}, fmt.Errorf(
			"device-profile capsule file is a symlink: %s",
			path,
		)
	}
	if !entry.Mode().IsRegular() {
		return FileIntegrity{}, fmt.Errorf(
			"device-profile capsule file is not regular: %s",
			path,
		)
	}

	f, err := os.Open(path)
	if err != nil {
		return FileIntegrity{}, fmt.Errorf("open device-profile capsule file %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return FileIntegrity{}, fmt.Errorf("inspect opened device-profile capsule file %s: %w", path, err)
	}

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return FileIntegrity{}, fmt.Errorf("hash device-profile capsule file %s: %w", path, err)
	}

	return FileIntegrity{
		SHA256: hex.EncodeToString(hash.Sum(nil)),
		Size:   info.Size(),
	}, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize device-profile capsule %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write device-profile capsule %s: %w", filepath.Base(path), err)
	}
	return nil
}

func readJSON(path string, value any) error {
	entry, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf(
			"inspect device-profile capsule %s: %w",
			filepath.Base(path),
			err,
		)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"device-profile capsule metadata is a symlink: %s",
			filepath.Base(path),
		)
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf(
			"device-profile capsule metadata is not regular: %s",
			filepath.Base(path),
		)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open device-profile capsule %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf(
			"inspect opened device-profile capsule %s: %w",
			filepath.Base(path),
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf(
			"opened device-profile capsule metadata is not regular: %s",
			filepath.Base(path),
		)
	}

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode device-profile capsule %s: %w", filepath.Base(path), err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf(
				"decode device-profile capsule %s: trailing JSON value",
				filepath.Base(path),
			)
		}
		return fmt.Errorf(
			"decode device-profile capsule %s trailing data: %w",
			filepath.Base(path),
			err,
		)
	}

	return nil
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
