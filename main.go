// Copyright 2026 The envreplace authors. All rights reserved.
// Use of this source code is governed by a BSD-style license.

// Package main provides envreplace, a minimal utility that performs
// environment-variable token replacement in files. It is intended for
// use as a Docker Compose init container: it reads a tree of template
// files from an input directory and writes a fully substituted copy to
// an output directory.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// config holds the settings for a single run, all read from the
// environment. Every utility configuration variable uses the
// ENVREPLACE_ prefix; any other environment variable may be referenced
// by tokens in the input files.
type config struct {
	inputDir   string
	outputDir  string
	prefix     string
	suffix     string
	emptyValue string
}

// The names of the environment variables that configure the utility.
const (
	envInputDir   = "ENVREPLACE_INPUT_DIR"
	envOutputDir  = "ENVREPLACE_OUTPUT_DIR"
	envPrefix     = "ENVREPLACE_TOKEN_PREFIX"
	envSuffix     = "ENVREPLACE_TOKEN_SUFFIX"
	envEmptyValue = "ENVREPLACE_EMPTY_VALUE"

	defaultInputDir   = "/input"
	defaultOutputDir  = "/output"
	defaultPrefix     = "<"
	defaultSuffix     = ">"
	defaultEmptyValue = "(empty)"
)

// strOrDefault returns the value of the environment variable key, or
// fallback when the variable is unset or set to the empty string.
func strOrDefault(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if ok && value != "" {
		return value
	}
	return fallback
}

// loadConfig reads the utility's configuration from the environment.
func loadConfig() (config, error) {
	if v, ok := os.LookupEnv(envPrefix); ok && v == "" {
		return config{}, errors.New("ENVREPLACE_TOKEN_PREFIX cannot be set to the empty string")
	}
	if v, ok := os.LookupEnv(envSuffix); ok && v == "" {
		return config{}, errors.New("ENVREPLACE_TOKEN_SUFFIX cannot be set to the empty string")
	}
	prefix := strOrDefault(envPrefix, defaultPrefix)
	suffix := strOrDefault(envSuffix, defaultSuffix)
	return config{
		inputDir:   strOrDefault(envInputDir, defaultInputDir),
		outputDir:  strOrDefault(envOutputDir, defaultOutputDir),
		prefix:     prefix,
		suffix:     suffix,
		emptyValue: strOrDefault(envEmptyValue, defaultEmptyValue),
	}, nil
}

// sameDir reports whether two paths refer to the same directory.
func sameDir(a, b string) bool {
	aa, aerr := filepath.Abs(a)
	bb, berr := filepath.Abs(b)
	if aerr != nil || berr != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// pathWithin reports whether child is inside parent (or equal to it).
func pathWithin(parent, child string) bool {
	ap, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	ac, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(ap), filepath.Clean(ac))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// run performs a single substitution pass, mirroring cfg.inputDir to
// cfg.outputDir. On failure it leaves any existing output untouched and
// returns a non-nil error.
func run(cfg config) error {
	inputDir := filepath.Clean(cfg.inputDir)
	outputDir := filepath.Clean(cfg.outputDir)

	if sameDir(inputDir, outputDir) {
		return errors.New("input and output directories are the same: " + inputDir)
	}
	// The output tree must not overlap the input tree. An output nested
	// inside the input would be walked and mirrored onto itself; an input
	// nested inside the output would be deleted when the output is removed.
	if pathWithin(inputDir, outputDir) {
		return errors.New("output directory " + outputDir +
			" is inside the input directory " + inputDir)
	}
	if pathWithin(outputDir, inputDir) {
		return errors.New("input directory " + inputDir +
			" is inside the output directory " + outputDir)
	}

	fi, err := os.Stat(inputDir)
	if err != nil {
		return errors.New("cannot access input directory: " + err.Error())
	}
	if !fi.IsDir() {
		return errors.New("input path " + inputDir + " is not a directory")
	}

	parent := filepath.Dir(outputDir)
	if parent == "" {
		parent = "."
	}
	if pfi, err := os.Stat(parent); err != nil || !pfi.IsDir() {
		return errors.New("output directory's parent is not a directory: " + parent)
	}

	// Build the complete result in a temporary directory that is a
	// sibling of the output directory, then swap it into place.
	tmp, err := os.MkdirTemp(parent, ".envreplace-tmp-")
	if err != nil {
		return errors.New("cannot create temporary directory in " + parent + ": " + err.Error())
	}

	err = processTree(cfg, inputDir, tmp)
	if err == nil {
		// Mirror the permissions of the input root directory.
		err = os.Chmod(tmp, fi.Mode().Perm())
	}
	if err == nil {
		// Output is replaced only after every file has been written.
		// RemoveAll also removes a plain file sitting at outputDir.
		if _, err := os.Stat(outputDir); err == nil {
			if derr := os.RemoveAll(outputDir); derr != nil {
				err = derr
			}
		}
	}
	if err == nil {
		err = os.Rename(tmp, outputDir)
	}
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

// processTree mirrors srcRoot into dstRoot, substituting tokens in
// regular text files. It returns the first error encountered. The
// source tree is never modified; it is only read.
func processTree(cfg config, srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcRoot {
			return nil
		}
		// Defense in depth: never mirror an envreplace temporary
		// directory that somehow appears inside the input tree.
		if strings.HasPrefix(d.Name(), ".envreplace-tmp-") {
			log.Printf("  Skipped envreplace temporary entry: %s", path)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		outPath := filepath.Join(dstRoot, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			// Create explicitly with the input's permission bits; the
			// mode argument of os.Mkdir is masked by the process umask.
			if err := os.Mkdir(outPath, mode.Perm()); err != nil {
				return err
			}
			return os.Chmod(outPath, mode.Perm())
		case mode&fs.ModeSymlink != 0:
			return processSymlink(cfg, path, outPath)
		case !mode.IsRegular():
			// FIFOs, sockets and devices cannot be mirrored
			// meaningfully; skip them rather than fail.
			log.Printf("  Skipped special file: %s", path)
			return nil
		default:
			log.Printf("Processing: %s -> %s", path, outPath)
			return processFile(cfg, path, outPath, mode.Perm())
		}
	})
}

// processSymlink resolves a symbolic link and copies the content of its
// target as a regular file at outPath. A symlink to a directory or to a
// special file is skipped (the tree is not followed); this is the
// documented, usefully-simple behavior. A symlink whose target resolves
// outside the input directory is a hard error: mirroring it would copy
// files the input did not ask to be published.
func processSymlink(cfg config, linkPath, outPath string) error {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	inputResolved, err := filepath.EvalSymlinks(cfg.inputDir)
	if err != nil {
		return err
	}
	if !pathWithin(inputResolved, resolved) {
		return fmt.Errorf("symlink %s resolves outside the input directory: %s -> %s",
			linkPath, target, resolved)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	mode := fi.Mode()
	if mode.IsDir() {
		log.Printf("  Skipped symlink to a directory: %s -> %s", linkPath, target)
		return nil
	}
	if !mode.IsRegular() {
		log.Printf("  Skipped symlink to a special file: %s -> %s", linkPath, target)
		return nil
	}
	log.Printf("Processing: %s -> %s (symlink to %s)", linkPath, outPath, resolved)
	return processFile(cfg, resolved, outPath, mode.Perm())
}

// processFile reads inPath, substitutes tokens, and writes the result
// to outPath. Permissions from perm are applied to the written file.
func processFile(cfg config, inPath, outPath string, perm fs.FileMode) error {
	data, err := readWholeFile(inPath)
	if err != nil {
		return err
	}
	out, names, binary, err := replaceBytes(cfg, inPath, data)
	if err != nil {
		return err
	}
	w, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer w.Close()
	if _, werr := w.Write(out); werr != nil {
		return werr
	}
	if err := os.Chmod(outPath, perm); err != nil {
		return err
	}
	if binary {
		log.Println("  Copied as binary (no replacements)")
	} else if len(names) == 0 {
		log.Println("  No replacements")
	} else {
		log.Printf("  Replaced: %s", summarizeNames(names))
	}
	return nil
}

// readWholeFile reads the entire contents of the named file.
func readWholeFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// isBinary reports whether data should be copied without substitution:
// either it contains a NUL byte anywhere, or it is not valid UTF-8.
// Scanning the whole file (not just a leading chunk) keeps generated
// YAML/JSON safe: a NUL appended at the end of an otherwise text file
// still marks it binary.
func isBinary(data []byte) bool {
	if bytes.IndexByte(data, byte(0)) != -1 {
		return true
	}
	return !utf8.Valid(data)
}

// toBytes returns the bytes encoding of the string s.
func toBytes(s string) []byte {
	return []byte(s)
}

// tokenError reports a token that could not be substituted.
type tokenError struct {
	path    string // input file containing the token
	name    string // name of the referenced variable
	missing bool   // true when unset, false when present but empty
}

func (e *tokenError) Error() string {
	problem := "has an empty value"
	if e.missing {
		problem = "is not set"
	}
	return "environment variable " + e.name + " " + problem +
		" (token \"" + e.name + "\") in " + e.path
}

// replaceBytes substitutes token occurrences in the text data with the
// values of the corresponding environment variables. The substitution
// operates on the raw bytes of the file: line endings and all other
// content are preserved exactly. It returns the substituted content,
// the names of the replaced variables in order of first appearance,
// and whether the data was treated as binary (in which case it is
// returned unchanged with no substitution).
func replaceBytes(cfg config, inPath string, data []byte) (out []byte, names []string, binary bool, err error) {
	if isBinary(data) {
		return data, nil, true, nil
	}
	prefix := toBytes(cfg.prefix)
	suffix := toBytes(cfg.suffix)
	var buf []byte
	var replaced []string
	i := 0
	for i < len(data) {
		j := bytes.Index(data[i:], prefix)
		if j == -1 {
			buf = append(buf, data[i:]...)
			i = len(data)
			break
		}
		nameStart := i + j + len(prefix)
		k := bytes.Index(data[nameStart:], suffix)
		if k == -1 {
			// Unclosed token: copy the rest of the file literally.
			buf = append(buf, data[i:]...)
			i = len(data)
			break
		}
		name := string(data[nameStart : nameStart+k])
		if name == "" {
			// Empty token names are not variables; copy the
			// delimiters literally and keep scanning.
			buf = append(buf, data[i:nameStart]...)
			i = nameStart
			continue
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return nil, nil, false, &tokenError{path: inPath, name: name, missing: true}
		}
		if value == "" && value != cfg.emptyValue {
			return nil, nil, false, &tokenError{path: inPath, name: name, missing: false}
		}
		repl := value
		if value == cfg.emptyValue {
			repl = ""
		}
		buf = append(buf, data[i:nameStart-len(prefix)]...)
		buf = append(buf, toBytes(repl)...)
		replaced = append(replaced, name)
		i = nameStart + k + len(suffix)
	}
	if buf == nil {
		buf = make([]byte, 0)
	}
	return buf, replaced, false, nil
}

// summarizeNames returns the unique names in sorted order, joined by
// ", ", for use in log messages. Values are never logged.
func summarizeNames(names []string) string {
	// Copy before sorting: the slice of the caller's array must not be
	// reordered.
	sorted := append([]string(nil), names...)
	if sorted == nil {
		return ""
	}
	sort.Strings(sorted)
	var b strings.Builder
	first := true
	prev := ""
	for i := 0; i < len(sorted); i++ {
		name := sorted[i]
		if i > 0 && name == prev {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		b.WriteString(name)
		prev = name
		first = false
	}
	return b.String()
}

// main is the entry point of the envreplace executable. Processing logs
// go to stdout; the fatal error is printed to stderr.
func main() {
	log.SetOutput(os.Stdout)
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envreplace: "+err.Error())
		os.Exit(1)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "envreplace: error: "+err.Error())
		os.Exit(1)
	}
}
