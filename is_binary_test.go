package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestIsBinaryFile_PlainText is the common case: source files are
// reported as editable. The picker should surface every .go / .md
// / .sh file the user might want to open in $EDITOR.
func TestIsBinaryFile_PlainText(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"go.txt":      []byte("package main\n\nfunc main() {}\n"),
		"shell.sh":    []byte("#!/usr/bin/env bash\necho hello\n"),
		"markdown.md": []byte("# Title\n\nSome *emphasized* text.\n"),
		"empty":       nil,
		"single-byte": []byte("a"),
		"short-ascii": []byte("hi\n"),
	}
	for name, data := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if isBinaryFile(path) {
			t.Errorf("%s: isBinaryFile = true, want false (text should be editable)", name)
		}
	}
}

// TestIsBinaryFile_ELF covers the historical case the old
// implementation handled: Linux ELF binaries. Should still be
// detected as binary.
func TestIsBinaryFile_ELF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-elf")
	// 4 bytes of ELF magic followed by padding so ReadFull gets
	// something to chew on.
	data := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{0}, 64)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isBinaryFile(path) {
		t.Errorf("ELF: isBinaryFile = false, want true")
	}
}

// TestIsBinaryFile_OtherBinaries covers the magic bytes of common
// binary formats the OLD implementation missed: Mach-O (macOS),
// PE (Windows .exe), Java class files. Each should now be detected
// as binary thanks to http.DetectContentType.
func TestIsBinaryFile_OtherBinaries(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"macho32": {0xfe, 0xed, 0xfa, 0xce, 0x00, 0x00, 0x00, 0x0c}, // Mach-O 32
		"macho64": {0xfe, 0xed, 0xfa, 0xcf, 0x00, 0x00, 0x00, 0x0c}, // Mach-O 64
		"java":    {0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x34}, // Java class
		"windows": {'M', 'Z', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},   // PE / .exe
		"wasm":    {0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00},    // WebAssembly
		"pdf":     {'%', 'P', 'D', 'F', '-', '1', '.', '4'},         // PDF
		"png":     {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},    // PNG image
		"jpeg":    {0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F'},   // JPEG image
		"zip":     {'P', 'K', 0x03, 0x04, 0x00, 0x00, 0x00, 0x00},   // ZIP / docx / jar
		"gzip":    {0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}, // gzip
	}
	for name, data := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if !isBinaryFile(path) {
			t.Errorf("%s: isBinaryFile = false, want true (should be detected as binary)", name)
		}
	}
}

// TestIsBinaryFile_BinaryWithTextName covers the edge case where a
// file has a .txt extension but is actually binary (e.g. a user
// renamed a binary). isBinaryFile should not be fooled by the
// extension \xe2\x80\x94 it inspects the actual content.
func TestIsBinaryFile_BinaryWithTextName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "looks-like.txt")
	// Random bytes that don't match any known text format.
	data := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isBinaryFile(path) {
		t.Errorf("random bytes: isBinaryFile = false, want true")
	}
}

// TestIsBinaryFile_MissingFile is the "broken on disk" case: we
// should report the file as editable (false) so the picker surfaces
// it and the editor can produce the real error message. A false
// negative ("it's binary, skip it") would silently hide a real
// problem from the user.
func TestIsBinaryFile_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")
	if isBinaryFile(path) {
		t.Errorf("missing file: isBinaryFile = true, want false (let editor fail loudly)")
	}
}

// TestIsBinaryFile_LargeTextFile covers a >512-byte text file: the
// detector reads up to 512 bytes, which is enough to identify the
// content as text. Anything past the first 512 bytes doesn't affect
// the decision (and shouldn't, to keep the syscall cheap).
func TestIsBinaryFile_LargeTextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	data := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if isBinaryFile(path) {
		t.Errorf("large text: isBinaryFile = true, want false")
	}
}

// TestIsBinaryFile_ZeroByteFile treats an empty file as editable.
// The detector returns application/octet-stream for an empty
// input, but we early-return false for zero-length content (see
// the len(buf) == 0 guard). This matches user expectation: an
// empty file opened in $EDITOR is just an empty buffer.
func TestIsBinaryFile_ZeroByteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if isBinaryFile(path) {
		t.Errorf("empty file: isBinaryFile = true, want false (empty file is editable)")
	}
}
