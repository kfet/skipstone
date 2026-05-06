// Package awsini parses the AWS shared config / credentials file format.
//
// AWS ini has two quirks vs standard INI:
//   - The credentials file uses bare `[name]` section headers.
//   - The config file uses `[profile name]` (and `[default]`, `[sso-session x]`).
//
// Both quirks are normalised here: callers look up by profile name only.
package awsini

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
)

// File is a parsed ini document: section name -> key -> value.
type File map[string]map[string]string

// ParseConfig parses an AWS config file. `[profile foo]` headers become
// section "foo"; `[default]` stays "default"; other prefixed sections
// (e.g. `[sso-session x]`) are stored as "sso-session x".
func ParseConfig(r io.Reader) (File, error) {
	return parse(r, true)
}

// ParseCredentials parses an AWS credentials file. Section headers are bare
// profile names; `[default]` is the default profile.
func ParseCredentials(r io.Reader) (File, error) {
	return parse(r, false)
}

// LoadConfig reads and parses the config file at path. Returns an empty File
// if the path does not exist.
func LoadConfig(path string) (File, error) {
	return loadFile(path, ParseConfig)
}

// LoadCredentials reads and parses the credentials file at path. Returns an
// empty File if the path does not exist.
func LoadCredentials(path string) (File, error) {
	return loadFile(path, ParseCredentials)
}

func loadFile(path string, fn func(io.Reader) (File, error)) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, nil
		}
		return nil, err
	}
	defer f.Close()
	return fn(f)
}

func parse(r io.Reader, isConfig bool) (File, error) {
	out := File{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	var current string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, errors.New("awsini: unterminated section header: " + line)
			}
			name := strings.TrimSpace(line[1:end])
			if isConfig {
				if strings.HasPrefix(name, "profile ") {
					name = strings.TrimSpace(name[len("profile "):])
				}
			}
			if name == "" {
				return nil, errors.New("awsini: empty section name")
			}
			current = name
			if _, ok := out[current]; !ok {
				out[current] = map[string]string{}
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, errors.New("awsini: malformed line (no '='): " + line)
		}
		if current == "" {
			return nil, errors.New("awsini: key=value before any section: " + line)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			return nil, errors.New("awsini: empty key: " + line)
		}
		out[current][key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns the value of key in the named profile, or "" if absent.
func (f File) Get(profile, key string) string {
	if f == nil {
		return ""
	}
	if p, ok := f[profile]; ok {
		return p[key]
	}
	return ""
}
