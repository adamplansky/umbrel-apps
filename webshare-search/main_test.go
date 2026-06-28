package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRunSearchesAndGeneratesLinks(t *testing.T) {
	var sawWST bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(bodyBytes))
		if err != nil {
			t.Fatal(err)
		}

		var response string
		switch r.URL.Path {
		case "/api/search/":
			if got := values.Get("what"); got != "ubuntu iso" {
				t.Fatalf("what = %q", got)
			}
			response = `<?xml version="1.0" encoding="UTF-8"?>
<response><status>OK</status><total>1</total><file><ident>abc123</ident><name>ubuntu.iso</name><type>iso</type><size>2048</size><positive_votes>3</positive_votes><negative_votes>1</negative_votes><password>0</password></file></response>`
		case "/api/file_link/":
			if got := values.Get("ident"); got != "abc123" {
				t.Fatalf("ident = %q", got)
			}
			if values.Get("download_type") != "file_download" {
				t.Fatalf("download_type = %q", values.Get("download_type"))
			}
			if values.Get("force_https") != "1" {
				t.Fatalf("force_https = %q", values.Get("force_https"))
			}
			if values.Get("wst") == "token123" {
				sawWST = true
			}
			response = `<?xml version="1.0" encoding="UTF-8"?>
<response><status>OK</status><link>https://free.example/abc123/ubuntu.iso</link></response>`
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    r,
		}, nil
	})

	var stdout, stderr bytes.Buffer
	err := run(
		[]string{"ubuntu", "iso"},
		&stdout,
		&stderr,
		"https://example.test/api",
		&http.Client{Transport: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sawWST {
		t.Fatal("unexpected token in anonymous run")
	}

	out := stdout.String()
	if !strings.Contains(out, "ident\tname\ttype\tsize") {
		t.Fatalf("missing header: %s", out)
	}
	if !strings.Contains(out, "abc123\tubuntu.iso\tiso\t2048\t2.0 KiB") {
		t.Fatalf("missing file row: %s", out)
	}
	if !strings.Contains(out, "https://free.example/abc123/ubuntu.iso") {
		t.Fatalf("missing generated URL: %s", out)
	}
}

func TestRunLogsInAndUsesToken(t *testing.T) {
	var sawTokenInSearch, sawTokenInFileLink bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(bodyBytes))
		if err != nil {
			t.Fatal(err)
		}

		var response string
		switch r.URL.Path {
		case "/api/salt/":
			if got := values.Get("username_or_email"); got != "adam@example.com" {
				t.Fatalf("username_or_email = %q", got)
			}
			response = `<?xml version="1.0" encoding="UTF-8"?><response><status>OK</status><salt>abcdefgh</salt></response>`
		case "/api/login/":
			if got := values.Get("password"); got != authHash("password", "abcdefgh") {
				t.Fatalf("password hash = %q", got)
			}
			if values.Get("wst") == "" {
				t.Fatal("missing login wst")
			}
			response = `<?xml version="1.0" encoding="UTF-8"?><response><status>OK</status><token>token123</token></response>`
		case "/api/search/":
			sawTokenInSearch = values.Get("wst") == "token123"
			response = `<?xml version="1.0" encoding="UTF-8"?>
<response><status>OK</status><total>1</total><file><ident>abc123</ident><name>ubuntu.iso</name><type>iso</type><size>2048</size><password>0</password></file></response>`
		case "/api/file_link/":
			sawTokenInFileLink = values.Get("wst") == "token123"
			response = `<?xml version="1.0" encoding="UTF-8"?><response><status>OK</status><link>https://vip.example/abc123/ubuntu.iso</link></response>`
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    r,
		}, nil
	})

	var stdout, stderr bytes.Buffer
	err := run(
		[]string{"-username", "adam@example.com", "-password", "password", "ubuntu"},
		&stdout,
		&stderr,
		"https://example.test/api",
		&http.Client{Transport: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "logged in to Webshare") {
		t.Fatalf("missing login notice: %s", stderr.String())
	}
	if !sawTokenInSearch || !sawTokenInFileLink {
		t.Fatalf("token not sent to search=%v file_link=%v", sawTokenInSearch, sawTokenInFileLink)
	}
	if !strings.Contains(stdout.String(), "https://vip.example/abc123/ubuntu.iso") {
		t.Fatalf("missing vip URL: %s", stdout.String())
	}
}

func TestHumanSize(t *testing.T) {
	if got := humanSize(1536); got != "1.5 KiB" {
		t.Fatalf("humanSize() = %q", got)
	}
}

func TestMD5Crypt(t *testing.T) {
	if got := md5crypt("password", "abcdefgh"); got != "$1$abcdefgh$G//4keteveJp0qb8z2DxG/" {
		t.Fatalf("md5crypt() = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
