package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
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
	Progress   int64              `json:"progress"`
	Total      int64              `json:"total"`
	Speed      int64              `json:"speed"` // bytes per second
	StartedAt  time.Time          `json:"started_at"`
	OutputPath string             `json:"-"`
	CancelFunc context.CancelFunc `json:"-"`
}

// Web server state
type WebDownloader struct {
	outputDir   string
	historyFile string
	history     *History
	historyMu   sync.RWMutex

	downloads   map[string]*ActiveDownload
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
	// Sort by start time (oldest first - keeps stable order)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartedAt.Before(result[j].StartedAt)
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
	wd          *WebDownloader
	downloadID  string
	Total       int64
	Downloaded  int64
	LastUpdate  time.Time
	LastBytes   int64
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

func (wd *WebDownloader) downloadFile(ctx context.Context, downloadID, rawURL string) (string, int64, error) {
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
	outputPath := filepath.Join(wd.outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		outputPath = filepath.Join(wd.outputDir, fmt.Sprintf("%s_%s%s", base, urlHash(rawURL), ext))
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
	filename := filenameFromURL(rawURL)

	// Check history
	wd.historyMu.RLock()
	_, urlExists := wd.history.Downloads[rawURL]
	_, fileExists := wd.history.DownloadedFiles[filename]
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
		StartedAt:  time.Now(),
		CancelFunc: cancel,
	}
	wd.downloadsMu.Unlock()

	go func() {
		defer func() {
			wd.downloadsMu.Lock()
			delete(wd.downloads, id)
			wd.downloadsMu.Unlock()
		}()

		outputPath, size, err := wd.downloadFile(ctx, id, rawURL)
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
		wd.history.DownloadedFiles[filename] = rawURL
		saveHistory(wd.historyFile, wd.history)
		wd.historyMu.Unlock()
	}()

	return id, nil
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
		delete(wd.downloads, id)
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

const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>Downloader</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
        * { box-sizing: border-box; }
        body { font-family: system-ui, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; background: #1a1a2e; color: #eee; }
        h1 { color: #00d4ff; }
        .input-group { display: flex; gap: 10px; margin-bottom: 20px; }
        input[type="text"] { flex: 1; padding: 12px; border: 1px solid #333; border-radius: 6px; background: #16213e; color: #eee; font-size: 16px; }
        button { padding: 12px 24px; border: none; border-radius: 6px; cursor: pointer; font-size: 16px; font-weight: bold; }
        .btn-primary { background: #00d4ff; color: #000; }
        .btn-danger { background: #ff4757; color: #fff; padding: 8px 16px; font-size: 14px; }
        .btn-primary:hover { background: #00b8e6; }
        .btn-danger:hover { background: #ff3344; }
        .downloads-section { margin-bottom: 20px; }
        .downloads-section h2 { color: #00d4ff; border-bottom: 1px solid #333; padding-bottom: 10px; margin-bottom: 15px; }
        .download-item { background: #16213e; border-radius: 8px; padding: 15px; margin-bottom: 10px; }
        .download-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
        .download-filename { font-weight: bold; color: #00d4ff; word-break: break-all; }
        .progress-bar { height: 20px; background: #333; border-radius: 10px; overflow: hidden; margin: 8px 0; }
        .progress-fill { height: 100%; background: linear-gradient(90deg, #00d4ff, #00ff88); transition: width 0.3s; }
        .progress-text { font-size: 13px; color: #aaa; }
        .history { margin-top: 30px; }
        .history h2 { color: #00d4ff; border-bottom: 1px solid #333; padding-bottom: 10px; }
        .history-item { background: #16213e; padding: 15px; border-radius: 6px; margin-bottom: 10px; }
        .history-item .name { font-weight: bold; color: #00ff88; }
        .history-item .size { color: #aaa; font-size: 14px; }
        .history-item .date { color: #666; font-size: 12px; }
        .empty { color: #666; font-style: italic; }
    </style>
</head>
<body>
    <h1>Downloader</h1>

    <div class="input-group">
        <input type="text" id="url" placeholder="Enter URL to download..." onkeypress="if(event.key==='Enter')startDownload()">
        <button class="btn-primary" onclick="startDownload()">Download</button>
    </div>

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

        function formatBytes(bytes) {
            if (bytes === 0) return '0 B';
            const k = 1024;
            const sizes = ['B', 'KB', 'MB', 'GB'];
            const i = Math.floor(Math.log(bytes) / Math.log(k));
            return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
        }

        async function startDownload() {
            const url = document.getElementById('url').value.trim();
            if (!url) return;

            const resp = await fetch('/api/download', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({url: url})
            });

            if (resp.ok) {
                document.getElementById('url').value = '';
                if (!polling) pollProgress();
            } else {
                const text = await resp.text();
                alert('Failed: ' + text);
            }
        }

        async function cancelDownload(id) {
            await fetch('/api/cancel', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({id: id})
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
                        return '<div class="download-item" id="dl-' + d.id + '">' +
                            '<div class="download-header">' +
                                '<span class="download-filename">' + d.filename + '</span>' +
                                '<button class="btn-danger" onclick="cancelDownload(\'' + d.id + '\')">Cancel</button>' +
                            '</div>' +
                            '<div class="progress-bar"><div class="progress-fill" style="width:' + pct + '%"></div></div>' +
                            '<div class="progress-text">' + pct.toFixed(1) + '% - ' + formatBytes(d.progress) + ' / ' + formatBytes(d.total) + ' - ' + formatBytes(d.speed) + '/s</div>' +
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
                    '<div class="name">' + name + '</div>' +
                    '<div class="size">' + formatBytes(item.size) + '</div>' +
                    '<div class="date">' + date + '</div>' +
                '</div>';
            }).join('');
        }

        // Initial load
        loadHistory();

        // Check if downloads in progress
        fetch('/api/progress').then(r => r.json()).then(data => {
            if (data.length > 0) pollProgress();
        });
    </script>
</body>
</html>`

func startWebServer(addr, outputDir, historyFile string) {
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
		var req struct{ URL string `json:"url"` }
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

	http.HandleFunc("/api/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		var req struct{ ID string `json:"id"` }
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
	flag.Parse()

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
		startWebServer(*webAddr, *outputDir, *historyFile)
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
