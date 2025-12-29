package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func downloadFile(rawURL, outputDir string) (string, int64, error) {
	resp, err := http.Get(rawURL)
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
	defer out.Close()

	pw := &ProgressWriter{
		Total:    resp.ContentLength,
		Filename: filepath.Base(outputPath),
	}

	size, err := io.Copy(out, io.TeeReader(resp.Body, pw))
	fmt.Println() // newline after progress bar
	if err != nil {
		os.Remove(outputPath)
		return "", 0, err
	}

	return outputPath, size, nil
}

func main() {
	outputDir := flag.String("o", ".", "Output directory for downloads")
	historyFile := flag.String("history", ".download_history.json", "History file path")
	force := flag.Bool("f", false, "Force re-download even if already downloaded")
	listHistory := flag.Bool("list", false, "List download history")
	flag.Parse()

	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
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
		fmt.Println("Paste URLs (one per line, empty line or Ctrl+D to finish):")
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
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

	for _, rawURL := range urls {
		rawURL = strings.TrimSpace(rawURL)
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
		fmt.Printf("DEBUG: extracted filename = '%s'\n", filename)
		fmt.Printf("DEBUG: known files = %v\n", keys(history.DownloadedFiles))
		if _, exists := history.DownloadedFiles[filename]; exists && !*force {
			fmt.Printf("SKIP (already have): %s\n", filename)
			continue
		}

		fmt.Printf("Downloading: %s\n", filename)
		outputPath, size, err := downloadFile(rawURL, *outputDir)
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
