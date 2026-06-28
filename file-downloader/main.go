package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
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
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

type DownloadRecord struct {
	URL        string    `json:"url"`
	Filename   string    `json:"filename"`
	Downloaded time.Time `json:"downloaded"`
	Size       int64     `json:"size"`
}

type History struct {
	Downloads       map[string]DownloadRecord `json:"downloads"`
	DownloadedFiles map[string]string         `json:"downloaded_files"`
}

type ProgressWriter struct {
	Total      int64
	Downloaded int64
	Filename   string
	LastPrint  time.Time
}

// Global state for tracking current download (for cleanup on cancel)
var (
	currentDownloadPath string
	currentDownloadMu   sync.Mutex
)

func setCurrentDownload(path string) {
	currentDownloadMu.Lock()
	currentDownloadPath = path
	currentDownloadMu.Unlock()
}

func cleanupCurrentDownload() {
	currentDownloadMu.Lock()
	path := currentDownloadPath
	currentDownloadPath = ""
	currentDownloadMu.Unlock()

	if path != "" {
		os.Remove(path)
		fmt.Printf("\nCleaned up partial download: %s\n", filepath.Base(path))
	}
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.Downloaded += int64(n)

	if time.Since(pw.LastPrint) > 100*time.Millisecond {
		pw.printProgress()
		pw.LastPrint = time.Now()
	}
	return n, nil
}

func (pw *ProgressWriter) printProgress() {
	if pw.Total > 0 {
		pct := float64(pw.Downloaded) / float64(pw.Total) * 100
		bar := int(pct / 2)
		fmt.Printf("\r[%-50s] %6.2f%% %s / %s  %s",
			strings.Repeat("=", bar)+">",
			pct,
			formatBytes(pw.Downloaded),
			formatBytes(pw.Total),
			pw.Filename)
	} else {
		fmt.Printf("\r%s downloaded  %s", formatBytes(pw.Downloaded), pw.Filename)
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func loadEnvFiles(paths ...string) ([]string, error) {
	var loaded []string
	for _, path := range paths {
		if err := loadEnvFile(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return loaded, err
		}
		loaded = append(loaded, path)
	}
	return loaded, nil
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=value", path, lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !validEnvKey(key) {
			return fmt.Errorf("%s:%d: invalid environment key %q", path, lineNo, key)
		}
		if len(value) >= 2 {
			quote := value[0]
			if (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
				value = value[1 : len(value)-1]
			}
		}
		if existing, exists := os.LookupEnv(key); !exists || existing == "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func configuredMaxConcurrentDownloads(flagValue int) int {
	if flagValue > 0 {
		return flagValue
	}
	if value := strings.TrimSpace(os.Getenv("MAX_CONCURRENT_DOWNLOADS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
		fmt.Fprintf(os.Stderr, "Warning: invalid MAX_CONCURRENT_DOWNLOADS=%q, using %d\n", value, defaultMaxConcurrentDownloads)
	}
	return defaultMaxConcurrentDownloads
}

func configuredSeriesSearchConcurrency(value int) int {
	if value <= 0 {
		if envValue := strings.TrimSpace(os.Getenv("SERIES_SEARCH_CONCURRENCY")); envValue != "" {
			parsed, err := strconv.Atoi(envValue)
			if err == nil {
				value = parsed
			} else {
				fmt.Fprintf(os.Stderr, "Warning: invalid SERIES_SEARCH_CONCURRENCY=%q, using %d\n", envValue, defaultSeriesSearchConcurrency)
			}
		}
	}
	if value <= 0 {
		return defaultSeriesSearchConcurrency
	}
	if value > maxSeriesSearchConcurrency {
		return maxSeriesSearchConcurrency
	}
	return value
}

func loadHistory(historyFile string) (*History, bool, error) {
	history := &History{
		Downloads:       make(map[string]DownloadRecord),
		DownloadedFiles: make(map[string]string),
	}

	data, err := os.ReadFile(historyFile)
	if os.IsNotExist(err) {
		return history, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	if err := json.Unmarshal(data, history); err != nil {
		return nil, false, err
	}

	if history.Downloads == nil {
		history.Downloads = make(map[string]DownloadRecord)
	}
	if history.DownloadedFiles == nil {
		history.DownloadedFiles = make(map[string]string)
	}

	// Migrate: populate DownloadedFiles from Downloads if empty
	needsSave := false
	if len(history.DownloadedFiles) == 0 && len(history.Downloads) > 0 {
		for u := range history.Downloads {
			filename := filenameFromURL(u)
			history.DownloadedFiles[filename] = u
		}
		needsSave = true
	}

	return history, needsSave, nil
}

func saveHistory(historyFile string, history *History) error {
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(historyFile, data, 0644)
}

func urlHash(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:8])
}

func keys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	return k
}

func filenameFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return urlHash(rawURL)
	}

	filename := filepath.Base(parsed.Path)
	if filename == "" || filename == "." || filename == "/" {
		return urlHash(rawURL)
	}

	return filename
}

func safePathComponent(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		if r < 32 {
			continue
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			if unicode.IsSpace(r) {
				if !lastSpace {
					b.WriteByte(' ')
					lastSpace = true
				}
				continue
			}
			b.WriteRune(r)
			lastSpace = false
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		return fallback
	}
	return out
}

func safeRelativePath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		cleaned = append(cleaned, safePathComponent(part, "Unknown"))
	}
	return filepath.Join(cleaned...)
}

func movieTargetPath(filename string) string {
	filename = safePathComponent(filename, "download")
	ext := filepath.Ext(filename)
	title := strings.TrimSuffix(filename, ext)
	return safeRelativePath("Movies", title, filename)
}

func movieMetadataTargetPath(item MovieDownloadItem) string {
	title := safePathComponent(item.Title, "Unknown Movie")
	folder := title
	if item.Year != "" {
		folder = fmt.Sprintf("%s (%s)", title, item.Year)
	}
	ext := filepath.Ext(item.Filename)
	if ext == "" {
		ext = filepath.Ext(filenameFromURL(item.URL))
	}
	if ext == "" {
		ext = ".mp4"
	}
	filename := folder + ext
	return safeRelativePath("Movies", folder, filename)
}

func yearFromDate(date string) string {
	if len(date) >= 4 {
		year := date[:4]
		if _, err := strconv.Atoi(year); err == nil {
			return year
		}
	}
	return ""
}

func showFolderName(name, year string) string {
	name = safePathComponent(name, "Unknown Series")
	if year == "" {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, year)
}

func seriesTargetPath(item SeriesDownloadItem) string {
	showName := safePathComponent(item.ShowName, "Unknown Series")
	showFolder := showFolderName(showName, item.ShowYear)
	seasonFolder := fmt.Sprintf("Season %02d", item.Season)
	ext := filepath.Ext(item.Filename)
	if ext == "" {
		ext = filepath.Ext(filenameFromURL(item.URL))
	}
	if ext == "" {
		ext = ".mp4"
	}
	filename := fmt.Sprintf("%s S%02dE%02d%s", showName, item.Season, item.Episode, ext)
	return safeRelativePath("Shows", showFolder, seasonFolder, filename)
}

func resolveOutputPath(outputDir, relativePath string) (string, error) {
	if relativePath == "" {
		return "", errors.New("relative path is required")
	}
	clean := filepath.Clean(relativePath)
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid output path: %s", relativePath)
	}
	base, err := filepath.Abs(outputDir)
	if err != nil {
		return "", err
	}
	outputPath, err := filepath.Abs(filepath.Join(outputDir, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, outputPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid output path: %s", relativePath)
	}
	return outputPath, nil
}

func downloadFile(ctx context.Context, rawURL, outputDir string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("bad status: %s", resp.Status)
	}

	filename := filenameFromURL(rawURL)
	outputPath := filepath.Join(outputDir, filename)

	// Handle duplicate filenames on disk
	if _, err := os.Stat(outputPath); err == nil {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		outputPath = filepath.Join(outputDir, fmt.Sprintf("%s_%s%s", base, urlHash(rawURL), ext))
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return "", 0, err
	}

	// Track current download for cleanup on cancel
	setCurrentDownload(outputPath)
	defer setCurrentDownload("")

	pw := &ProgressWriter{
		Total:    resp.ContentLength,
		Filename: filepath.Base(outputPath),
	}

	size, err := io.Copy(out, io.TeeReader(resp.Body, pw))
	out.Close()
	fmt.Println() // newline after progress bar

	if err != nil {
		os.Remove(outputPath)
		return "", 0, err
	}

	return outputPath, size, nil
}

// Active download tracking
type ActiveDownload struct {
	ID         string             `json:"id"`
	URL        string             `json:"url"`
	Filename   string             `json:"filename"`
	TargetPath string             `json:"target_path"`
	State      string             `json:"state"`
	Order      int                `json:"order"`
	Progress   int64              `json:"progress"`
	Total      int64              `json:"total"`
	Speed      int64              `json:"speed"` // bytes per second
	StartedAt  time.Time          `json:"started_at"`
	OutputPath string             `json:"-"`
	Context    context.Context    `json:"-"`
	CancelFunc context.CancelFunc `json:"-"`
}

const (
	defaultMaxConcurrentDownloads  = 5
	defaultSeriesSearchConcurrency = 5
	maxSeriesSearchConcurrency     = 20
)

// Web server state
type WebDownloader struct {
	outputDir   string
	historyFile string
	history     *History
	historyMu   sync.RWMutex

	downloads   map[string]*ActiveDownload
	queue       []string
	running     int
	maxRunning  int
	downloadsMu sync.RWMutex
	nextID      int
}

func (wd *WebDownloader) getActiveDownloads() []ActiveDownload {
	wd.downloadsMu.RLock()
	defer wd.downloadsMu.RUnlock()

	result := make([]ActiveDownload, 0, len(wd.downloads))
	for _, d := range wd.downloads {
		result = append(result, *d)
	}
	// Sort by queue order (oldest first - keeps stable order)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Order < result[j].Order
	})
	return result
}

func (wd *WebDownloader) updateProgress(id string, progress, total, speed int64) {
	wd.downloadsMu.Lock()
	if d, ok := wd.downloads[id]; ok {
		d.Progress = progress
		d.Total = total
		d.Speed = speed
	}
	wd.downloadsMu.Unlock()
}

type WebProgressWriter struct {
	wd           *WebDownloader
	downloadID   string
	Total        int64
	Downloaded   int64
	LastUpdate   time.Time
	LastBytes    int64
	CurrentSpeed int64
}

func (wpw *WebProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	wpw.Downloaded += int64(n)

	now := time.Now()
	elapsed := now.Sub(wpw.LastUpdate)
	if elapsed >= 500*time.Millisecond {
		bytesDelta := wpw.Downloaded - wpw.LastBytes
		wpw.CurrentSpeed = int64(float64(bytesDelta) / elapsed.Seconds())
		wpw.LastUpdate = now
		wpw.LastBytes = wpw.Downloaded
	}

	wpw.wd.updateProgress(wpw.downloadID, wpw.Downloaded, wpw.Total, wpw.CurrentSpeed)
	return n, nil
}

func (wd *WebDownloader) downloadFile(ctx context.Context, downloadID, rawURL, targetPath string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("bad status: %s", resp.Status)
	}

	filename := filepath.Base(targetPath)
	outputPath, err := resolveOutputPath(wd.outputDir, targetPath)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return "", 0, err
	}

	if _, err := os.Stat(outputPath); err == nil {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		outputPath = filepath.Join(filepath.Dir(outputPath), fmt.Sprintf("%s_%s%s", base, urlHash(rawURL), ext))
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return "", 0, err
	}

	// Track output path for cleanup
	wd.downloadsMu.Lock()
	if d, ok := wd.downloads[downloadID]; ok {
		d.OutputPath = outputPath
		d.Filename = filepath.Base(outputPath)
		base, baseErr := filepath.Abs(wd.outputDir)
		if rel, err := filepath.Rel(base, outputPath); baseErr == nil && err == nil {
			d.TargetPath = rel
		}
	}
	wd.downloadsMu.Unlock()

	wpw := &WebProgressWriter{
		wd:         wd,
		downloadID: downloadID,
		Total:      resp.ContentLength,
		LastUpdate: time.Now(),
	}
	wd.updateProgress(downloadID, 0, resp.ContentLength, 0)

	size, err := io.Copy(out, io.TeeReader(resp.Body, wpw))
	out.Close()

	if err != nil {
		os.Remove(outputPath)
		return "", 0, err
	}

	return outputPath, size, nil
}

func (wd *WebDownloader) startDownload(rawURL string) (string, error) {
	return wd.startDownloadTo(rawURL, movieTargetPath(filenameFromURL(rawURL)))
}

func (wd *WebDownloader) startDownloadTo(rawURL, targetPath string) (string, error) {
	filename := filepath.Base(targetPath)

	// Check history
	wd.historyMu.RLock()
	_, urlExists := wd.history.Downloads[rawURL]
	_, fileExists := wd.history.DownloadedFiles[targetPath]
	wd.historyMu.RUnlock()

	if urlExists || fileExists {
		return "", fmt.Errorf("already downloaded: %s", filename)
	}

	ctx, cancel := context.WithCancel(context.Background())

	wd.downloadsMu.Lock()
	wd.nextID++
	id := fmt.Sprintf("dl-%d", wd.nextID)
	wd.downloads[id] = &ActiveDownload{
		ID:         id,
		URL:        rawURL,
		Filename:   filename,
		TargetPath: targetPath,
		State:      "queued",
		Order:      wd.nextID,
		StartedAt:  time.Now(),
		Context:    ctx,
		CancelFunc: cancel,
	}
	wd.queue = append(wd.queue, id)
	wd.downloadsMu.Unlock()

	wd.startQueuedDownloads()

	return id, nil
}

func (wd *WebDownloader) startQueuedDownloads() {
	for {
		wd.downloadsMu.Lock()
		if wd.running >= wd.maxRunning {
			wd.downloadsMu.Unlock()
			return
		}

		var d *ActiveDownload
		for len(wd.queue) > 0 {
			id := wd.queue[0]
			wd.queue = wd.queue[1:]
			candidate, ok := wd.downloads[id]
			if ok && candidate.State == "queued" {
				d = candidate
				break
			}
		}
		if d == nil {
			wd.downloadsMu.Unlock()
			return
		}

		d.State = "downloading"
		d.StartedAt = time.Now()
		wd.running++
		id := d.ID
		rawURL := d.URL
		targetPath := d.TargetPath
		ctx := d.Context
		wd.downloadsMu.Unlock()

		go wd.runDownload(ctx, id, rawURL, targetPath)
	}
}

func (wd *WebDownloader) runDownload(ctx context.Context, id, rawURL, targetPath string) {
	defer func() {
		wd.downloadsMu.Lock()
		if d, ok := wd.downloads[id]; ok && d.State == "downloading" {
			wd.running--
		}
		delete(wd.downloads, id)
		wd.downloadsMu.Unlock()
		wd.startQueuedDownloads()
	}()

	outputPath, size, err := wd.downloadFile(ctx, id, rawURL, targetPath)
	if err != nil {
		return
	}

	wd.historyMu.Lock()
	wd.history.Downloads[rawURL] = DownloadRecord{
		URL:        rawURL,
		Filename:   outputPath,
		Downloaded: time.Now(),
		Size:       size,
	}
	wd.history.DownloadedFiles[targetPath] = rawURL
	saveHistory(wd.historyFile, wd.history)
	wd.historyMu.Unlock()
}

func (wd *WebDownloader) cancelDownload(id string) {
	wd.downloadsMu.Lock()
	d, ok := wd.downloads[id]
	if ok {
		d.CancelFunc()
		// Cleanup partial file
		if d.OutputPath != "" {
			os.Remove(d.OutputPath)
		}
		if d.State == "queued" {
			delete(wd.downloads, id)
		}
	}
	wd.downloadsMu.Unlock()
}

func (wd *WebDownloader) getHistory() []DownloadRecord {
	wd.historyMu.RLock()
	defer wd.historyMu.RUnlock()

	records := make([]DownloadRecord, 0, len(wd.history.Downloads))
	for _, r := range wd.history.Downloads {
		records = append(records, r)
	}
	// Sort by download time (newest first)
	sort.Slice(records, func(i, j int) bool {
		return records[i].Downloaded.After(records[j].Downloaded)
	})
	return records
}

const (
	tvmazeBaseURL   = "https://api.tvmaze.com"
	webshareBaseURL = "https://webshare.cz/api"
	tmdbBaseURL     = "https://api.themoviedb.org/3"
)

type SeriesSearchRequest struct {
	Query            string  `json:"query"`
	ShowID           int     `json:"show_id"`
	SearchLimit      int     `json:"search_limit"`
	Candidates       int     `json:"candidates"`
	Concurrency      int     `json:"concurrency"`
	MinScore         float64 `json:"min_score"`
	PreferredQuality string  `json:"preferred_quality"`
}

type MovieSearchRequest struct {
	Query            string  `json:"query"`
	Year             string  `json:"year"`
	SearchLimit      int     `json:"search_limit"`
	Candidates       int     `json:"candidates"`
	MinScore         float64 `json:"min_score"`
	PreferredQuality string  `json:"preferred_quality"`
}

type SeriesDownloadRequest struct {
	URLs  []string             `json:"urls"`
	Items []SeriesDownloadItem `json:"items"`
}

type MovieDownloadRequest struct {
	Items []MovieDownloadItem `json:"items"`
}

type SeriesDownloadItem struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ShowName    string `json:"show_name"`
	ShowYear    string `json:"show_year"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	EpisodeName string `json:"episode_name"`
}

type MovieDownloadItem struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Title    string `json:"title"`
	Year     string `json:"year"`
}

type TVMazeClient struct {
	baseURL    string
	httpClient *http.Client
}

type TMDbClient struct {
	baseURL     string
	httpClient  *http.Client
	apiKey      string
	accessToken string
}

type tmdbMovieSearchResponse struct {
	Results []TMDbMovie `json:"results"`
}

type TMDbMovie struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	ReleaseDate   string  `json:"release_date"`
	Overview      string  `json:"overview"`
	Popularity    float64 `json:"popularity"`
	VoteAverage   float64 `json:"vote_average"`
}

type TVSearchResult struct {
	Score float64 `json:"score"`
	Show  Show    `json:"show"`
}

type Show struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Premiered string `json:"premiered"`
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

type SeriesMatch struct {
	ShowID            int               `json:"show_id"`
	ShowName          string            `json:"show_name"`
	ShowYear          string            `json:"show_year,omitempty"`
	Code              string            `json:"code"`
	Season            int               `json:"season"`
	Episode           int               `json:"episode"`
	EpisodeName       string            `json:"episode_name"`
	EpisodeAirdate    string            `json:"episode_airdate"`
	WebshareIdent     string            `json:"webshare_ident,omitempty"`
	WebshareName      string            `json:"webshare_name,omitempty"`
	WebshareSize      int64             `json:"webshare_size,omitempty"`
	WebshareSizeHuman string            `json:"webshare_size_human,omitempty"`
	Score             float64           `json:"score"`
	URL               string            `json:"url,omitempty"`
	Error             string            `json:"error,omitempty"`
	Candidates        []SeriesCandidate `json:"candidates,omitempty"`
}

type MovieMatch struct {
	MovieID           int               `json:"movie_id"`
	Title             string            `json:"title"`
	OriginalTitle     string            `json:"original_title,omitempty"`
	Year              string            `json:"year,omitempty"`
	ReleaseDate       string            `json:"release_date,omitempty"`
	Overview          string            `json:"overview,omitempty"`
	WebshareIdent     string            `json:"webshare_ident,omitempty"`
	WebshareName      string            `json:"webshare_name,omitempty"`
	WebshareSize      int64             `json:"webshare_size,omitempty"`
	WebshareSizeHuman string            `json:"webshare_size_human,omitempty"`
	Score             float64           `json:"score"`
	URL               string            `json:"url,omitempty"`
	Error             string            `json:"error,omitempty"`
	Candidates        []SeriesCandidate `json:"candidates,omitempty"`
}

type SeriesCandidate struct {
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

type WebshareStatus struct {
	Configured      bool   `json:"configured"`
	Authenticated   bool   `json:"authenticated"`
	Mode            string `json:"mode"`
	UsernamePresent bool   `json:"username_present"`
	PasswordPresent bool   `json:"password_present"`
	TokenPresent    bool   `json:"token_present"`
	Message         string `json:"message"`
	Error           string `json:"error,omitempty"`
}

type TMDbStatus struct {
	Configured         bool   `json:"configured"`
	Authenticated      bool   `json:"authenticated"`
	Mode               string `json:"mode"`
	AccessTokenPresent bool   `json:"access_token_present"`
	APIKeyPresent      bool   `json:"api_key_present"`
	Message            string `json:"message"`
	Error              string `json:"error,omitempty"`
}

func checkWebshareStatus(ctx context.Context) WebshareStatus {
	username := strings.TrimSpace(os.Getenv("WEBSHARE_USERNAME"))
	password := os.Getenv("WEBSHARE_PASSWORD")
	token := strings.TrimSpace(os.Getenv("WEBSHARE_WST"))
	status := WebshareStatus{
		UsernamePresent: username != "",
		PasswordPresent: password != "",
		TokenPresent:    token != "",
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	ws := WebshareClient{
		baseURL:    webshareBaseURL,
		httpClient: httpClient,
		token:      token,
	}

	switch {
	case token != "":
		status.Configured = true
		status.Mode = "session token"
		if _, err := ws.Search(ctx, "test", 0, 1); err != nil {
			status.Message = "Webshare token is configured, but validation failed."
			status.Error = err.Error()
			return status
		}
		status.Authenticated = true
		status.Message = "Webshare session token is configured and accepted."
		return status
	case username != "" || password != "":
		status.Configured = true
		status.Mode = "username/password"
		if username == "" {
			status.Message = "WEBSHARE_PASSWORD is set, but WEBSHARE_USERNAME is missing."
			status.Error = "WEBSHARE_USERNAME is empty"
			return status
		}
		if password == "" {
			status.Message = "WEBSHARE_USERNAME is set, but WEBSHARE_PASSWORD is missing."
			status.Error = "WEBSHARE_PASSWORD is empty"
			return status
		}
		if _, err := ws.Login(ctx, username, password, false); err != nil {
			status.Message = "Webshare username/password are configured, but login failed."
			status.Error = err.Error()
			return status
		}
		status.Authenticated = true
		status.Message = "Webshare username/password are configured and login works."
		return status
	default:
		status.Mode = "anonymous"
		status.Message = "Webshare credentials are not configured. Searches run anonymously and downloads may be slow."
		return status
	}
}

func checkTMDbStatus(ctx context.Context) TMDbStatus {
	apiKey := strings.TrimSpace(os.Getenv("TMDB_API_KEY"))
	accessToken := strings.TrimSpace(os.Getenv("TMDB_ACCESS_TOKEN"))
	status := TMDbStatus{
		AccessTokenPresent: accessToken != "",
		APIKeyPresent:      apiKey != "",
	}
	if accessToken == "" && apiKey == "" {
		status.Mode = "not configured"
		status.Message = "TMDb credentials are not configured. Movie Search needs TMDB_ACCESS_TOKEN or TMDB_API_KEY."
		return status
	}
	status.Configured = true
	if accessToken != "" {
		status.Mode = "access token"
	} else {
		status.Mode = "api key"
	}
	client := TMDbClient{
		baseURL:     tmdbBaseURL,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		apiKey:      apiKey,
		accessToken: accessToken,
	}
	if _, err := client.SearchMovie(ctx, "Forrest Gump", "1994"); err != nil {
		status.Message = "TMDb credentials are configured, but validation failed."
		status.Error = err.Error()
		return status
	}
	status.Authenticated = true
	status.Message = "TMDb credentials are configured and movie lookup works."
	return status
}

func webshareClientFromEnv(ctx context.Context, httpClient *http.Client) (WebshareClient, error) {
	ws := WebshareClient{
		baseURL:    webshareBaseURL,
		httpClient: httpClient,
		token:      os.Getenv("WEBSHARE_WST"),
	}
	if ws.token == "" && os.Getenv("WEBSHARE_USERNAME") != "" {
		token, err := ws.Login(ctx, os.Getenv("WEBSHARE_USERNAME"), os.Getenv("WEBSHARE_PASSWORD"), false)
		if err != nil {
			return ws, err
		}
		ws.token = token
	}
	return ws, nil
}

func (wd *WebDownloader) searchSeries(ctx context.Context, req SeriesSearchRequest) ([]SeriesMatch, error) {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" && req.ShowID == 0 {
		return nil, errors.New("query or show_id is required")
	}
	if req.SearchLimit <= 0 {
		req.SearchLimit = 8
	}
	if req.Candidates <= 0 {
		req.Candidates = 5
	}
	if req.MinScore <= 0 {
		req.MinScore = 0.35
	}

	httpClient := &http.Client{Timeout: 25 * time.Second}
	tv := TVMazeClient{baseURL: tvmazeBaseURL, httpClient: httpClient}
	ws, err := webshareClientFromEnv(ctx, httpClient)
	if err != nil {
		return nil, err
	}

	show, episodes, err := loadSeriesEpisodes(ctx, tv, req)
	if err != nil {
		return nil, err
	}

	concurrency := configuredSeriesSearchConcurrency(req.Concurrency)
	if concurrency > len(episodes) && len(episodes) > 0 {
		concurrency = len(episodes)
	}

	matches := make([]SeriesMatch, len(episodes))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				matches[idx] = linkSeriesEpisode(ctx, ws, show, episodes[idx], req)
			}
		}()
	}
	for idx := range episodes {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		case jobs <- idx:
		}
	}
	close(jobs)
	wg.Wait()
	return matches, nil
}

func (wd *WebDownloader) searchMovie(ctx context.Context, req MovieSearchRequest) (MovieMatch, error) {
	req.Query = strings.TrimSpace(req.Query)
	req.Year = strings.TrimSpace(req.Year)
	if req.Query == "" {
		return MovieMatch{}, errors.New("query is required")
	}
	if req.SearchLimit <= 0 {
		req.SearchLimit = 8
	}
	if req.Candidates <= 0 {
		req.Candidates = 5
	}
	if req.MinScore <= 0 {
		req.MinScore = 0.25
	}

	httpClient := &http.Client{Timeout: 25 * time.Second}
	tmdb := TMDbClient{
		baseURL:     tmdbBaseURL,
		httpClient:  httpClient,
		apiKey:      strings.TrimSpace(os.Getenv("TMDB_API_KEY")),
		accessToken: strings.TrimSpace(os.Getenv("TMDB_ACCESS_TOKEN")),
	}
	ws, err := webshareClientFromEnv(ctx, httpClient)
	if err != nil {
		return MovieMatch{}, err
	}

	movie, err := tmdb.SearchMovie(ctx, req.Query, req.Year)
	if err != nil {
		return MovieMatch{}, err
	}
	return linkMovie(ctx, ws, movie, req), nil
}

func loadSeriesEpisodes(ctx context.Context, tv TVMazeClient, req SeriesSearchRequest) (Show, []Episode, error) {
	var show Show
	if req.ShowID != 0 {
		found, err := tv.Show(ctx, req.ShowID)
		if err != nil {
			return show, nil, err
		}
		show = found
	} else {
		results, err := tv.Search(ctx, req.Query)
		if err != nil {
			return show, nil, err
		}
		if len(results) == 0 {
			return show, nil, fmt.Errorf("no TVmaze show found for %q", req.Query)
		}
		show = results[0].Show
	}
	episodes, err := tv.Episodes(ctx, show.ID)
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

func linkMovie(ctx context.Context, ws WebshareClient, movie TMDbMovie, req MovieSearchRequest) MovieMatch {
	year := yearFromDate(movie.ReleaseDate)
	match := MovieMatch{
		MovieID:       movie.ID,
		Title:         movie.Title,
		OriginalTitle: movie.OriginalTitle,
		Year:          year,
		ReleaseDate:   movie.ReleaseDate,
		Overview:      movie.Overview,
	}

	candidatesByIdent := map[string]SeriesCandidate{}
	for _, query := range movieQueries(movie, req.PreferredQuality) {
		response, err := ws.Search(ctx, query, 0, req.SearchLimit)
		if err != nil {
			if match.Error == "" {
				match.Error = err.Error()
			}
			continue
		}
		for _, file := range response.Files {
			if file.Password != 0 {
				continue
			}
			candidate := SeriesCandidate{
				Ident:     file.Ident,
				Name:      file.Name,
				Type:      file.Type,
				Size:      file.Size,
				SizeHuman: formatBytes(file.Size),
				Query:     query,
				Score:     scoreMovieCandidate(movie, file, req.PreferredQuality),
			}
			if previous, ok := candidatesByIdent[file.Ident]; !ok || candidate.Score > previous.Score {
				candidatesByIdent[file.Ident] = candidate
			}
		}
	}

	candidates := make([]SeriesCandidate, 0, len(candidatesByIdent))
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

	keep := min(req.Candidates, len(candidates))
	match.Candidates = candidates[:keep]
	for i := 0; i < keep; i++ {
		if candidates[i].Score < req.MinScore {
			continue
		}
		link, err := ws.FileLink(ctx, candidates[i].Ident)
		if err != nil {
			match.Candidates[i].Error = err.Error()
			continue
		}
		match.Candidates[i].URL = link
		if match.URL == "" {
			match.WebshareIdent = candidates[i].Ident
			match.WebshareName = candidates[i].Name
			match.WebshareSize = candidates[i].Size
			match.WebshareSizeHuman = candidates[i].SizeHuman
			match.Score = candidates[i].Score
			match.URL = link
			match.Error = ""
		}
	}
	if match.URL == "" {
		match.WebshareIdent = candidates[0].Ident
		match.WebshareName = candidates[0].Name
		match.WebshareSize = candidates[0].Size
		match.WebshareSizeHuman = candidates[0].SizeHuman
		match.Score = candidates[0].Score
		match.Error = fmt.Sprintf("no candidate above min score %.2f with generated URL", req.MinScore)
	}
	return match
}

func linkSeriesEpisode(ctx context.Context, ws WebshareClient, show Show, episode Episode, req SeriesSearchRequest) SeriesMatch {
	match := SeriesMatch{
		ShowID:         show.ID,
		ShowName:       show.Name,
		ShowYear:       yearFromDate(show.Premiered),
		Code:           episodeCode(episode),
		Season:         episode.Season,
		Episode:        episode.Number,
		EpisodeName:    episode.Name,
		EpisodeAirdate: episode.Airdate,
	}

	candidatesByIdent := map[string]SeriesCandidate{}
	for _, query := range episodeQueries(show.Name, episode, req.PreferredQuality) {
		response, err := ws.Search(ctx, query, 0, req.SearchLimit)
		if err != nil {
			if match.Error == "" {
				match.Error = err.Error()
			}
			continue
		}
		for _, file := range response.Files {
			if file.Password != 0 {
				continue
			}
			candidate := SeriesCandidate{
				Ident:     file.Ident,
				Name:      file.Name,
				Type:      file.Type,
				Size:      file.Size,
				SizeHuman: formatBytes(file.Size),
				Query:     query,
				Score:     scoreSeriesCandidate(show.Name, episode, file, req.PreferredQuality),
			}
			if previous, ok := candidatesByIdent[file.Ident]; !ok || candidate.Score > previous.Score {
				candidatesByIdent[file.Ident] = candidate
			}
		}
	}

	candidates := make([]SeriesCandidate, 0, len(candidatesByIdent))
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

	keep := min(req.Candidates, len(candidates))
	match.Candidates = candidates[:keep]
	for i := 0; i < keep; i++ {
		if candidates[i].Score < req.MinScore {
			continue
		}
		link, err := ws.FileLink(ctx, candidates[i].Ident)
		if err != nil {
			match.Candidates[i].Error = err.Error()
			continue
		}
		match.Candidates[i].URL = link
		if match.URL == "" {
			match.WebshareIdent = candidates[i].Ident
			match.WebshareName = candidates[i].Name
			match.WebshareSize = candidates[i].Size
			match.WebshareSizeHuman = candidates[i].SizeHuman
			match.Score = candidates[i].Score
			match.URL = link
			match.Error = ""
		}
	}
	if match.URL == "" {
		match.WebshareIdent = candidates[0].Ident
		match.WebshareName = candidates[0].Name
		match.WebshareSize = candidates[0].Size
		match.WebshareSizeHuman = candidates[0].SizeHuman
		match.Score = candidates[0].Score
		match.Error = fmt.Sprintf("no candidate above min score %.2f with generated URL", req.MinScore)
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

func (c TVMazeClient) Episodes(ctx context.Context, showID int) ([]Episode, error) {
	var episodes []Episode
	err := c.getJSON(ctx, fmt.Sprintf("/shows/%d/episodes", showID), &episodes)
	return episodes, err
}

func (c TMDbClient) SearchMovie(ctx context.Context, query, year string) (TMDbMovie, error) {
	values := url.Values{}
	values.Set("query", query)
	values.Set("include_adult", "false")
	values.Set("language", "en-US")
	if year != "" {
		values.Set("year", year)
	}
	var response tmdbMovieSearchResponse
	if err := c.getJSON(ctx, "/search/movie?"+values.Encode(), &response); err != nil {
		return TMDbMovie{}, err
	}
	if len(response.Results) == 0 {
		return TMDbMovie{}, fmt.Errorf("no TMDb movie found for %q", query)
	}
	return response.Results[0], nil
}

func (c TMDbClient) getJSON(ctx context.Context, path string, target any) error {
	if c.accessToken == "" && c.apiKey == "" {
		return errors.New("TMDb is not configured: set TMDB_ACCESS_TOKEN or TMDB_API_KEY")
	}
	fullURL := c.baseURL + path
	if c.accessToken == "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		fullURL += sep + "api_key=" + url.QueryEscape(c.apiKey)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "umbrel-file-downloader/movie")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("tmdb request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (c TVMazeClient) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "umbrel-file-downloader/series")
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
	if password == "" {
		return "", errors.New("WEBSHARE_PASSWORD is empty")
	}
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
	req.Header.Set("User-Agent", "umbrel-file-downloader/series")
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

func episodeQueries(showName string, episode Episode, quality string) []string {
	code := episodeCode(episode)
	plainShow := asciiFold(showName)
	plainEpisode := asciiFold(episode.Name)
	base := []string{
		fmt.Sprintf("%s %s", showName, code),
		fmt.Sprintf("%s %s %s", showName, code, episode.Name),
		fmt.Sprintf("%s %s", plainShow, code),
		fmt.Sprintf("%s %s %s", plainShow, code, plainEpisode),
		fmt.Sprintf("%s %dx%02d", showName, episode.Season, episode.Number),
		fmt.Sprintf("%s %dx%02d", plainShow, episode.Season, episode.Number),
	}
	if strings.TrimSpace(quality) != "" {
		q := strings.TrimSpace(quality)
		withQuality := make([]string, 0, len(base))
		for _, query := range base {
			withQuality = append(withQuality, query+" "+q)
		}
		base = append(withQuality, base...)
	}
	return uniqueStrings(base)
}

func movieQueries(movie TMDbMovie, quality string) []string {
	year := yearFromDate(movie.ReleaseDate)
	titles := []string{movie.Title}
	if movie.OriginalTitle != "" && normalize(movie.OriginalTitle) != normalize(movie.Title) {
		titles = append(titles, movie.OriginalTitle)
	}
	base := []string{}
	for _, title := range titles {
		if year != "" {
			base = append(base, fmt.Sprintf("%s %s", title, year))
		}
		base = append(base, title)
		if folded := asciiFold(title); folded != title {
			if year != "" {
				base = append(base, fmt.Sprintf("%s %s", folded, year))
			}
			base = append(base, folded)
		}
	}
	if strings.TrimSpace(quality) != "" {
		q := strings.TrimSpace(quality)
		withQuality := make([]string, 0, len(base))
		for _, query := range base {
			withQuality = append(withQuality, query+" "+q)
		}
		base = append(withQuality, base...)
	}
	return uniqueStrings(base)
}

func scoreSeriesCandidate(showName string, episode Episode, file WebshareFile, quality string) float64 {
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
	if strings.TrimSpace(quality) != "" && strings.Contains(name, normalize(quality)) {
		score += 0.08
	}
	if file.Password != 0 {
		score -= 0.25
	}
	score += math.Min(float64(file.PositiveVotes), 10) * 0.005
	score -= math.Min(float64(file.NegativeVotes), 10) * 0.01
	return math.Round(score*100) / 100
}

func scoreMovieCandidate(movie TMDbMovie, file WebshareFile, quality string) float64 {
	name := normalize(file.Name)
	title := normalize(movie.Title)
	originalTitle := normalize(movie.OriginalTitle)
	year := yearFromDate(movie.ReleaseDate)
	score := 0.0
	score += 0.45 * tokenOverlap(title, name)
	if originalTitle != "" && originalTitle != title {
		score += 0.25 * tokenOverlap(originalTitle, name)
	}
	if year != "" && strings.Contains(name, year) {
		score += 0.18
	}
	if isVideoType(file.Type, file.Name) {
		score += 0.1
	}
	if file.Size >= 600*1024*1024 {
		score += 0.08
	} else if file.Size > 0 && file.Size < 100*1024*1024 {
		score -= 0.2
	}
	if strings.TrimSpace(quality) != "" && strings.Contains(name, normalize(quality)) {
		score += 0.08
	}
	if file.Password != 0 {
		score -= 0.25
	}
	score += math.Min(float64(file.PositiveVotes), 10) * 0.005
	score -= math.Min(float64(file.NegativeVotes), 10) * 0.01
	return math.Round(score*100) / 100
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

const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>Downloader</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
        * { box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", system-ui, sans-serif; max-width: 1180px; margin: 0 auto; padding: 24px; background: #0b0f17; color: #f5f7fb; }
        h1, h2 { color: #f5f7fb; letter-spacing: 0; }
        h1 { margin: 0 0 16px; font-size: 30px; font-weight: 750; }
        h2 { border-bottom: 1px solid rgba(148, 163, 184, 0.2); padding-bottom: 10px; margin: 24px 0 15px; font-size: 20px; }
        input, select { padding: 13px 14px; border: 1px solid rgba(148, 163, 184, 0.24); border-radius: 8px; background: rgba(15, 23, 42, 0.78); color: #f5f7fb; font-size: 15px; min-width: 0; outline: none; }
        input:focus, select:focus { border-color: #60a5fa; box-shadow: 0 0 0 3px rgba(96, 165, 250, 0.18); }
        input[type="text"] { width: 100%; }
        label { display: block; font-size: 13px; font-weight: 650; color: #d7dde8; margin-bottom: 7px; }
        button { padding: 13px 18px; border: none; border-radius: 8px; cursor: pointer; font-size: 15px; font-weight: 700; white-space: nowrap; transition: transform 0.12s, background 0.15s, opacity 0.15s; }
        button:hover { transform: translateY(-1px); }
        button:disabled { opacity: 0.55; cursor: not-allowed; }
        table { width: 100%; border-collapse: collapse; }
        th, td { padding: 10px; border-bottom: 1px solid #30384a; text-align: left; vertical-align: top; }
        th { color: #9ca3af; font-size: 12px; text-transform: uppercase; }
        .tabs { display: inline-flex; gap: 4px; margin: 4px 0 20px; padding: 4px; background: rgba(15, 23, 42, 0.75); border: 1px solid rgba(148, 163, 184, 0.18); border-radius: 10px; }
        .tab { background: transparent; color: #9ca3af; border-radius: 7px; padding: 10px 14px; }
        .tab.active { background: #f5f7fb; color: #0b0f17; }
        .tab-panel { display: none; }
        .tab-panel.active { display: block; }
        .row { display: grid; grid-template-columns: 1fr auto; gap: 10px; margin-bottom: 14px; align-items: center; }
        .series-form { background: rgba(17, 24, 39, 0.82); border: 1px solid rgba(148, 163, 184, 0.18); border-radius: 10px; padding: 18px; margin-bottom: 14px; box-shadow: 0 18px 50px rgba(0,0,0,0.24); }
        .series-controls { display: grid; grid-template-columns: minmax(280px, 1fr) 150px auto; gap: 12px; align-items: end; }
        .movie-controls { display: grid; grid-template-columns: minmax(260px, 1fr) 110px 150px auto; gap: 12px; align-items: end; }
        .advanced { margin-top: 14px; border-top: 1px solid rgba(148, 163, 184, 0.14); padding-top: 12px; }
        .advanced summary { cursor: pointer; color: #9ca3af; font-size: 13px; font-weight: 650; width: fit-content; }
        .advanced-grid { display: grid; grid-template-columns: repeat(3, minmax(150px, 1fr)); gap: 12px; margin-top: 12px; }
        .field-help { margin-top: 6px; font-size: 12px; color: #9ca3af; line-height: 1.35; }
        .btn-primary { background: #f5f7fb; color: #0b0f17; box-shadow: 0 10px 24px rgba(245, 247, 251, 0.12); }
        .btn-primary:hover { background: #ffffff; }
        .btn-secondary { background: #1f2937; color: #eef2f7; border: 1px solid rgba(148, 163, 184, 0.18); }
        .btn-download { background: #34d399; color: #04130d; padding: 14px 20px; box-shadow: 0 12px 28px rgba(52, 211, 153, 0.18); }
        .btn-download:hover { background: #6ee7b7; }
        .btn-danger { background: #ef4444; color: #fff; padding: 8px 14px; font-size: 14px; }
        .btn-danger:hover { background: #dc2626; }
        .download-item, .history-item, .series-results { background: rgba(17, 24, 39, 0.82); border: 1px solid rgba(148, 163, 184, 0.14); border-radius: 10px; padding: 15px; margin-bottom: 10px; }
        .download-header { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 8px; }
        .download-filename { font-weight: bold; color: #38bdf8; word-break: break-all; }
        .progress-bar { height: 18px; background: #0f172a; border-radius: 10px; overflow: hidden; margin: 8px 0; }
        .progress-fill { height: 100%; background: linear-gradient(90deg, #38bdf8, #22c55e); transition: width 0.3s; }
        .progress-text, .muted { font-size: 13px; color: #9ca3af; }
        .history-item .name { font-weight: bold; color: #22c55e; }
        .history-item .size { color: #9ca3af; font-size: 14px; }
        .history-item .date { color: #6b7280; font-size: 12px; }
        .empty { color: #6b7280; font-style: italic; }
        .episode-title { min-width: 190px; }
        .candidate-select { width: 100%; max-width: 460px; }
        .score { color: #fbbf24; font-variant-numeric: tabular-nums; }
        .error { color: #f87171; }
        .status { margin: 10px 0; color: #9ca3af; }
        .loading-box { display: none; background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 16px; margin: 14px 0; }
        .loading-box.active { display: flex; align-items: center; gap: 14px; }
        .spinner { width: 28px; height: 28px; border: 3px solid #334155; border-top-color: #38bdf8; border-radius: 50%; animation: spin 0.9s linear infinite; flex: 0 0 auto; }
        .loading-title { font-weight: 700; color: #eef2f7; margin-bottom: 3px; }
        .loading-detail { color: #9ca3af; font-size: 13px; }
        .season-tools { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 14px; }
        .season-tool { display: inline-flex; align-items: center; gap: 6px; background: #111827; border: 1px solid #334155; border-radius: 6px; padding: 6px; }
        .season-label { color: #d1d5db; font-size: 13px; font-weight: 700; padding: 0 4px; }
        .btn-small { padding: 7px 9px; font-size: 12px; border-radius: 5px; background: #334155; color: #eef2f7; }
        .selection-count { color: #9ca3af; font-size: 13px; margin-left: auto; }
        .auth-status { display: none; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; background: #0f172a; }
        .auth-status.visible { display: block; }
        .auth-status.ok { display: none; border-color: #166534; background: #052e1a; }
        .auth-status.warn { border-color: #92400e; background: #2a1705; }
        .auth-status.error { border-color: #991b1b; background: #2a0b0b; }
        .auth-title { font-weight: 700; margin-bottom: 4px; }
        .auth-detail { color: #d1d5db; font-size: 13px; line-height: 1.35; }
        @keyframes spin { to { transform: rotate(360deg); } }
        @media (max-width: 820px) {
            body { padding: 14px; }
            .row, .series-controls, .movie-controls, .advanced-grid { grid-template-columns: 1fr; }
            table, thead, tbody, tr, th, td { display: block; }
            thead { display: none; }
            td { border-bottom: none; padding: 6px 0; }
            tr { border-bottom: 1px solid #30384a; padding: 10px 0; }
        }
    </style>
</head>
<body>
    <h1>Downloader</h1>

    <div class="tabs">
        <button class="tab active" onclick="showTab('url-tab', this)">URL Download</button>
        <button class="tab" onclick="showTab('movie-tab', this)">Movie Search</button>
        <button class="tab" onclick="showTab('series-tab', this)">Series Search</button>
    </div>

    <section id="url-tab" class="tab-panel active">
        <div class="row">
            <input type="text" id="url" placeholder="Enter URL to download..." onkeypress="if(event.key==='Enter')startDownload()">
            <button class="btn-primary" onclick="startDownload()">Download</button>
        </div>
    </section>

    <section id="movie-tab" class="tab-panel">
        <div class="series-form">
            <div class="movie-controls">
                <div>
                    <label for="movie-query">Movie title</label>
                    <input type="text" id="movie-query" placeholder="Forrest Gump" onkeypress="if(event.key==='Enter')searchMovie()">
                </div>
                <div>
                    <label for="movie-year">Year</label>
                    <input type="text" id="movie-year" placeholder="optional">
                </div>
                <div>
                    <label for="movie-quality">Quality</label>
                    <select id="movie-quality">
                        <option value="">Any</option>
                        <option value="1080p" selected>1080p</option>
                        <option value="720p">720p</option>
                        <option value="2160p">2160p / 4K</option>
                        <option value="x265">x265</option>
                    </select>
                </div>
                <button id="movie-search-btn" class="btn-primary" onclick="searchMovie()">Search</button>
            </div>
            <details class="advanced">
                <summary>Advanced</summary>
                <div class="advanced-grid">
                    <div>
                        <label for="movie-limit">Search depth</label>
                        <select id="movie-limit">
                            <option value="4">Fast - 4 results</option>
                            <option value="8" selected>Balanced - 8 results</option>
                            <option value="12">Deep - 12 results</option>
                        </select>
                    </div>
                    <div>
                        <label for="movie-candidates">File choices</label>
                        <select id="movie-candidates">
                            <option value="3">Simple - 3 choices</option>
                            <option value="5" selected>Normal - 5 choices</option>
                            <option value="8">Detailed - 8 choices</option>
                        </select>
                    </div>
                </div>
            </details>
        </div>
        <div id="tmdb-auth-status" class="auth-status">
            <div class="auth-title">Checking TMDb configuration...</div>
            <div class="auth-detail">Verifying whether movie metadata lookup is enabled.</div>
        </div>
        <div id="movie-status" class="status"></div>
        <div id="movie-loading" class="loading-box" aria-live="polite">
            <div class="spinner"></div>
            <div>
                <div class="loading-title">Searching movie and files...</div>
                <div id="movie-loading-detail" class="loading-detail">Looking up TMDb metadata, then checking Webshare candidates.</div>
            </div>
        </div>
        <div id="movie-results"></div>
    </section>

    <section id="series-tab" class="tab-panel">
        <div class="series-form">
            <div class="series-controls">
                <div>
                    <label for="series-query">Series title</label>
                    <input type="text" id="series-query" placeholder="Pripady 1 oddeleni" onkeypress="if(event.key==='Enter')searchSeries()">
                </div>
                <div>
                    <label for="series-quality">Quality</label>
                    <select id="series-quality">
                        <option value="">Any</option>
                        <option value="1080p" selected>1080p</option>
                        <option value="720p">720p</option>
                        <option value="2160p">2160p / 4K</option>
                        <option value="x265">x265</option>
                    </select>
                </div>
                <button id="series-search-btn" class="btn-primary" onclick="searchSeries()">Search</button>
            </div>
            <details class="advanced">
                <summary>Advanced</summary>
                <div class="advanced-grid">
                    <div>
                        <label for="series-limit">Search depth</label>
                        <select id="series-limit">
                            <option value="4">Fast - 4 results</option>
                            <option value="8" selected>Balanced - 8 results</option>
                            <option value="12">Deep - 12 results</option>
                        </select>
                    </div>
                    <div>
                        <label for="series-candidates">File choices</label>
                        <select id="series-candidates">
                            <option value="3">Simple - 3 per episode</option>
                            <option value="5" selected>Normal - 5 per episode</option>
                            <option value="8">Detailed - 8 per episode</option>
                        </select>
                    </div>
                    <div>
                        <label for="series-concurrency">Search speed</label>
                        <select id="series-concurrency">
                            <option value="2">Conservative - 2 at once</option>
                            <option value="5" selected>Balanced - 5 at once</option>
                            <option value="10">Fast - 10 at once</option>
                        </select>
                    </div>
                </div>
            </details>
        </div>
        <div id="webshare-auth-status" class="auth-status">
            <div class="auth-title">Checking Webshare login...</div>
            <div class="auth-detail">Verifying whether fast downloads are enabled.</div>
        </div>
        <div class="row">
            <div class="muted">Review the matches, clear anything you do not want, then queue the selected downloads.</div>
            <button class="btn-download" onclick="downloadSelectedSeries()">Download Selected</button>
        </div>
        <div id="series-status" class="status"></div>
        <div id="series-loading" class="loading-box" aria-live="polite">
            <div class="spinner"></div>
            <div>
                <div class="loading-title">Searching episodes and files...</div>
                <div id="series-loading-detail" class="loading-detail">This can take a minute for longer series.</div>
            </div>
        </div>
        <div id="series-results"></div>
    </section>

    <div class="downloads-section" id="downloads-section" style="display:none;">
        <h2>Active Downloads</h2>
        <div id="downloads-list"></div>
    </div>

    <div class="history">
        <h2>Download History</h2>
        <div id="history-list"><p class="empty">No downloads yet</p></div>
    </div>

    <script>
        let polling = false;
        let seriesMatches = [];
        let movieMatch = null;

        function escapeHtml(value) {
            return String(value || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
        }

        function showTab(id, button) {
            document.querySelectorAll('.tab-panel').forEach(el => el.classList.remove('active'));
            document.querySelectorAll('.tab').forEach(el => el.classList.remove('active'));
            document.getElementById(id).classList.add('active');
            button.classList.add('active');
        }

        function formatBytes(bytes) {
            if (!bytes) return '0 B';
            const k = 1024;
            const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
            const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
            return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
        }

        async function startDownloadURL(url) {
            const resp = await fetch('/api/download', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({url})
            });
            if (!resp.ok) throw new Error(await resp.text());
            if (!polling) pollProgress();
        }

        async function startDownload() {
            const input = document.getElementById('url');
            const url = input.value.trim();
            if (!url) return;
            try {
                await startDownloadURL(url);
                input.value = '';
            } catch (err) {
                alert('Failed: ' + err.message);
            }
        }

        async function searchSeries() {
            const query = document.getElementById('series-query').value.trim();
            if (!query) return;
            const status = document.getElementById('series-status');
            const results = document.getElementById('series-results');
            const searchBtn = document.getElementById('series-search-btn');
            status.textContent = 'Searching TVmaze and Webshare...';
            results.innerHTML = '';
            seriesMatches = [];
            setSeriesLoading(true, 'Looking up show metadata, then checking Webshare candidates for multiple episodes in parallel.');

            const payload = {
                query,
                preferred_quality: document.getElementById('series-quality').value.trim(),
                search_limit: parseInt(document.getElementById('series-limit').value, 10) || 8,
                candidates: parseInt(document.getElementById('series-candidates').value, 10) || 5,
                concurrency: parseInt(document.getElementById('series-concurrency').value, 10) || 5,
                min_score: 0.35
            };

            searchBtn.disabled = true;
            try {
                const resp = await fetch('/api/series/search', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(payload)
                });
                if (!resp.ok) {
                    status.textContent = 'Search failed: ' + await resp.text();
                    return;
                }
                seriesMatches = await resp.json();
                const downloadable = seriesMatches.filter(m => m.url).length;
                status.textContent = 'Found ' + seriesMatches.length + ' episodes, ' + downloadable + ' with a selected file.';
                renderSeriesResults();
            } finally {
                searchBtn.disabled = false;
                setSeriesLoading(false);
            }
        }

        async function searchMovie() {
            const query = document.getElementById('movie-query').value.trim();
            if (!query) return;
            const status = document.getElementById('movie-status');
            const results = document.getElementById('movie-results');
            const searchBtn = document.getElementById('movie-search-btn');
            status.textContent = 'Searching TMDb and Webshare...';
            results.innerHTML = '';
            movieMatch = null;
            setMovieLoading(true, 'Looking up TMDb metadata, then checking Webshare candidates.');

            const payload = {
                query,
                year: document.getElementById('movie-year').value.trim(),
                preferred_quality: document.getElementById('movie-quality').value.trim(),
                search_limit: parseInt(document.getElementById('movie-limit').value, 10) || 8,
                candidates: parseInt(document.getElementById('movie-candidates').value, 10) || 5,
                min_score: 0.25
            };

            searchBtn.disabled = true;
            try {
                const resp = await fetch('/api/movie/search', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(payload)
                });
                if (!resp.ok) {
                    status.textContent = 'Search failed: ' + await resp.text();
                    return;
                }
                movieMatch = await resp.json();
                status.textContent = movieMatch.url
                    ? 'Found ' + movieTitle(movieMatch) + ' with a selected file.'
                    : 'Found ' + movieTitle(movieMatch) + ', but no downloadable file matched.';
                renderMovieResults();
            } finally {
                searchBtn.disabled = false;
                setMovieLoading(false);
            }
        }

        function setMovieLoading(active, detail) {
            const box = document.getElementById('movie-loading');
            const detailEl = document.getElementById('movie-loading-detail');
            box.classList.toggle('active', active);
            if (detail) detailEl.textContent = detail;
        }

        function movieTitle(movie) {
            if (!movie) return '';
            return movie.title + (movie.year ? ' (' + movie.year + ')' : '');
        }

        function renderMovieResults() {
            const results = document.getElementById('movie-results');
            if (!movieMatch) {
                results.innerHTML = '<p class="empty">No movie found</p>';
                return;
            }
            const options = (movieMatch.candidates || []).map((c, cidx) => ({...c, cidx})).filter(c => c.url).map(c => {
                const label = c.name + ' - ' + c.size_human + ' - score ' + c.score.toFixed(2);
                return '<option value="' + c.cidx + '">' + escapeHtml(label) + '</option>';
            }).join('');
            const disabled = options ? '' : 'disabled';
            const overview = movieMatch.overview ? '<div class="muted">' + escapeHtml(movieMatch.overview) + '</div>' : '';
            const err = movieMatch.error ? '<div class="error">' + escapeHtml(movieMatch.error) + '</div>' : '';
            results.innerHTML = '<div class="series-results">' +
                '<div class="download-header">' +
                    '<div><strong>' + escapeHtml(movieTitle(movieMatch)) + '</strong>' + overview + err + '</div>' +
                    '<button class="btn-download" onclick="downloadSelectedMovie()" ' + disabled + '>Download Movie</button>' +
                '</div>' +
                '<select id="movie-candidate-select" class="candidate-select" ' + disabled + '>' + options + '</select>' +
            '</div>';
        }

        async function downloadSelectedMovie() {
            if (!movieMatch) return;
            const select = document.getElementById('movie-candidate-select');
            if (!select) return;
            const candidate = (movieMatch.candidates || [])[parseInt(select.value, 10)];
            if (!candidate || !candidate.url) {
                alert('No selected downloadable movie file.');
                return;
            }
            const item = {
                url: candidate.url,
                filename: candidate.name,
                title: movieMatch.title,
                year: movieMatch.year
            };
            const resp = await fetch('/api/movie/download', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({items: [item]})
            });
            if (!resp.ok) {
                alert('Failed: ' + await resp.text());
                return;
            }
            document.getElementById('movie-status').textContent = 'Queued ' + movieTitle(movieMatch) + '.';
            if (!polling) pollProgress();
        }

        function setSeriesLoading(active, detail) {
            const box = document.getElementById('series-loading');
            const detailEl = document.getElementById('series-loading-detail');
            box.classList.toggle('active', active);
            if (detail) detailEl.textContent = detail;
        }

        function renderSeriesResults() {
            const results = document.getElementById('series-results');
            if (!seriesMatches.length) {
                results.innerHTML = '<p class="empty">No episodes found</p>';
                return;
            }
            const rows = seriesMatches.map((m, idx) => {
                const options = (m.candidates || []).map((c, cidx) => ({...c, cidx})).filter(c => c.url).map(c => {
                    const label = c.name + ' - ' + c.size_human + ' - score ' + c.score.toFixed(2);
                    return '<option value="' + c.cidx + '">' + escapeHtml(label) + '</option>';
                }).join('');
                const checked = m.url ? 'checked' : '';
                const disabled = options ? '' : 'disabled';
                const err = m.error ? '<div class="error">' + escapeHtml(m.error) + '</div>' : '';
                return '<tr>' +
                    '<td><input type="checkbox" class="series-check" data-index="' + idx + '" data-season="' + m.season + '" onchange="updateSelectionCount()" ' + checked + ' ' + disabled + '></td>' +
                    '<td class="episode-title"><strong>' + escapeHtml(m.code) + '</strong><br>' + escapeHtml(m.episode_name) + '</td>' +
                    '<td><select class="candidate-select" data-index="' + idx + '" ' + disabled + '>' + options + '</select>' + err + '</td>' +
                    '<td class="score">' + (m.score || 0).toFixed(2) + '</td>' +
                '</tr>';
            }).join('');
            const seasons = [...new Set(seriesMatches.map(m => m.season))].sort((a, b) => a - b);
            const tools = seasons.map(season => {
                const label = 'S' + String(season).padStart(2, '0');
                return '<div class="season-tool">' +
                    '<span class="season-label">' + label + '</span>' +
                    '<button class="btn-small" onclick="setSeasonSelection(' + season + ', true)">Select</button>' +
                    '<button class="btn-small" onclick="setSeasonSelection(' + season + ', false)">Clear</button>' +
                '</div>';
            }).join('');
            results.innerHTML = '<div class="series-results">' +
                '<div class="season-tools">' + tools + '<span id="selection-count" class="selection-count"></span></div>' +
                '<table><thead><tr><th></th><th>Episode</th><th>Selected file</th><th>Score</th></tr></thead><tbody>' + rows + '</tbody></table>' +
            '</div>';
            updateSelectionCount();
        }

        function setSeasonSelection(season, selected) {
            document.querySelectorAll('.series-check[data-season="' + season + '"]').forEach(check => {
                if (!check.disabled) check.checked = selected;
            });
            updateSelectionCount();
        }

        function updateSelectionCount() {
            const count = document.querySelectorAll('.series-check:checked').length;
            const total = document.querySelectorAll('.series-check:not(:disabled)').length;
            const el = document.getElementById('selection-count');
            if (el) el.textContent = count + ' of ' + total + ' selected';
        }

        async function downloadSelectedSeries() {
            const items = [];
            document.querySelectorAll('.series-check:checked').forEach(check => {
                const idx = parseInt(check.dataset.index, 10);
                const match = seriesMatches[idx];
                const select = document.querySelector('.candidate-select[data-index="' + idx + '"]');
                if (!select) return;
                const candidate = (match.candidates || [])[parseInt(select.value, 10)];
                if (candidate && candidate.url) {
                    items.push({
                        url: candidate.url,
                        filename: candidate.name,
                        show_name: match.show_name,
                        show_year: match.show_year,
                        season: match.season,
                        episode: match.episode,
                        episode_name: match.episode_name
                    });
                }
            });
            if (!items.length) {
                alert('No selected downloadable episodes.');
                return;
            }
            const resp = await fetch('/api/series/download', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({items})
            });
            if (!resp.ok) {
                alert('Failed: ' + await resp.text());
                return;
            }
            document.getElementById('series-status').textContent = 'Queued ' + items.length + ' downloads. Up to 5 run at the same time.';
            if (!polling) pollProgress();
        }

        async function cancelDownload(id) {
            await fetch('/api/cancel', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({id})
            });
        }

        async function pollProgress() {
            polling = true;
            const section = document.getElementById('downloads-section');
            const list = document.getElementById('downloads-list');

            const poll = async () => {
                const resp = await fetch('/api/progress');
                const downloads = await resp.json();
                if (downloads.length > 0) {
                    section.style.display = 'block';
                    list.innerHTML = downloads.map(d => {
                        const pct = d.total > 0 ? (d.progress / d.total * 100) : 0;
                        const state = d.state || 'downloading';
                        const progressText = state === 'queued'
                            ? 'Queued - waiting for an open download slot'
                            : pct.toFixed(1) + '% - ' + formatBytes(d.progress) + ' / ' + formatBytes(d.total) + ' - ' + formatBytes(d.speed) + '/s';
                        return '<div class="download-item" id="dl-' + d.id + '">' +
                            '<div class="download-header">' +
                                '<span class="download-filename">' + escapeHtml(d.filename) + '</span>' +
                                '<button class="btn-danger" onclick="cancelDownload(\'' + d.id + '\')">Cancel</button>' +
                            '</div>' +
                            '<div class="progress-bar"><div class="progress-fill" style="width:' + pct + '%"></div></div>' +
                            '<div class="progress-text">' + escapeHtml(progressText) + '</div>' +
                        '</div>';
                    }).join('');
                    setTimeout(poll, 500);
                } else {
                    section.style.display = 'none';
                    list.innerHTML = '';
                    polling = false;
                    loadHistory();
                }
            };
            poll();
        }

        async function loadHistory() {
            const resp = await fetch('/api/history');
            const data = await resp.json();
            const list = document.getElementById('history-list');
            if (data.length === 0) {
                list.innerHTML = '<p class="empty">No downloads yet</p>';
                return;
            }
            list.innerHTML = data.map(item => {
                const date = new Date(item.downloaded).toLocaleString();
                const name = item.filename.split('/').pop();
                return '<div class="history-item">' +
                    '<div class="name">' + escapeHtml(name) + '</div>' +
                    '<div class="size">' + formatBytes(item.size) + '</div>' +
                    '<div class="date">' + escapeHtml(date) + '</div>' +
                '</div>';
            }).join('');
        }

        async function loadWebshareStatus() {
            const box = document.getElementById('webshare-auth-status');
            if (!box) return;
            try {
                const resp = await fetch('/api/webshare/status');
                const data = await resp.json();
                box.classList.remove('visible', 'ok', 'warn', 'error');
                if (data.authenticated) {
                    box.classList.add('ok');
                    box.innerHTML = '';
                } else if (data.configured) {
                    box.classList.add('visible', 'error');
                    box.innerHTML = '<div class="auth-title">Webshare login needs attention</div>' +
                        '<div class="auth-detail">' + escapeHtml(data.message || data.error || 'Authentication failed.') + '</div>';
                } else {
                    box.classList.add('visible', 'warn');
                    box.innerHTML = '<div class="auth-title">Fast Webshare downloads are not configured</div>' +
                        '<div class="auth-detail">' + escapeHtml(data.message) + '</div>';
                }
            } catch (err) {
                box.classList.remove('ok', 'warn');
                box.classList.add('visible', 'error');
                box.innerHTML = '<div class="auth-title">Could not check Webshare login</div>' +
                    '<div class="auth-detail">' + escapeHtml(err.message) + '</div>';
            }
        }

        async function loadTMDbStatus() {
            const box = document.getElementById('tmdb-auth-status');
            if (!box) return;
            try {
                const resp = await fetch('/api/tmdb/status');
                const data = await resp.json();
                box.classList.remove('visible', 'ok', 'warn', 'error');
                if (data.authenticated) {
                    box.classList.add('ok');
                    box.innerHTML = '';
                } else if (data.configured) {
                    box.classList.add('visible', 'error');
                    box.innerHTML = '<div class="auth-title">TMDb configuration needs attention</div>' +
                        '<div class="auth-detail">' + escapeHtml(data.message || data.error || 'TMDb validation failed.') + '</div>';
                } else {
                    box.classList.add('visible', 'warn');
                    box.innerHTML = '<div class="auth-title">Movie Search is not configured</div>' +
                        '<div class="auth-detail">' + escapeHtml(data.message) + '</div>';
                }
            } catch (err) {
                box.classList.remove('ok', 'warn');
                box.classList.add('visible', 'error');
                box.innerHTML = '<div class="auth-title">Could not check TMDb configuration</div>' +
                    '<div class="auth-detail">' + escapeHtml(err.message) + '</div>';
            }
        }

        loadHistory();
        loadWebshareStatus();
        loadTMDbStatus();
        fetch('/api/progress').then(r => r.json()).then(data => {
            if (data.length > 0) pollProgress();
        });
    </script>
</body>
</html>`

func startWebServer(addr, outputDir, historyFile string, maxConcurrent int) {
	history, _, err := loadHistory(historyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading history: %v\n", err)
		os.Exit(1)
	}

	wd := &WebDownloader{
		outputDir:   outputDir,
		historyFile: historyFile,
		history:     history,
		downloads:   make(map[string]*ActiveDownload),
		maxRunning:  maxConcurrent,
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(htmlTemplate))
	})

	http.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		id, err := wd.startDownload(req.URL)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})

	http.HandleFunc("/api/series/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req SeriesSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
		defer cancel()
		matches, err := wd.searchSeries(ctx, req)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(matches)
	})

	http.HandleFunc("/api/movie/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req MovieSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		match, err := wd.searchMovie(ctx, req)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(match)
	})

	http.HandleFunc("/api/movie/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req MovieDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		started := make([]string, 0, len(req.Items))
		var errs []string
		for _, item := range req.Items {
			item.URL = strings.TrimSpace(item.URL)
			if item.URL == "" {
				continue
			}
			id, err := wd.startDownloadTo(item.URL, movieMetadataTargetPath(item))
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			started = append(started, id)
		}
		if len(started) == 0 && len(errs) > 0 {
			http.Error(w, strings.Join(errs, "\n"), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"started": started, "errors": errs})
	})

	http.HandleFunc("/api/series/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req SeriesDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		started := make([]string, 0, len(req.Items)+len(req.URLs))
		var errs []string
		for _, item := range req.Items {
			item.URL = strings.TrimSpace(item.URL)
			if item.URL == "" {
				continue
			}
			id, err := wd.startDownloadTo(item.URL, seriesTargetPath(item))
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			started = append(started, id)
		}
		for _, rawURL := range req.URLs {
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" {
				continue
			}
			id, err := wd.startDownload(rawURL)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			started = append(started, id)
		}
		if len(started) == 0 && len(errs) > 0 {
			http.Error(w, strings.Join(errs, "\n"), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"started": started, "errors": errs})
	})

	http.HandleFunc("/api/webshare/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(checkWebshareStatus(ctx))
	})

	http.HandleFunc("/api/tmdb/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(checkTMDbStatus(ctx))
	})

	http.HandleFunc("/api/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		wd.cancelDownload(req.ID)
		w.WriteHeader(200)
	})

	http.HandleFunc("/api/progress", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(wd.getActiveDownloads())
	})

	http.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(wd.getHistory())
	})

	fmt.Printf("Starting web server at http://%s\n", addr)
	fmt.Printf("Max concurrent downloads: %d\n", maxConcurrent)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	outputDir := flag.String("o", ".", "Output directory for downloads")
	historyFile := flag.String("history", ".download_history.json", "History file path")
	force := flag.Bool("f", false, "Force re-download even if already downloaded")
	listHistory := flag.Bool("list", false, "List download history")
	webAddr := flag.String("web", "", "Start web UI on this address (e.g., :8080)")
	maxConcurrent := flag.Int("max-concurrent", 0, "Maximum concurrent web downloads (default 5, or MAX_CONCURRENT_DOWNLOADS)")
	flag.Parse()

	if loaded, err := loadEnvFiles("/data/webshare.env", "/data/.env", ".env"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load env file: %v\n", err)
	} else if len(loaded) > 0 {
		fmt.Printf("Loaded env file(s): %s\n", strings.Join(loaded, ", "))
	}

	// Set up signal handling for cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		cleanupCurrentDownload()
		os.Exit(1)
	}()

	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	// Web server mode
	if *webAddr != "" {
		startWebServer(*webAddr, *outputDir, *historyFile, configuredMaxConcurrentDownloads(*maxConcurrent))
		return
	}

	history, needsSave, err := loadHistory(*historyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading history: %v\n", err)
		os.Exit(1)
	}

	// Save migrated history
	if needsSave {
		if err := saveHistory(*historyFile, history); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save migrated history: %v\n", err)
		}
	}

	if *listHistory {
		if len(history.Downloads) == 0 {
			fmt.Println("No downloads in history")
			return
		}
		fmt.Printf("Downloaded files (%d):\n", len(history.DownloadedFiles))
		for filename, u := range history.DownloadedFiles {
			fmt.Printf("  %s\n    URL: %s\n", filename, u[:min(80, len(u))]+"...")
		}
		return
	}

	var urls []string

	if flag.NArg() > 0 {
		urls = flag.Args()
	} else {
		scanner := bufio.NewScanner(os.Stdin)
		// Increase buffer for very long URLs
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		fmt.Println("Paste URLs (one per line, empty line or Ctrl+D to finish):")
		for scanner.Scan() {
			line := scanner.Text()
			// Clean up - handle \r\n, extra whitespace
			line = strings.TrimSpace(line)
			line = strings.ReplaceAll(line, "\r", "")
			if line == "" {
				break
			}
			urls = append(urls, line)
		}
	}

	if len(urls) == 0 {
		fmt.Println("No URLs provided")
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()

	for _, rawURL := range urls {
		// Clean up URL - remove all whitespace, carriage returns, newlines
		rawURL = strings.TrimSpace(rawURL)
		rawURL = strings.ReplaceAll(rawURL, "\r", "")
		rawURL = strings.ReplaceAll(rawURL, "\n", "")
		if rawURL == "" {
			continue
		}

		// Check if already downloaded (by URL)
		if record, exists := history.Downloads[rawURL]; exists && !*force {
			fmt.Printf("SKIP (same URL): %s\n", record.Filename)
			continue
		}

		// Check if already downloaded (by filename)
		filename := filenameFromURL(rawURL)
		if _, exists := history.DownloadedFiles[filename]; exists && !*force {
			fmt.Printf("SKIP (already have): %s\n", filename)
			continue
		}

		fmt.Printf("Downloading: %s\n", filename)
		outputPath, size, err := downloadFile(ctx, rawURL, *outputDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			continue
		}

		history.Downloads[rawURL] = DownloadRecord{
			URL:        rawURL,
			Filename:   outputPath,
			Downloaded: time.Now(),
			Size:       size,
		}
		history.DownloadedFiles[filename] = rawURL

		if err := saveHistory(*historyFile, history); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save history: %v\n", err)
		}

		fmt.Printf("OK: %s (%s)\n", outputPath, formatBytes(size))
	}
}
