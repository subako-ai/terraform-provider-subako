package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The files a function bundle is made of, as the function push routes name
// them: the package's manifest and module at the root, and whatever it reads
// at run time under the assets directory.
const (
	functionManifestFile = "subako-package.json"
	functionModuleFile   = "module.wasm"
	functionAssetsDir    = "assets"
)

// Bounds on what is read of a function bundle directory. They keep a
// directory no upload could carry from being read into memory; the exact
// caps, and every other rule a bundle keeps, are the server's, and its
// rejection names what was wrong.
const (
	// CODESYNC(function-bundle-max-bytes)
	MaxFunctionBundleBytes = 64 * 1024 * 1024
	// The most a bundle's files may hold together: the archive the server
	// extracts is bounded by the bundle cap plus the tar framing of the most
	// assets a bundle may carry.
	// CODESYNC(function-assets-max-files)
	maxFunctionContentBytes = MaxFunctionBundleBytes + 10_000*6*512 + 2*512
)

// Function is a function bundle directory read into memory.
type Function struct {
	// Name and Description are what the manifest states.
	Name         string
	Description  string
	ModuleSha256 string
	// Assets lists each file under the assets directory by its path there.
	Assets []Entry
	files  []File
}

// ReadFunction walks dir into a Function. The manifest and the module must
// be at its root; anything else must sit under the assets directory, as the
// server refuses any other entry.
func ReadFunction(dir string) (*Function, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("reading function directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("reading function directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	walked, err := walk(dir, MaxFunctionBundleBytes)
	if err != nil {
		return nil, err
	}

	function := &Function{}
	var manifest []byte
	var read int64
	for _, file := range walked {
		if file.rel != functionManifestFile && file.rel != functionModuleFile &&
			!strings.HasPrefix(file.rel, functionAssetsDir+"/") {
			return nil, fmt.Errorf("%s is not part of a function bundle, which holds only %s, %s, and files under %s/",
				file.path, functionManifestFile, functionModuleFile, functionAssetsDir)
		}
		content, err := readAtMost(file.path, MaxFunctionBundleBytes+1)
		if err != nil {
			return nil, err
		}
		if int64(len(content)) > MaxFunctionBundleBytes {
			return nil, overFile(file.path, MaxFunctionBundleBytes)
		}
		read += int64(len(content))
		if read > maxFunctionContentBytes {
			return nil, fmt.Errorf("%s holds more than a function bundle may carry", dir)
		}
		sum := sha256.Sum256(content)
		digest := hex.EncodeToString(sum[:])
		function.files = append(function.files, File{Path: file.rel, Sha256: digest, bytes: content})
		switch {
		case file.rel == functionManifestFile:
			manifest = content
		case file.rel == functionModuleFile:
			function.ModuleSha256 = digest
		default:
			function.Assets = append(function.Assets, Entry{
				Path:   strings.TrimPrefix(file.rel, functionAssetsDir+"/"),
				Sha256: digest,
			})
		}
	}

	if manifest == nil {
		return nil, fmt.Errorf("%s holds no %s: a function bundle is the directory that file sits in", dir, functionManifestFile)
	}
	if function.ModuleSha256 == "" {
		return nil, fmt.Errorf("%s holds no %s", dir, functionModuleFile)
	}
	var stated struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(manifest, &stated); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", filepath.Join(dir, functionManifestFile), err)
	}
	function.Name = stated.Name
	function.Description = stated.Description
	return function, nil
}

// Hash fingerprints the bundle as FunctionHash does a stored version.
func (f *Function) Hash() string {
	return FunctionHash(f.Name, f.Description, f.ModuleSha256, f.Assets)
}

// FunctionHash fingerprints a function bundle by what a stored version
// answers: the manifest's name and description, the module's digest, and
// each asset's. The module lists the package's functions, so its digest
// covers them.
func FunctionHash(name, description, moduleSha256 string, assets []Entry) string {
	stated := sha256.Sum256([]byte(name + "\x00" + description))
	entries := []Entry{
		{Path: functionManifestFile, Sha256: hex.EncodeToString(stated[:])},
		{Path: functionModuleFile, Sha256: moduleSha256},
	}
	for _, asset := range assets {
		entries = append(entries, Entry{Path: functionAssetsDir + "/" + asset.Path, Sha256: asset.Sha256})
	}
	return Hash(entries)
}

// Pack builds the archive the push routes take.
func (f *Function) Pack() ([]byte, error) {
	return pack(f.files)
}
