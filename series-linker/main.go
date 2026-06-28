package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultTVMazeBaseURL   = "https://api.tvmaze.com"
	defaultWebshareBaseURL = "https://webshare.cz/api"
)

type Config struct {
	Query           string
	ShowID          int
	Format          string
	SearchLimit     int
	Candidates      int
	Timeout         time.Duration
	Specials        bool
	IncludePassword bool
	Username        string
	Password        string
	Token           string
	KeepLoggedIn    bool
	MinScore        float64
}

type TVMazeClient struct {
	baseURL    string
	httpClient *http.Client
}

type TVSearchResult struct {
	Score float64 `json:"score"`
	Show  Show    `json:"show"`
}

type Show struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Language  string   `json:"language"`
	Genres    []string `json:"genres"`
	Premiered string   `json:"premiered"`
	Ended     string   `json:"ended"`
}

type Episode struct {
	ID      int    `json:"id"`
	URL     string `json:"url"`
	Name    string `json:"name"`
	Season  int    `json:"season"`
	Number  int    `json:"number"`
	Airdate string `json:"airdate"`
	Runtime int    `json:"runtime"`
}

type WebshareClient struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

type saltResponse struct {
	Status  string `xml:"status"`
	Code    string `xml:"code"`
	Message string `xml:"message"`
	Salt    string `xml:"salt"`
}

type loginResponse struct {
	Status  string `xml:"status"`
	Code    string `xml:"code"`
	Message string `xml:"message"`
	Token   string `xml:"token"`
	Reason  string `xml:"reason"`
}

type webshareSearchResponse struct {
	Status  string         `xml:"status"`
	Code    string         `xml:"code"`
	Message string         `xml:"message"`
	Total   int            `xml:"total"`
	Files   []WebshareFile `xml:"file"`
}

type WebshareFile struct {
	Ident         string `xml:"ident" json:"ident"`
	Name          string `xml:"name" json:"name"`
	Type          string `xml:"type" json:"type"`
	Size          int64  `xml:"size" json:"size"`
	PositiveVotes int    `xml:"positive_votes" json:"positive_votes"`
	NegativeVotes int    `xml:"negative_votes" json:"negative_votes"`
	Password      int    `xml:"password" json:"password"`
}

type linkResponse struct {
	Status  string `xml:"status"`
	Code    string `xml:"code"`
	Message string `xml:"message"`
	Link    string `xml:"link"`
}

type Match struct {
	ShowID            int         `json:"show_id"`
	ShowName          string      `json:"show_name"`
	ShowURL           string      `json:"show_url"`
	Code              string      `json:"code"`
	Season            int         `json:"season"`
	Episode           int         `json:"episode"`
	EpisodeName       string      `json:"episode_name"`
	EpisodeAirdate    string      `json:"episode_airdate"`
	EpisodeURL        string      `json:"episode_url"`
	QueryUsed         string      `json:"query_used"`
	WebshareIdent     string      `json:"webshare_ident,omitempty"`
	WebshareName      string      `json:"webshare_name,omitempty"`
	WebshareType      string      `json:"webshare_type,omitempty"`
	WebshareSize      int64       `json:"webshare_size,omitempty"`
	WebshareSizeHuman string      `json:"webshare_size_human,omitempty"`
	Score             float64     `json:"score"`
	URL               string      `json:"url,omitempty"`
	Error             string      `json:"error,omitempty"`
	Candidates        []Candidate `json:"candidates,omitempty"`
}

type Candidate struct {
	Ident     string  `json:"ident"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Size      int64   `json:"size"`
	SizeHuman string  `json:"size_human"`
	Score     float64 `json:"score"`
	Query     string  `json:"query"`
	URL       string  `json:"url,omitempty"`
	Error     string  `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, defaultTVMazeBaseURL, defaultWebshareBaseURL, nil); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, tvmazeBaseURL, webshareBaseURL string, httpClient *http.Client) error {
	cfg, err := parseConfig(args, stderr)
	if err != nil {
		return err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	tv := TVMazeClient{baseURL: strings.TrimRight(tvmazeBaseURL, "/"), httpClient: httpClient}
	ws := WebshareClient{baseURL: strings.TrimRight(webshareBaseURL, "/"), httpClient: httpClient, token: cfg.Token}

	if ws.token == "" && cfg.Username != "" {
		if cfg.Password == "" {
			return errors.New("password is required when -username is set; use -password or WEBSHARE_PASSWORD")
		}
		token, err := ws.Login(ctx, cfg.Username, cfg.Password, cfg.KeepLoggedIn)
		if err != nil {
			return err
		}
		ws.token = token
		fmt.Fprintln(stderr, "logged in to Webshare")
	} else if ws.token != "" {
		fmt.Fprintln(stderr, "using Webshare session token")
	}

	show, episodes, err := loadShowEpisodes(ctx, tv, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "selected show: %s (id=%d), episodes=%d\n", show.Name, show.ID, len(episodes))

	matches := make([]Match, 0, len(episodes))
	for _, episode := range episodes {
		match := linkEpisode(ctx, ws, show, episode, cfg)
		matches = append(matches, match)
		if match.URL != "" {
			fmt.Fprintf(stderr, "matched %s -> %s (score %.2f)\n", match.Code, match.WebshareName, match.Score)
		} else {
			fmt.Fprintf(stderr, "no URL for %s: %s\n", match.Code, match.Error)
		}
	}

	switch cfg.Format {
	case "tsv":
		return writeTSV(stdout, matches, '\t')
	case "csv":
		return writeTSV(stdout, matches, ',')
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(matches)
	default:
		return fmt.Errorf("unsupported -format %q; use tsv, csv, or json", cfg.Format)
	}
}

func parseConfig(args []string, stderr io.Writer) (Config, error) {
	cfg := Config{
		Format:      "tsv",
		SearchLimit: 8,
		Candidates:  1,
		Timeout:     2 * time.Minute,
		Username:    env("WEBSHARE_USERNAME", ""),
		Password:    env("WEBSHARE_PASSWORD", ""),
		Token:       env("WEBSHARE_WST", ""),
		MinScore:    0.35,
	}

	fs := flag.NewFlagSet("series-linker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.Query, "query", "", "TV show name to search")
	fs.IntVar(&cfg.ShowID, "show-id", 0, "TVmaze show ID; skips show search when set")
	fs.StringVar(&cfg.Format, "format", cfg.Format, "output format: tsv, csv, json")
	fs.IntVar(&cfg.SearchLimit, "search-limit", cfg.SearchLimit, "Webshare results per generated episode query")
	fs.IntVar(&cfg.Candidates, "candidates", cfg.Candidates, "number of top candidates to keep in JSON output")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "total HTTP timeout")
	fs.BoolVar(&cfg.Specials, "specials", false, "include TVmaze specials")
	fs.BoolVar(&cfg.IncludePassword, "include-password", false, "try password-protected Webshare files too")
	fs.StringVar(&cfg.Username, "username", cfg.Username, "Webshare username or email; can also use WEBSHARE_USERNAME")
	fs.StringVar(&cfg.Password, "password", cfg.Password, "Webshare password; prefer WEBSHARE_PASSWORD")
	fs.StringVar(&cfg.Token, "wst", cfg.Token, "existing Webshare session token; can also use WEBSHARE_WST")
	fs.BoolVar(&cfg.KeepLoggedIn, "keep-logged-in", false, "ask Webshare to keep the generated session token logged in")
	fs.Float64Var(&cfg.MinScore, "min-score", cfg.MinScore, "minimum score required before generating a URL")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 && cfg.Query == "" {
		cfg.Query = strings.Join(fs.Args(), " ")
	}
	cfg.Query = strings.TrimSpace(cfg.Query)
	if cfg.Query == "" && cfg.ShowID == 0 {
		return cfg, errors.New("provide a show query or -show-id")
	}
	if cfg.SearchLimit <= 0 {
		return cfg, errors.New("-search-limit must be greater than zero")
	}
	if cfg.Candidates <= 0 {
		return cfg, errors.New("-candidates must be greater than zero")
	}
	return cfg, nil
}

func loadShowEpisodes(ctx context.Context, tv TVMazeClient, cfg Config) (Show, []Episode, error) {
	var show Show
	if cfg.ShowID != 0 {
		found, err := tv.Show(ctx, cfg.ShowID)
		if err != nil {
			return show, nil, err
		}
		show = found
	} else {
		results, err := tv.Search(ctx, cfg.Query)
		if err != nil {
			return show, nil, err
		}
		if len(results) == 0 {
			return show, nil, fmt.Errorf("no TVmaze show found for %q", cfg.Query)
		}
		show = results[0].Show
	}
	episodes, err := tv.Episodes(ctx, show.ID, cfg.Specials)
	if err != nil {
		return show, nil, err
	}
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		return episodes[i].Number < episodes[j].Number
	})
	return show, episodes, nil
}

func linkEpisode(ctx context.Context, ws WebshareClient, show Show, episode Episode, cfg Config) Match {
	match := Match{
		ShowID:         show.ID,
		ShowName:       show.Name,
		ShowURL:        show.URL,
		Code:           episodeCode(episode),
		Season:         episode.Season,
		Episode:        episode.Number,
		EpisodeName:    episode.Name,
		EpisodeAirdate: episode.Airdate,
		EpisodeURL:     episode.URL,
	}

	queries := episodeQueries(show.Name, episode)
	candidatesByIdent := map[string]Candidate{}
	for _, query := range queries {
		response, err := ws.Search(ctx, query, 0, cfg.SearchLimit)
		if err != nil {
			if match.Error == "" {
				match.Error = err.Error()
			}
			continue
		}
		for _, file := range response.Files {
			if file.Password != 0 && !cfg.IncludePassword {
				continue
			}
			candidate := Candidate{
				Ident:     file.Ident,
				Name:      file.Name,
				Type:      file.Type,
				Size:      file.Size,
				SizeHuman: humanSize(file.Size),
				Query:     query,
				Score:     scoreCandidate(show.Name, episode, file),
			}
			if previous, ok := candidatesByIdent[file.Ident]; !ok || candidate.Score > previous.Score {
				candidatesByIdent[file.Ident] = candidate
			}
		}
	}

	candidates := make([]Candidate, 0, len(candidatesByIdent))
	for _, candidate := range candidatesByIdent {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Size > candidates[j].Size
	})
	if len(candidates) == 0 {
		if match.Error == "" {
			match.Error = "no Webshare candidates"
		}
		return match
	}

	kept := min(cfg.Candidates, len(candidates))
	match.Candidates = candidates[:kept]

	for i := range candidates {
		if candidates[i].Score < cfg.MinScore {
			continue
		}
		link, err := ws.FileLink(ctx, candidates[i].Ident)
		if err != nil {
			candidates[i].Error = err.Error()
			if i < kept {
				match.Candidates[i].Error = err.Error()
			}
			continue
		}
		candidates[i].URL = link
		match.QueryUsed = candidates[i].Query
		match.WebshareIdent = candidates[i].Ident
		match.WebshareName = candidates[i].Name
		match.WebshareType = candidates[i].Type
		match.WebshareSize = candidates[i].Size
		match.WebshareSizeHuman = candidates[i].SizeHuman
		match.Score = roundScore(candidates[i].Score)
		match.URL = link
		if i < kept {
			match.Candidates[i].URL = link
		}
		return match
	}

	match.Error = fmt.Sprintf("no candidate above min score %.2f with generated URL", cfg.MinScore)
	if len(candidates) > 0 {
		match.QueryUsed = candidates[0].Query
		match.WebshareIdent = candidates[0].Ident
		match.WebshareName = candidates[0].Name
		match.WebshareType = candidates[0].Type
		match.WebshareSize = candidates[0].Size
		match.WebshareSizeHuman = candidates[0].SizeHuman
		match.Score = roundScore(candidates[0].Score)
	}
	return match
}

func (c TVMazeClient) Search(ctx context.Context, query string) ([]TVSearchResult, error) {
	var results []TVSearchResult
	err := c.getJSON(ctx, "/search/shows?q="+url.QueryEscape(query), &results)
	return results, err
}

func (c TVMazeClient) Show(ctx context.Context, id int) (Show, error) {
	var show Show
	err := c.getJSON(ctx, fmt.Sprintf("/shows/%d", id), &show)
	return show, err
}

func (c TVMazeClient) Episodes(ctx context.Context, showID int, specials bool) ([]Episode, error) {
	path := fmt.Sprintf("/shows/%d/episodes", showID)
	if specials {
		path += "?specials=1"
	}
	var episodes []Episode
	err := c.getJSON(ctx, path, &episodes)
	return episodes, err
}

func (c TVMazeClient) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "series-linker/0.1")
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

func (c WebshareClient) Search(ctx context.Context, query string, offset, limit int) (webshareSearchResponse, error) {
	values := url.Values{}
	values.Set("what", query)
	values.Set("offset", strconv.Itoa(offset))
	values.Set("limit", strconv.Itoa(limit))
	var response webshareSearchResponse
	if err := c.postXML(ctx, "/search/", values, &response); err != nil {
		return response, err
	}
	if response.Status != "OK" {
		return response, apiError(response.Status, response.Code, response.Message)
	}
	return response, nil
}

func (c WebshareClient) FileLink(ctx context.Context, ident string) (string, error) {
	values := url.Values{}
	values.Set("ident", ident)
	values.Set("download_type", "file_download")
	values.Set("force_https", "1")
	var response linkResponse
	if err := c.postXML(ctx, "/file_link/", values, &response); err != nil {
		return "", err
	}
	if response.Status != "OK" {
		return "", apiError(response.Status, response.Code, response.Message)
	}
	if response.Link == "" {
		return "", errors.New("empty download URL")
	}
	return response.Link, nil
}

func (c WebshareClient) Login(ctx context.Context, usernameOrEmail, password string, keepLoggedIn bool) (string, error) {
	saltValues := url.Values{}
	saltValues.Set("username_or_email", usernameOrEmail)
	var salt saltResponse
	if err := c.postXML(ctx, "/salt/", saltValues, &salt); err != nil {
		return "", err
	}
	if salt.Status != "OK" {
		return "", apiError(salt.Status, salt.Code, salt.Message)
	}

	loginValues := url.Values{}
	loginValues.Set("username_or_email", usernameOrEmail)
	loginValues.Set("password", authHash(password, salt.Salt))
	if keepLoggedIn {
		loginValues.Set("keep_logged_in", "1")
	} else {
		loginValues.Set("keep_logged_in", "0")
	}
	loginValues.Set("wst", randomToken(16))
	var login loginResponse
	if err := c.postXML(ctx, "/login/", loginValues, &login); err != nil {
		return "", err
	}
	if login.Status != "OK" {
		message := login.Message
		if message == "" {
			message = login.Reason
		}
		return "", apiError(login.Status, login.Code, message)
	}
	if login.Token == "" {
		return "", errors.New("login succeeded but Webshare returned an empty token")
	}
	return login.Token, nil
}

func (c WebshareClient) postXML(ctx context.Context, path string, values url.Values, target any) error {
	if c.token != "" && values.Get("wst") == "" {
		values.Set("wst", c.token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/xml")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "series-linker/0.1")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("webshare request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return xml.NewDecoder(resp.Body).Decode(target)
}

func episodeQueries(showName string, episode Episode) []string {
	code := episodeCode(episode)
	altCode := fmt.Sprintf("%dx%02d", episode.Season, episode.Number)
	plainShow := asciiFold(showName)
	plainEpisode := asciiFold(episode.Name)
	queries := []string{
		fmt.Sprintf("%s %s", showName, code),
		fmt.Sprintf("%s %s %s", showName, code, episode.Name),
		fmt.Sprintf("%s %s", plainShow, code),
		fmt.Sprintf("%s %s %s", plainShow, code, plainEpisode),
		fmt.Sprintf("%s %s", showName, altCode),
		fmt.Sprintf("%s %s", plainShow, altCode),
	}
	return uniqueStrings(queries)
}

func scoreCandidate(showName string, episode Episode, file WebshareFile) float64 {
	name := normalize(file.Name)
	show := normalize(showName)
	episodeName := normalize(episode.Name)
	code := strings.ToLower(episodeCode(episode))
	altCode := strings.ToLower(fmt.Sprintf("%dx%02d", episode.Season, episode.Number))

	score := 0.0
	if strings.Contains(name, code) || strings.Contains(name, altCode) {
		score += 0.45
	} else if containsWrongEpisodeCode(name, episode.Season, episode.Number) {
		score -= 0.55
	}
	score += 0.25 * tokenOverlap(show, name)
	if episodeName != "" {
		score += 0.15 * tokenOverlap(episodeName, name)
	}
	if isVideoType(file.Type, file.Name) {
		score += 0.08
	}
	if file.Size >= 200*1024*1024 {
		score += 0.05
	} else if file.Size > 0 && file.Size < 50*1024*1024 {
		score -= 0.15
	}
	if file.Password != 0 {
		score -= 0.25
	}
	score += math.Min(float64(file.PositiveVotes), 10) * 0.005
	score -= math.Min(float64(file.NegativeVotes), 10) * 0.01
	return roundScore(score)
}

var episodeCodeRE = regexp.MustCompile(`(?i)(?:s(\d{1,2})e(\d{1,2})|(\d{1,2})x(\d{1,2}))`)

func containsWrongEpisodeCode(name string, season, episode int) bool {
	matches := episodeCodeRE.FindAllStringSubmatch(name, -1)
	for _, match := range matches {
		var s, e int
		if match[1] != "" {
			s, _ = strconv.Atoi(match[1])
			e, _ = strconv.Atoi(match[2])
		} else {
			s, _ = strconv.Atoi(match[3])
			e, _ = strconv.Atoi(match[4])
		}
		if s != season || e != episode {
			return true
		}
	}
	return false
}

func tokenOverlap(needle, haystack string) float64 {
	needleTokens := tokenSet(needle)
	if len(needleTokens) == 0 {
		return 0
	}
	haystackTokens := tokenSet(haystack)
	matches := 0
	for token := range needleTokens {
		if haystackTokens[token] {
			matches++
		}
	}
	return float64(matches) / float64(len(needleTokens))
}

func tokenSet(value string) map[string]bool {
	fields := strings.Fields(normalize(value))
	out := make(map[string]bool, len(fields))
	for _, field := range fields {
		if len(field) >= 2 {
			out[field] = true
		}
	}
	return out
}

func normalize(value string) string {
	value = strings.ToLower(asciiFold(value))
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func asciiFold(value string) string {
	replacer := strings.NewReplacer(
		"á", "a", "č", "c", "ď", "d", "é", "e", "ě", "e", "í", "i", "ň", "n", "ó", "o", "ř", "r", "š", "s", "ť", "t", "ú", "u", "ů", "u", "ý", "y", "ž", "z",
		"Á", "A", "Č", "C", "Ď", "D", "É", "E", "Ě", "E", "Í", "I", "Ň", "N", "Ó", "O", "Ř", "R", "Š", "S", "Ť", "T", "Ú", "U", "Ů", "U", "Ý", "Y", "Ž", "Z",
	)
	return replacer.Replace(value)
}

func isVideoType(fileType, name string) bool {
	typ := strings.ToLower(strings.TrimPrefix(fileType, "."))
	if typ == "" {
		parts := strings.Split(name, ".")
		if len(parts) > 1 {
			typ = strings.ToLower(parts[len(parts)-1])
		}
	}
	switch typ {
	case "mkv", "mp4", "avi", "mov", "wmv", "webm", "m4v":
		return true
	default:
		return false
	}
}

func episodeCode(episode Episode) string {
	return fmt.Sprintf("S%02dE%02d", episode.Season, episode.Number)
}

func writeTSV(w io.Writer, matches []Match, comma rune) error {
	cw := csv.NewWriter(w)
	cw.Comma = comma
	if err := cw.Write([]string{
		"show",
		"code",
		"episode_name",
		"webshare_name",
		"size",
		"score",
		"url",
		"error",
	}); err != nil {
		return err
	}
	for _, match := range matches {
		if err := cw.Write([]string{
			match.ShowName,
			match.Code,
			match.EpisodeName,
			match.WebshareName,
			match.WebshareSizeHuman,
			fmt.Sprintf("%.2f", match.Score),
			match.URL,
			match.Error,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func apiError(status, code, message string) error {
	parts := []string{"API returned " + status}
	if code != "" {
		parts = append(parts, code)
	}
	if message != "" {
		parts = append(parts, message)
	}
	return errors.New(strings.Join(parts, ": "))
}

func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func authHash(password, salt string) string {
	sum := sha1.Sum([]byte(md5crypt(password, salt)))
	return hex.EncodeToString(sum[:])
}

func md5crypt(password, salt string) string {
	const magic = "$1$"
	if strings.HasPrefix(salt, magic) {
		salt = strings.TrimPrefix(salt, magic)
	}
	if idx := strings.IndexByte(salt, '$'); idx >= 0 {
		salt = salt[:idx]
	}
	if len(salt) > 8 {
		salt = salt[:8]
	}

	ctx := md5.New()
	ctx.Write([]byte(password))
	ctx.Write([]byte(magic))
	ctx.Write([]byte(salt))

	alt := md5.New()
	alt.Write([]byte(password))
	alt.Write([]byte(salt))
	alt.Write([]byte(password))
	altSum := alt.Sum(nil)
	for i := len(password); i > 0; i -= 16 {
		if i > 16 {
			ctx.Write(altSum)
		} else {
			ctx.Write(altSum[:i])
		}
	}
	for i := len(password); i > 0; i >>= 1 {
		if i&1 == 1 {
			ctx.Write([]byte{0})
		} else {
			ctx.Write([]byte{password[0]})
		}
	}

	final := ctx.Sum(nil)
	for i := 0; i < 1000; i++ {
		round := md5.New()
		if i&1 == 1 {
			round.Write([]byte(password))
		} else {
			round.Write(final)
		}
		if i%3 != 0 {
			round.Write([]byte(salt))
		}
		if i%7 != 0 {
			round.Write([]byte(password))
		}
		if i&1 == 1 {
			round.Write(final)
		} else {
			round.Write([]byte(password))
		}
		final = round.Sum(nil)
	}

	return magic + salt + "$" +
		to64(uint32(final[0])<<16|uint32(final[6])<<8|uint32(final[12]), 4) +
		to64(uint32(final[1])<<16|uint32(final[7])<<8|uint32(final[13]), 4) +
		to64(uint32(final[2])<<16|uint32(final[8])<<8|uint32(final[14]), 4) +
		to64(uint32(final[3])<<16|uint32(final[9])<<8|uint32(final[15]), 4) +
		to64(uint32(final[4])<<16|uint32(final[10])<<8|uint32(final[5]), 4) +
		to64(uint32(final[11]), 2)
}

func to64(value uint32, length int) string {
	const table = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var out strings.Builder
	for i := 0; i < length; i++ {
		out.WriteByte(table[value&0x3f])
		value >>= 6
	}
	return out.String()
}

func randomToken(length int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		key := normalize(value)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func roundScore(score float64) float64 {
	return math.Round(score*100) / 100
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
