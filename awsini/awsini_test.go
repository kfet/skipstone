package awsini

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCredentials(t *testing.T) {
	in := `
# comment
; another comment

[default]
aws_access_key_id = AKIA_DEFAULT
aws_secret_access_key = secretdefault

[work]
aws_access_key_id = AKIA_WORK
aws_secret_access_key = secretwork
aws_session_token = tok
`
	f, err := ParseCredentials(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Get("default", "aws_access_key_id"); got != "AKIA_DEFAULT" {
		t.Errorf("default key: %q", got)
	}
	if got := f.Get("work", "aws_session_token"); got != "tok" {
		t.Errorf("work token: %q", got)
	}
	if got := f.Get("missing", "x"); got != "" {
		t.Errorf("missing profile should be empty, got %q", got)
	}
}

func TestParseConfigStripsProfilePrefix(t *testing.T) {
	in := `
[default]
region = us-east-1

[profile work]
region = us-west-2

[sso-session corp]
sso_start_url = https://x
`
	f, err := ParseConfig(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if f.Get("default", "region") != "us-east-1" {
		t.Error("default region")
	}
	if f.Get("work", "region") != "us-west-2" {
		t.Error("work region")
	}
	if f.Get("sso-session corp", "sso_start_url") != "https://x" {
		t.Error("sso-session preserved")
	}
}

func TestNilFileGet(t *testing.T) {
	var f File
	if f.Get("a", "b") != "" {
		t.Error("nil file should return empty")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, in string }{
		{"unterminated", "[oops\nkey=val\n"},
		{"empty-section", "[]\nkey=val\n"},
		{"no-equals", "[s]\nkeyval\n"},
		{"empty-key", "[s]\n=val\n"},
		{"key-before-section", "key=val\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseCredentials(strings.NewReader(c.in)); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	f, err := LoadCredentials(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 0 {
		t.Error("expected empty file")
	}
	f2, err := LoadConfig(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f2) != 0 {
		t.Error("expected empty file")
	}
}

func TestLoadReal(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials")
	if err := writeFile(credPath, "[default]\naws_access_key_id=A\naws_secret_access_key=B\n"); err != nil {
		t.Fatal(err)
	}
	f, err := LoadCredentials(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if f.Get("default", "aws_access_key_id") != "A" {
		t.Error("load credentials")
	}

	cfgPath := filepath.Join(dir, "config")
	if err := writeFile(cfgPath, "[profile foo]\nregion=eu-west-1\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Get("foo", "region") != "eu-west-1" {
		t.Error("load config")
	}
}

func TestLoadReadError(t *testing.T) {
	// Reading a directory as a file: os.Open succeeds, scanner errors.
	dir := t.TempDir()
	if _, err := LoadCredentials(dir); err == nil {
		t.Error("expected error reading directory as file")
	}
	if _, err := LoadConfig(dir); err == nil {
		t.Error("expected error reading directory as file")
	}
}

func TestLoadOpenError(t *testing.T) {
	// Permission denied on a file -> os.Open returns an error that is not ErrNotExist.
	dir := t.TempDir()
	path := dir + "/locked"
	if err := writeFile(path, "[default]\nx=y\n"); err != nil {
		t.Fatal(err)
	}
	if err := chmod(path, 0); err != nil {
		t.Skip("chmod not effective on this fs")
	}
	defer chmod(path, 0o600)
	if _, err := LoadCredentials(path); err == nil {
		t.Error("expected permission error")
	}
}

// helper kept here to avoid an extra file in this small package.
func writeFile(path, content string) error {
	return writeFileImpl(path, content)
}
