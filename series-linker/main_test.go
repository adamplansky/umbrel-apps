package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRunLinksEpisodes(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var response string
		switch r.Method + " " + r.URL.Path {
		case "GET /search/shows":
			if got := r.URL.Query().Get("q"); got != "Pripady 1 oddeleni" {
				t.Fatalf("tvmaze q = %q", got)
			}
			response = `[{"score":1.0,"show":{"id":13257,"name":"Případy 1. oddělení","url":"https://tvmaze/show"}}]`
		case "GET /shows/13257/episodes":
			response = `[
				{"id":1,"url":"https://tvmaze/e1","name":"Rozčtvrcená","season":1,"number":1,"airdate":"2014-01-06","runtime":60},
				{"id":2,"url":"https://tvmaze/e2","name":"Fantom z Jižáku","season":1,"number":2,"airdate":"2014-01-13","runtime":60}
			]`
		case "POST /search/":
			values := readForm(t, r)
			query := values.Get("what")
			switch {
			case strings.Contains(query, "S01E01"):
				response = `<response><status>OK</status><total>2</total>
					<file><ident>wrong</ident><name>Případy 1 oddělení S01E02 Fantom.mkv</name><type>mkv</type><size>800000000</size><password>0</password></file>
					<file><ident>e1</ident><name>Případy 1 oddělení S01E01 Rozčtvrcená.mkv</name><type>mkv</type><size>816341981</size><positive_votes>2</positive_votes><password>0</password></file>
				</response>`
			case strings.Contains(query, "S01E02"):
				response = `<response><status>OK</status><total>1</total>
					<file><ident>e2</ident><name>Pripady 1 oddeleni S01E02 Fantom z Jizaku.mkv</name><type>mkv</type><size>782016574</size><password>0</password></file>
				</response>`
			default:
				response = `<response><status>OK</status><total>0</total></response>`
			}
		case "POST /file_link/":
			values := readForm(t, r)
			response = `<response><status>OK</status><link>https://download.example/` + values.Get("ident") + `</link></response>`
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		return testResponse(r, response), nil
	})

	var stdout, stderr bytes.Buffer
	err := run(
		[]string{"-search-limit", "3", "Pripady", "1", "oddeleni"},
		&stdout,
		&stderr,
		"https://tvmaze.test",
		"https://webshare.test",
		&http.Client{Transport: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "S01E01") || !strings.Contains(out, "https://download.example/e1") {
		t.Fatalf("missing e1 match: %s", out)
	}
	if !strings.Contains(out, "S01E02") || !strings.Contains(out, "https://download.example/e2") {
		t.Fatalf("missing e2 match: %s", out)
	}
	if strings.Contains(out, "https://download.example/wrong") {
		t.Fatalf("picked wrong episode: %s", out)
	}
}

func TestScoreCandidatePenalizesWrongEpisode(t *testing.T) {
	episode := Episode{Name: "Rozčtvrcená", Season: 1, Number: 1}
	good := scoreCandidate("Případy 1. oddělení", episode, WebshareFile{Name: "Případy 1 oddělení S01E01 Rozčtvrcená.mkv", Type: "mkv", Size: 800000000})
	bad := scoreCandidate("Případy 1. oddělení", episode, WebshareFile{Name: "Případy 1 oddělení S01E02 Fantom.mkv", Type: "mkv", Size: 800000000})
	if good <= bad {
		t.Fatalf("good score %.2f <= bad score %.2f", good, bad)
	}
}

func TestMD5Crypt(t *testing.T) {
	if got := md5crypt("password", "abcdefgh"); got != "$1$abcdefgh$G//4keteveJp0qb8z2DxG/" {
		t.Fatalf("md5crypt() = %q", got)
	}
}

func readForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func testResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
