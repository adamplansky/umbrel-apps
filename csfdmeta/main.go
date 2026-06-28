package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.tvmaze.com"

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type SearchResult struct {
	Score float64 `json:"score"`
	Show  Show    `json:"show"`
}

type Show struct {
	ID         int               `json:"id"`
	Name       string            `json:"name"`
	URL        string            `json:"url"`
	Type       string            `json:"type"`
	Language   string            `json:"language"`
	Genres     []string          `json:"genres"`
	Status     string            `json:"status"`
	Runtime    int               `json:"runtime"`
	Premiered  string            `json:"premiered"`
	Ended      string            `json:"ended"`
	Rating     map[string]any    `json:"rating"`
	Network    *Network          `json:"network"`
	WebChannel *Network          `json:"webChannel"`
	Summary    string            `json:"summary"`
	Externals  map[string]any    `json:"externals"`
	Image      map[string]string `json:"image"`
}

type Network struct {
	Name    string   `json:"name"`
	Country *Country `json:"country"`
}

type Country struct {
	Name     string `json:"name"`
	Code     string `json:"code"`
	Timezone string `json:"timezone"`
}

type Episode struct {
	ID      int               `json:"id"`
	URL     string            `json:"url"`
	Name    string            `json:"name"`
	Season  int               `json:"season"`
	Number  int               `json:"number"`
	Type    string            `json:"type"`
	Airdate string            `json:"airdate"`
	Runtime int               `json:"runtime"`
	Rating  map[string]any    `json:"rating"`
	Summary string            `json:"summary"`
	Image   map[string]string `json:"image"`
}

type EpisodeRow struct {
	ShowID        int      `json:"show_id"`
	ShowName      string   `json:"show_name"`
	ShowURL       string   `json:"show_url"`
	ShowPremiered string   `json:"show_premiered"`
	ShowEnded     string   `json:"show_ended"`
	ShowGenres    []string `json:"show_genres"`
	ShowLanguage  string   `json:"show_language"`
	Code          string   `json:"code"`
	Season        int      `json:"season"`
	Episode       int      `json:"episode"`
	EpisodeID     int      `json:"episode_id"`
	EpisodeName   string   `json:"episode_name"`
	Airdate       string   `json:"airdate"`
	Runtime       int      `json:"runtime"`
	EpisodeURL    string   `json:"episode_url"`
	Summary       string   `json:"summary"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, defaultBaseURL); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, baseURL string) error {
	return runWithHTTPClient(args, stdout, stderr, baseURL, nil)
}

func runWithHTTPClient(args []string, stdout, stderr io.Writer, baseURL string, httpClient *http.Client) error {
	var (
		query    string
		showID   int
		format   string
		specials bool
		timeout  time.Duration
	)

	fs := flag.NewFlagSet("csfdmeta", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&query, "query", "", "TV show name to search")
	fs.IntVar(&showID, "show-id", 0, "TVmaze show ID; skips search when set")
	fs.StringVar(&format, "format", "tsv", "output format: tsv, csv, json")
	fs.BoolVar(&specials, "specials", false, "include specials")
	fs.DurationVar(&timeout, "timeout", 15*time.Second, "HTTP timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 && query == "" {
		query = strings.Join(fs.Args(), " ")
	}
	if showID == 0 && strings.TrimSpace(query) == "" {
		return errors.New("provide -query or -show-id")
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: timeout,
		}
	}

	client := Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var show Show
	if showID != 0 {
		found, err := client.Show(ctx, showID)
		if err != nil {
			return err
		}
		show = found
	} else {
		results, err := client.Search(ctx, query)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			return fmt.Errorf("no TVmaze show found for %q", query)
		}
		show = results[0].Show
		fmt.Fprintf(stderr, "selected show: %s (id=%d, score=%.2f)\n", show.Name, show.ID, results[0].Score)
	}

	episodes, err := client.Episodes(ctx, show.ID, specials)
	if err != nil {
		return err
	}

	rows := make([]EpisodeRow, 0, len(episodes))
	for _, ep := range episodes {
		rows = append(rows, EpisodeRow{
			ShowID:        show.ID,
			ShowName:      show.Name,
			ShowURL:       show.URL,
			ShowPremiered: show.Premiered,
			ShowEnded:     show.Ended,
			ShowGenres:    show.Genres,
			ShowLanguage:  show.Language,
			Code:          fmt.Sprintf("S%02dE%02d", ep.Season, ep.Number),
			Season:        ep.Season,
			Episode:       ep.Number,
			EpisodeID:     ep.ID,
			EpisodeName:   ep.Name,
			Airdate:       ep.Airdate,
			Runtime:       ep.Runtime,
			EpisodeURL:    ep.URL,
			Summary:       cleanText(ep.Summary),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Season != rows[j].Season {
			return rows[i].Season < rows[j].Season
		}
		return rows[i].Episode < rows[j].Episode
	})

	switch format {
	case "tsv":
		return writeDelimited(stdout, rows, '\t')
	case "csv":
		return writeDelimited(stdout, rows, ',')
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	default:
		return fmt.Errorf("unsupported -format %q; use tsv, csv, or json", format)
	}
}

func (c Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var results []SearchResult
	err := c.getJSON(ctx, "/search/shows?q="+url.QueryEscape(query), &results)
	return results, err
}

func (c Client) Show(ctx context.Context, id int) (Show, error) {
	var show Show
	err := c.getJSON(ctx, fmt.Sprintf("/shows/%d", id), &show)
	return show, err
}

func (c Client) Episodes(ctx context.Context, showID int, specials bool) ([]Episode, error) {
	path := fmt.Sprintf("/shows/%d/episodes", showID)
	if specials {
		path += "?specials=1"
	}
	var episodes []Episode
	err := c.getJSON(ctx, path, &episodes)
	return episodes, err
}

func (c Client) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "csfdmeta/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("tvmaze request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func writeDelimited(w io.Writer, rows []EpisodeRow, comma rune) error {
	cw := csv.NewWriter(w)
	cw.Comma = comma
	if err := cw.Write([]string{
		"show_id",
		"show_name",
		"code",
		"season",
		"episode",
		"episode_name",
		"airdate",
		"runtime",
		"episode_url",
		"show_url",
		"genres",
		"summary",
	}); err != nil {
		return err
	}
	for _, row := range rows {
		if err := cw.Write([]string{
			fmt.Sprint(row.ShowID),
			row.ShowName,
			row.Code,
			fmt.Sprint(row.Season),
			fmt.Sprint(row.Episode),
			row.EpisodeName,
			row.Airdate,
			fmt.Sprint(row.Runtime),
			row.EpisodeURL,
			row.ShowURL,
			strings.Join(row.ShowGenres, "|"),
			row.Summary,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func cleanText(input string) string {
	withoutTags := tagRE.ReplaceAllString(input, " ")
	unescaped := html.UnescapeString(withoutTags)
	return strings.Join(strings.Fields(unescaped), " ")
}
