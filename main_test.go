// Copyright 2026 The docker-env-replace authors. All rights reserved.
// Use of this source code is governed by a BSD-style license.

// Package main holds the tests for the docker-env-replace utility. Every test
// that reads or writes the process environment takes the envMu lock so
// that the environment cannot change between a test's t.Setenv call and
// its use of loadConfig/run.
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// envMu serializes all tests that touch environment variables or the
// shared DOCKER_ENV_REPLACE_* configuration.
var envMu sync.Mutex

// testName is a unique prefix for variables used by a single test.
var testName int

// freshVar returns a unique environment variable name.
func freshVar(name string) string {
	testName++
	return fmt.Sprintf("DOCKER_ENV_REPLACE_TEST_%d_%s", testName, name)
}

// freshInputDir prepares a new input directory and points the
// DOCKER_ENV_REPLACE_* configuration at it plus a fresh output directory.
// It returns the input and output paths.
func freshInputDir(t *testing.T) (string, string) {
	root := t.TempDir()
	inDir := filepath.Join(root, "input")
	outDir := filepath.Join(root, "output")
	if err := os.MkdirAll(inDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envInputDir, inDir)
	t.Setenv(envOutputDir, outDir)
	t.Setenv(envPrefix, defaultPrefix)
	t.Setenv(envSuffix, defaultSuffix)
	t.Setenv(envEmptyValue, defaultEmptyValue)
	return inDir, outDir
}

// writeFile creates path (and parents) with the given content.
func writeFile(t *testing.T, path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, toBytes(content), 0666); err != nil {
		t.Fatal(err)
	}
}

// writeBytes creates path (and parents) with the given bytes.
func writeBytes(t *testing.T, path string, content []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0666); err != nil {
		t.Fatal(err)
	}
}

// readText returns the contents of path as a string.
func readText(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runHelper loads the configuration from the environment and runs a
// single substitution pass.
func runHelper(t *testing.T) error {
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return run(cfg)
}

// setUnset puts key in the unset state, restoring the previous value
// when the test ends.
func setUnset(t *testing.T, key string) {
	prev, ok := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if ok {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

// inode returns the inode number of path, to check whether a directory
// entry was reused in place or replaced by a different one.
func inode(t *testing.T, path string) uint64 {
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat info for %s is not a *syscall.Stat_t", path)
	}
	return st.Ino
}

// listFiles returns the (unsorted) names of the entries of dir.
func listFiles(t *testing.T, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestBasicReplacement(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	server := freshVar("SERVER")
	port := freshVar("PORT")
	t.Setenv(server, "myhost1")
	t.Setenv(port, "8080")

	writeFile(t, filepath.Join(inDir, "app.conf"),
		"server = <"+server+">\nport = <"+port+">\n")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	want := "server = myhost1\nport = 8080\n"
	if got := readText(t, filepath.Join(outDir, "app.conf")); got != want {
		t.Errorf("replaced content = %q, want %q", got, want)
	}
}

func TestMultipleTokensInOneFile(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	a := freshVar("A")
	b := freshVar("B")
	t.Setenv(a, "x")
	t.Setenv(b, "y")

	writeFile(t, filepath.Join(inDir, "multi.txt"), "<"+a+">:<"+b+">:<"+a+">:<"+b+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "multi.txt")); got != "x:y:x:y" {
		t.Errorf("multi-token content = %q, want %q", got, "x:y:x:y")
	}
}

func TestRepeatedToken(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	r := freshVar("R")
	t.Setenv(r, "value")

	writeFile(t, filepath.Join(inDir, "repeat.txt"),
		"<"+r+"> and <"+r+"> and <"+r+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "repeat.txt")); got != "value and value and value" {
		t.Errorf("repeated-token content = %q", got)
	}
}

func TestCustomPrefixSuffix(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	v := freshVar("V")
	t.Setenv(v, "abc")
	t.Setenv(envPrefix, "{{")
	t.Setenv(envSuffix, "}}")

	writeFile(t, filepath.Join(inDir, "custom.txt"), "x = {{"+v+"}}y")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "custom.txt")); got != "x = abcy" {
		t.Errorf("custom-delimiter content = %q, want %q", got, "x = abcy")
	}
}

func TestMissingVarErrors(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	missing := freshVar("MISSING")
	setUnset(t, missing)

	inFile := filepath.Join(inDir, "missing.txt")
	writeFile(t, inFile, "<"+missing+">")

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error for missing variable")
	}
	msg := err.Error()
	if !strings.Contains(msg, missing) {
		t.Errorf("error %q does not identify variable %q", msg, missing)
	}
	if !strings.Contains(msg, "not set") {
		t.Errorf("error %q does not report the variable as unset", msg)
	}
	if !strings.Contains(msg, outDir) && !strings.Contains(msg, inDir) {
		t.Errorf("error %q does not identify a file path", msg)
	}
}

func TestEmptyVarErrors(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, _ := freshInputDir(t)

	e := freshVar("EMPTY")
	t.Setenv(e, "")

	writeFile(t, filepath.Join(inDir, "empty.txt"), "<"+e+">")

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error for empty variable")
	}
	msg := err.Error()
	if !strings.Contains(msg, e) {
		t.Errorf("error %q does not identify variable %q", msg, e)
	}
	if !strings.Contains(msg, "empty") {
		t.Errorf("error %q does not mention the empty value", msg)
	}
}

func TestEmptySentinel(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	e := freshVar("OPTIONAL")
	t.Setenv(e, "(empty)")

	writeFile(t, filepath.Join(inDir, "sentinel.txt"), "a<"+e+">b")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "sentinel.txt")); got != "ab" {
		t.Errorf("sentinel replacement = %q, want %q", got, "ab")
	}
}

func TestCustomSentinel(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	opt := freshVar("OPTIONAL")
	lit := freshVar("LITERAL")
	t.Setenv(opt, "__EMPTY__")
	t.Setenv(lit, "(empty)")
	t.Setenv(envEmptyValue, "__EMPTY__")

	writeFile(t, filepath.Join(inDir, "custom-sentinel.txt"),
		"<"+opt+">|<"+lit+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "custom-sentinel.txt")); got != "|(empty)" {
		t.Errorf("custom sentinel replacement = %q, want %q", got, "|(empty)")
	}
}

func TestFilesWithoutTokensCopiedUnchanged(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	writeFile(t, filepath.Join(inDir, "plain.txt"), "no tokens here\njust text\n")
	writeFile(t, filepath.Join(inDir, "unicode.txt"), "héllo wörld <lgt; & < nope\n")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "plain.txt")); got != "no tokens here\njust text\n" {
		t.Errorf("plain copy = %q", got)
	}
	if got := readText(t, filepath.Join(outDir, "unicode.txt")); got != "héllo wörld <lgt; & < nope\n" {
		t.Errorf("unicode copy = %q", got)
	}
}

func TestNestedDirectories(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	deep := freshVar("DEEP")
	t.Setenv(deep, "nested")
	longPath := filepath.Join(inDir, "a", "b", "c", "d.txt")
	writeFile(t, longPath, "deep = <"+deep+">")
	writeFile(t, filepath.Join(inDir, "a", "top.txt"), "top")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "a", "b", "c", "d.txt")); got != "deep = nested" {
		t.Errorf("nested file = %q", got)
	}
	if got := readText(t, filepath.Join(outDir, "a", "top.txt")); got != "top" {
		t.Errorf("top-level file = %q", got)
	}
}

func TestBinaryFileCopiedByteForByte(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	// NUL byte anywhere in the file: treated as binary.
	content := []byte("head\x00mid<FAKE>tail\x01")
	writeBytes(t, filepath.Join(inDir, "bin.dat"), content)

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "bin.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("binary file was altered")
	}
}

func TestInvalidUtf8CopiedByteForByte(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	// No NUL byte, but invalid UTF-8: treated as binary.
	content := []byte("abc\xff\xfe<FAKE>xyz")
	writeBytes(t, filepath.Join(inDir, "latin1.txt"), content)

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "latin1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("invalid UTF-8 file was altered")
	}
}

func TestBinaryNulPastProbeLength(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	// A NUL byte after the first 8192 bytes must still mark the file as
	// binary: binary detection scans the whole file, not a leading chunk.
	content := bytes.Repeat([]byte("a"), 8192)
	content = append(content, byte(0))
	content = append(content, []byte("tail<FAKE>")...)
	writeBytes(t, filepath.Join(inDir, "late-nul.dat"), content)

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "late-nul.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("file with NUL after 8192 bytes was altered")
	}
}

func TestEmptyPrefixRejected(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	freshInputDir(t)
	t.Setenv(envPrefix, "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig succeeded, want error for empty token prefix")
	}
	if !strings.Contains(err.Error(), envPrefix) {
		t.Errorf("error %q does not identify %s", err.Error(), envPrefix)
	}
}

func TestEmptySuffixRejected(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	freshInputDir(t)
	t.Setenv(envSuffix, "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig succeeded, want error for empty token suffix")
	}
	if !strings.Contains(err.Error(), envSuffix) {
		t.Errorf("error %q does not identify %s", err.Error(), envSuffix)
	}
}

func TestInputFilesUnmodified(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, _ := freshInputDir(t)

	v := freshVar("U")
	t.Setenv(v, "mutated?")
	inFile := filepath.Join(inDir, "template.conf")
	writeFile(t, inFile, "v=<"+v+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, inFile); got != "v=<"+v+">" {
		t.Errorf("input file was modified: %q", got)
	}
}

func TestStaleOutputRemoved(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	v := freshVar("STALE")
	t.Setenv(v, "1")
	writeFile(t, filepath.Join(inDir, "keep.txt"), "<"+v+">")
	writeFile(t, filepath.Join(inDir, "old.txt"), "<"+v+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	// Remove old.txt from the input and plant a stale output file.
	if err := os.Remove(filepath.Join(inDir, "old.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outDir, "stale.txt"), "stale")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	names := listFiles(t, outDir)
	if len(names) != 1 || names[0] != "keep.txt" {
		t.Errorf("output after sync = %v, want [keep.txt]", names)
	}
	if _, err := os.Stat(filepath.Join(outDir, "stale.txt")); err == nil {
		t.Errorf("stale output file was not removed")
	}
}

func TestFailedRunLeavesOutputIntact(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	good := freshVar("GOOD")
	bad := freshVar("BAD")
	t.Setenv(good, "precious")

	writeFile(t, filepath.Join(inDir, "data.txt"), "<"+good+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	before := readText(t, filepath.Join(outDir, "data.txt"))
	if before != "precious" {
		t.Fatal("setup failed")
	}

	// Now break the input and try again.
	writeFile(t, filepath.Join(inDir, "broken.txt"), "<"+bad+">")

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want failure")
	}

	if got := readText(t, filepath.Join(outDir, "data.txt")); got != before {
		t.Errorf("output changed after a failed run: %q", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "broken.txt")); err == nil {
		t.Errorf("output contains partial result of a failed run")
	}

	// No temporary directory may linger next to the output.
	for _, name := range listFiles(t, filepath.Dir(outDir)) {
		if strings.HasPrefix(name, ".docker-env-replace-tmp-") {
			t.Errorf("temporary directory left behind: %s", name)
		}
	}
}

func TestEmptyInputClearsOutput(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	v := freshVar("CLEAR")
	t.Setenv(v, "x")
	writeFile(t, filepath.Join(inDir, "gone.txt"), "<"+v+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	if len(listFiles(t, outDir)) != 1 {
		t.Fatal("setup failed")
	}

	// Empty the input directory: the output must be mirrored to empty.
	for _, name := range listFiles(t, inDir) {
		if err := os.Remove(filepath.Join(inDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	if names := listFiles(t, outDir); len(names) != 0 {
		t.Errorf("output not empty after empty input: %v", names)
	}
}

func TestSymlinkCopiedAsFile(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	v := freshVar("LINKVAR")
	t.Setenv(v, "linked")
	writeFile(t, filepath.Join(inDir, "target.txt"), "value=<"+v+">")
	if err := os.Symlink("target.txt", filepath.Join(inDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	outLink := filepath.Join(outDir, "link.txt")
	if got := readText(t, outLink); got != "value=linked" {
		t.Errorf("symlink content = %q", got)
	}
	if fi, err := os.Lstat(outLink); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
		t.Errorf("output link is still a symlink")
	}
}

func TestSymlinkOutsideInputRejected(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, _ := freshInputDir(t)

	// A symlink whose target resolves outside the input directory must
	// fail the run rather than leak the target's content.
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	writeFile(t, secret, "top secret")
	if err := os.Symlink(secret, filepath.Join(inDir, "leak.txt")); err != nil {
		t.Fatal(err)
	}

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error for symlink escaping the input directory")
	}
	if !strings.Contains(err.Error(), "outside the input directory") {
		t.Errorf("error %q does not report the symlink escaping the input", err.Error())
	}
}

func TestNestedOutputRejected(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	root := t.TempDir()
	inDir := filepath.Join(root, "input")
	outDir := filepath.Join(inDir, "nested", "output")
	t.Setenv(envInputDir, inDir)
	t.Setenv(envOutputDir, outDir)
	t.Setenv(envPrefix, defaultPrefix)
	t.Setenv(envSuffix, defaultSuffix)
	t.Setenv(envEmptyValue, defaultEmptyValue)

	if err := os.MkdirAll(inDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error for output nested inside input")
	}
	if !strings.Contains(err.Error(), "inside the input directory") {
		t.Errorf("error %q does not report the nested output", err.Error())
	}
}

func TestInputNestedInOutputRejected(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	root := t.TempDir()
	// The input sits inside the output directory: swapping the output
	// would delete the input, so this must be rejected too.
	inDir := filepath.Join(root, "output", "input")
	outDir := filepath.Join(root, "output")
	t.Setenv(envInputDir, inDir)
	t.Setenv(envOutputDir, outDir)
	t.Setenv(envPrefix, defaultPrefix)
	t.Setenv(envSuffix, defaultSuffix)
	t.Setenv(envEmptyValue, defaultEmptyValue)

	if err := os.MkdirAll(inDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error for input nested inside output")
	}
	if !strings.Contains(err.Error(), "inside the output directory") {
		t.Errorf("error %q does not report the overlapping output", err.Error())
	}
}

func TestPermissionsPreserved(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	priv := filepath.Join(inDir, "priv.sh")
	writeFile(t, priv, "#!/bin/sh\necho ok\n")
	if err := os.Chmod(priv, 0750); err != nil {
		t.Fatal(err)
	}

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(outDir, "priv.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0750 {
		t.Errorf("output permission = %#o, want 0750", perm)
	}
}

func TestTokenMayReferenceConfigPrefixedVar(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	// A variable whose name starts with DOCKER_ENV_REPLACE_ is an ordinary
	// variable: tokens referring to it are resolved normally.
	t.Setenv("DOCKER_ENV_REPLACE_CUSTOM_FLAVOR", "vanilla")
	writeFile(t, filepath.Join(inDir, "flavor.txt"), "flavor=<DOCKER_ENV_REPLACE_CUSTOM_FLAVOR>")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "flavor.txt")); got != "flavor=vanilla" {
		t.Errorf("DOCKER_ENV_REPLACE_ variable replacement = %q", got)
	}
}

func TestEmptyFileCopied(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	writeFile(t, filepath.Join(inDir, "empty.txt"), "")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "empty.txt")); got != "" {
		t.Errorf("empty file = %q", got)
	}
}

func TestInputDirMissing(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	root := t.TempDir()
	t.Setenv(envInputDir, filepath.Join(root, "no-such-dir"))
	t.Setenv(envOutputDir, filepath.Join(root, "out"))
	t.Setenv(envPrefix, defaultPrefix)
	t.Setenv(envSuffix, defaultSuffix)
	t.Setenv(envEmptyValue, defaultEmptyValue)

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error for missing input directory")
	}
	if !strings.Contains(err.Error(), "input directory") {
		t.Errorf("error message = %q, want it to mention the input directory", err.Error())
	}
}

func TestUnclosedDelimiterLeftAlone(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	// A "<" that is never closed is not a token and is left alone.
	writeFile(t, filepath.Join(inDir, "math.txt"), "a < b and c\n")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}

	if got := readText(t, filepath.Join(outDir, "math.txt")); got != "a < b and c\n" {
		t.Errorf("unclosed delimiter content = %q", got)
	}
}

func TestExistingOutputDirNotReplaced(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	v := freshVar("STABLE")
	t.Setenv(v, "one")
	writeFile(t, filepath.Join(inDir, "a.txt"), "<"+v+">")

	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	firstIno := inode(t, outDir)

	// A pre-existing output directory is very often a mount point (a
	// Docker volume mounted directly at the output path in a real
	// container). It must never be removed or renamed over on a later
	// run -- only its contents may change -- or the swap fails with
	// "device or resource busy" against a real mount.
	t.Setenv(v, "two")
	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	secondIno := inode(t, outDir)
	if firstIno != secondIno {
		t.Errorf("output directory was replaced (inode %d -> %d); it must be reused in place", firstIno, secondIno)
	}
	if got := readText(t, filepath.Join(outDir, "a.txt")); got != "two" {
		t.Errorf("output content = %q, want %q", got, "two")
	}
}

func TestLogUsesFinalOutputPaths(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	inDir, outDir := freshInputDir(t)

	v := freshVar("LOG")
	t.Setenv(v, "x")
	writeFile(t, filepath.Join(inDir, "sub", "nested.conf"), "v=<"+v+">")
	writeFile(t, filepath.Join(inDir, "target.txt"), "plain")
	if err := os.Symlink("target.txt", filepath.Join(inDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	// Files are written to a temporary directory that is renamed or
	// swapped into place at the end of the run. A log line exposing
	// that temporary path would name a location that no longer exists
	// once the run finishes, so the displayed path must always be the
	// final destination under the output directory.
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	check := func(run string) {
		output := buf.String()
		if strings.Contains(output, ".docker-env-replace-tmp-") {
			t.Errorf("%s: log output exposes the temporary directory:\n%s", run, output)
		}
		want := filepath.Join(outDir, "sub", "nested.conf")
		if !strings.Contains(output, " -> "+want) {
			t.Errorf("%s: log output %q does not show the final path %q", run, output, want)
		}
		wantLink := filepath.Join(outDir, "link.txt")
		if !strings.Contains(output, " -> "+wantLink) {
			t.Errorf("%s: log output %q does not show the final symlink path %q", run, output, wantLink)
		}
		buf.Reset()
	}

	// First run: the output directory does not exist yet, so the
	// temporary directory is created as its sibling and renamed over.
	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	check("sibling swap")

	// Second run: the output directory now exists, so the temporary
	// directory is created inside it and its entries are swapped in.
	if err := runHelper(t); err != nil {
		t.Fatal(err)
	}
	check("in-place swap")
}

func TestWriteErrorNamesFinalOutputPath(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	root := t.TempDir()

	cfg := config{prefix: defaultPrefix, suffix: defaultSuffix, emptyValue: defaultEmptyValue}
	inFile := filepath.Join(root, "in.txt")
	writeFile(t, inFile, "hello")

	// Writing the output file legitimately happens on a path inside a
	// temporary directory that is swapped away at the end of the run. To
	// exercise the error path deterministically on every platform and
	// regardless of whether the tests run as root, point the output path
	// at a directory that does not exist: os.Create then always fails.
	tmpName := filepath.Join(root, ".docker-env-replace-tmp-missing")
	outPath := filepath.Join(tmpName, "nested", "out.txt")
	displayPath := filepath.Join(root, "output", "nested", "out.txt")

	err := processFile(cfg, inFile, outPath, displayPath, 0644)
	if err == nil {
		t.Fatal("processFile succeeded, want a write error")
	}
	msg := err.Error()
	if strings.Contains(msg, ".docker-env-replace-tmp-") {
		t.Errorf("write error exposes the temporary directory: %q", msg)
	}
	if !strings.Contains(msg, displayPath) {
		t.Errorf("write error %q does not name the final output path %q", msg, displayPath)
	}
}

func TestMkdirErrorNamesFinalOutputPath(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	root := t.TempDir()

	cfg := config{prefix: defaultPrefix, suffix: defaultSuffix, emptyValue: defaultEmptyValue}
	srcRoot := filepath.Join(root, "input")
	if err := os.MkdirAll(filepath.Join(srcRoot, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(srcRoot, "sub", "a.txt"), "x")

	// A regular file at the path where the subdirectory must be created
	// makes os.Mkdir fail deterministically, on every platform and
	// regardless of whether the tests run as root.
	dstRoot := filepath.Join(root, ".docker-env-replace-tmp-collide")
	if err := os.MkdirAll(dstRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dstRoot, "sub"), "not a directory")

	finalRoot := filepath.Join(root, "output")

	err := processTree(cfg, srcRoot, dstRoot, finalRoot)
	if err == nil {
		t.Fatal("processTree succeeded, want a mkdir error")
	}
	msg := err.Error()
	if strings.Contains(msg, ".docker-env-replace-tmp-") {
		t.Errorf("mkdir error exposes the temporary directory: %q", msg)
	}
	want := filepath.Join(finalRoot, "sub")
	if !strings.Contains(msg, want) {
		t.Errorf("mkdir error %q does not name the final output path %q", msg, want)
	}
}

func TestOutputParentNotWritable(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}

	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	inDir := filepath.Join(root, "input")
	// The output directory itself is writable, but its parent is not:
	// this mirrors a container where the output directory is a writable
	// mounted volume but its parent (e.g. "/") is read-only.
	outDir := filepath.Join(parent, "output")
	if err := os.MkdirAll(inDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0755) })

	t.Setenv(envInputDir, inDir)
	t.Setenv(envOutputDir, outDir)
	t.Setenv(envPrefix, defaultPrefix)
	t.Setenv(envSuffix, defaultSuffix)
	t.Setenv(envEmptyValue, defaultEmptyValue)

	v := freshVar("INPLACE")
	t.Setenv(v, "swapped")
	writeFile(t, filepath.Join(inDir, "a.txt"), "<"+v+">")
	// A stale file that must be removed by the swap.
	writeFile(t, filepath.Join(outDir, "stale.txt"), "stale")

	if err := runHelper(t); err != nil {
		t.Fatalf("run failed with an unwritable output parent: %v", err)
	}

	if got := readText(t, filepath.Join(outDir, "a.txt")); got != "swapped" {
		t.Errorf("output content = %q, want %q", got, "swapped")
	}
	if _, err := os.Stat(filepath.Join(outDir, "stale.txt")); err == nil {
		t.Errorf("stale output file was not removed")
	}
	for _, name := range listFiles(t, outDir) {
		if strings.HasPrefix(name, ".docker-env-replace-tmp-") {
			t.Errorf("temporary directory left behind: %s", name)
		}
	}
}

func TestOutputParentNotWritableAndOutputMissing(t *testing.T) {
	envMu.Lock()
	defer envMu.Unlock()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}

	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	inDir := filepath.Join(root, "input")
	// The output directory does not exist yet, so there is nothing to
	// build the swap inside: this must still fail, unlike the case
	// where the output directory already exists.
	outDir := filepath.Join(parent, "output")
	if err := os.MkdirAll(inDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0755) })

	t.Setenv(envInputDir, inDir)
	t.Setenv(envOutputDir, outDir)
	t.Setenv(envPrefix, defaultPrefix)
	t.Setenv(envSuffix, defaultSuffix)
	t.Setenv(envEmptyValue, defaultEmptyValue)

	writeFile(t, filepath.Join(inDir, "a.txt"), "x")

	err := runHelper(t)
	if err == nil {
		t.Fatal("run succeeded, want error: output missing and its parent is not writable")
	}
	if !strings.Contains(err.Error(), "cannot create temporary directory") {
		t.Errorf("error %q does not mention the temporary directory", err.Error())
	}
}
