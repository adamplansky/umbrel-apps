package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRunSearchOutputsEpisodesTSV(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/search/shows":
			if got := r.URL.Query().Get("q"); got != "Pripady 1 oddeleni" {
				t.Fatalf("query = %q", got)
			}
			body = `[{"score":0.98,"show":{"id":42,"name":"Případy 1. oddělení","url":"https://example/show","language":"Czech","genres":["Crime"],"premiered":"2014-01-06"}}]`
		case "/shows/42/episodes":
			body = `[
				{"id":2,"url":"https://example/e2","name":"Second","season":1,"number":2,"airdate":"2014-01-13","runtime":61,"summary":"<p>Beta &amp; gamma.</p>"},
				{"id":1,"url":"https://example/e1","name":"First","season":1,"number":1,"airdate":"2014-01-06","runtime":61,"summary":"<p>Alpha.</p>"}
			]`
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})

	var stdout, stderr bytes.Buffer
	err := runWithHTTPClient(
		[]string{"Pripady", "1", "oddeleni"},
		&stdout,
		&stderr,
		"https://api.test",
		&http.Client{Transport: transport},
	)
	if err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	if !strings.Contains(stderr.String(), "selected show: Případy 1. oddělení") {
		t.Fatalf("stderr did not mention selected show: %s", stderr.String())
	}
	if !strings.Contains(out, "show_id\tshow_name\tcode\tseason\tepisode") {
		t.Fatalf("missing TSV header: %s", out)
	}
	if !strings.Contains(out, "S01E01") {
		t.Fatalf("missing episode code: %s", out)
	}
	if strings.Index(out, "First") > strings.Index(out, "Second") {
		t.Fatalf("episodes are not sorted: %s", out)
	}
	if !strings.Contains(out, "Beta & gamma.") {
		t.Fatalf("summary was not cleaned: %s", out)
	}
}

func TestCleanText(t *testing.T) {
	got := cleanText("<p>Hello<br>world &amp; TV.</p>")
	if got != "Hello world & TV." {
		t.Fatalf("cleanText() = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
