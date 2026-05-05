package creds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- IMDS ----

func TestIMDSDisabled(t *testing.T) {
	cfg := &Config{Env: envMap(map[string]string{"AWS_EC2_METADATA_DISABLED": "TRUE"})}
	if _, err := imdsProvider(context.Background(), cfg); !errors.Is(err, errNoMatch) {
		t.Errorf("got %v", err)
	}
}

func TestIMDSUnreachable(t *testing.T) {
	// Unroutable -> Do() error -> errNoMatch.
	cfg := &Config{
		Env:          emptyEnv,
		IMDSEndpoint: "http://127.0.0.1:1", // refused
		HTTPClient:   &http.Client{Timeout: 200 * time.Millisecond},
	}
	if _, err := imdsProvider(context.Background(), cfg); !errors.Is(err, errNoMatch) {
		t.Errorf("got %v", err)
	}
}

func newIMDSServer(t *testing.T, role string, credBody string, tokenStatus, roleStatus, credStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/api/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("token method=%s", r.Method)
		}
		if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
			t.Error("missing ttl header")
		}
		if tokenStatus != 0 {
			w.WriteHeader(tokenStatus)
			return
		}
		w.Write([]byte("TOK"))
	})
	mux.HandleFunc("/latest/meta-data/iam/security-credentials/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-aws-ec2-metadata-token") != "TOK" {
			t.Error("missing/wrong token header")
		}
		path := r.URL.Path
		if path == "/latest/meta-data/iam/security-credentials/" {
			if roleStatus != 0 {
				w.WriteHeader(roleStatus)
				return
			}
			w.Write([]byte(role))
			return
		}
		if !strings.HasSuffix(path, "/"+role) {
			t.Errorf("unexpected path %s", path)
		}
		if credStatus != 0 {
			w.WriteHeader(credStatus)
			return
		}
		w.Write([]byte(credBody))
	})
	return httptest.NewServer(mux)
}

func TestIMDSSuccess(t *testing.T) {
	body := `{"Code":"Success","AccessKeyId":"AK","SecretAccessKey":"SK","Token":"ST","Expiration":"2099-01-01T00:00:00Z"}`
	srv := newIMDSServer(t, "myrole", body, 0, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	v, err := imdsProvider(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if v.AccessKeyID != "AK" || v.SessionToken != "ST" || v.Expires.IsZero() {
		t.Errorf("got %+v", v)
	}
}

func TestIMDSTokenError(t *testing.T) {
	srv := newIMDSServer(t, "r", "", 500, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSRoleError(t *testing.T) {
	srv := newIMDSServer(t, "r", "", 0, 500, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSEmptyRole(t *testing.T) {
	srv := newIMDSServer(t, "", "", 0, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSCredsError(t *testing.T) {
	srv := newIMDSServer(t, "r", "", 0, 0, 500)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSCredsBadJSON(t *testing.T) {
	srv := newIMDSServer(t, "r", "not json", 0, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSCredsCodeFailure(t *testing.T) {
	srv := newIMDSServer(t, "r", `{"Code":"Failure"}`, 0, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSCredsIncomplete(t *testing.T) {
	srv := newIMDSServer(t, "r", `{"Code":"Success"}`, 0, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}

func TestIMDSBadExpiration(t *testing.T) {
	srv := newIMDSServer(t, "r", `{"Code":"Success","AccessKeyId":"a","SecretAccessKey":"b","Expiration":"not-a-time"}`, 0, 0, 0)
	defer srv.Close()
	cfg := &Config{Env: emptyEnv, IMDSEndpoint: srv.URL, HTTPClient: srv.Client()}
	if _, err := imdsProvider(context.Background(), cfg); err == nil {
		t.Error("want err")
	}
}
