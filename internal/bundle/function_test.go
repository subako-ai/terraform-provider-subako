package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func functionDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "subako-package.json", `{"abi": 1, "name": "words", "description": "Looks words up."}`)
	write(t, dir, "module.wasm", "\x00asm")
	write(t, dir, "assets/data/words.txt", "hello\n")
	write(t, dir, "assets/b.bin", "\x01\x02")
	return dir
}

func TestReadFunctionReadsTheManifestModuleAndAssets(t *testing.T) {
	f, err := ReadFunction(functionDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "words" || f.Description != "Looks words up." {
		t.Fatalf("manifest = %q, %q", f.Name, f.Description)
	}
	if f.ModuleSha256 != digest("\x00asm") {
		t.Fatalf("module digest = %s", f.ModuleSha256)
	}
	want := []Entry{{Path: "b.bin", Sha256: digest("\x01\x02")}, {Path: "data/words.txt", Sha256: digest("hello\n")}}
	if len(f.Assets) != len(want) || f.Assets[0] != want[0] || f.Assets[1] != want[1] {
		t.Fatalf("assets = %v, want %v", f.Assets, want)
	}
}

// A stored version answers the manifest, the module's digest, and each
// asset's, so a directory and the version pushed from it fingerprint alike.
func TestAFunctionHashesAsTheVersionPushedFromIt(t *testing.T) {
	f, err := ReadFunction(functionDir(t))
	if err != nil {
		t.Fatal(err)
	}
	stored := FunctionHash("words", "Looks words up.", digest("\x00asm"), []Entry{
		{Path: "data/words.txt", Sha256: digest("hello\n")},
		{Path: "b.bin", Sha256: digest("\x01\x02")},
	})
	if f.Hash() != stored {
		t.Fatalf("directory %s, stored version %s", f.Hash(), stored)
	}
}

func TestAFunctionHashChangesWithEachPart(t *testing.T) {
	base := FunctionHash("words", "d", "m", []Entry{{Path: "a", Sha256: "x"}})
	for name, other := range map[string]string{
		"the name":         FunctionHash("other", "d", "m", []Entry{{Path: "a", Sha256: "x"}}),
		"the description":  FunctionHash("words", "e", "m", []Entry{{Path: "a", Sha256: "x"}}),
		"the module":       FunctionHash("words", "d", "n", []Entry{{Path: "a", Sha256: "x"}}),
		"an asset's bytes": FunctionHash("words", "d", "m", []Entry{{Path: "a", Sha256: "y"}}),
		"an asset's path":  FunctionHash("words", "d", "m", []Entry{{Path: "b", Sha256: "x"}}),
		"no assets at all": FunctionHash("words", "d", "m", nil),
		"name/description": FunctionHash("words\x00d", "", "m", []Entry{{Path: "a", Sha256: "x"}}),
	} {
		if other == base {
			t.Errorf("changing %s kept the hash", name)
		}
	}
}

func TestAFunctionPacksEveryFileUnderItsPath(t *testing.T) {
	f, err := ReadFunction(functionDir(t))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := f.Pack()
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if got, want := strings.Join(names, ","), "assets/b.bin,assets/data/words.txt,module.wasm,subako-package.json"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
}

func TestReadFunctionRefusesWhatABundleMayNotBe(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(t *testing.T, dir string)
		err  string
	}{
		"no manifest": {func(t *testing.T, dir string) {
			_ = os.Remove(filepath.Join(dir, "subako-package.json"))
		}, "holds no subako-package.json"},
		"no module": {func(t *testing.T, dir string) {
			_ = os.Remove(filepath.Join(dir, "module.wasm"))
		}, "holds no module.wasm"},
		"a file outside assets": {func(t *testing.T, dir string) {
			write(t, dir, "src/lib.rs", "fn main() {}")
		}, "is not part of a function bundle"},
		"a manifest that does not parse": {func(t *testing.T, dir string) {
			write(t, dir, "subako-package.json", "{")
		}, "does not parse"},
		"a symlink": {func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "module.wasm"), filepath.Join(dir, "assets", "link")); err != nil {
				t.Skip("no symlinks here:", err)
			}
		}, "is a symlink"},
		"a file past the bundle cap": {func(t *testing.T, dir string) {
			sparse(t, filepath.Join(dir, "assets", "big"), MaxFunctionBundleBytes+1)
		}, "more than the"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := functionDir(t)
			tc.edit(t, dir)
			_, err := ReadFunction(dir)
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err = %v, want one naming %q", err, tc.err)
			}
		})
	}
}
