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
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://webshare.cz/api"

type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

type saltResponse struct {
	XMLName xml.Name `xml:"response"`
	Status  string   `xml:"status"`
	Code    string   `xml:"code"`
	Message string   `xml:"message"`
	Salt    string   `xml:"salt"`
}

type loginResponse struct {
	XMLName xml.Name `xml:"response"`
	Status  string   `xml:"status"`
	Code    string   `xml:"code"`
	Message string   `xml:"message"`
	Token   string   `xml:"token"`
	Reason  string   `xml:"reason"`
}

type searchResponse struct {
	XMLName xml.Name     `xml:"response"`
	Status  string       `xml:"status"`
	Code    string       `xml:"code"`
	Message string       `xml:"message"`
	Total   int          `xml:"total"`
	Files   []SearchFile `xml:"file"`
}

type SearchFile struct {
	Ident         string `xml:"ident" json:"ident"`
	Name          string `xml:"name" json:"name"`
	Type          string `xml:"type" json:"type"`
	Image         string `xml:"img" json:"image,omitempty"`
	Stripe        string `xml:"stripe" json:"stripe,omitempty"`
	StripeCount   int    `xml:"stripe_count" json:"stripe_count"`
	Size          int64  `xml:"size" json:"size"`
	Queued        int    `xml:"queued" json:"queued"`
	PositiveVotes int    `xml:"positive_votes" json:"positive_votes"`
	NegativeVotes int    `xml:"negative_votes" json:"negative_votes"`
	Password      int    `xml:"password" json:"password"`
}

type linkResponse struct {
	XMLName xml.Name `xml:"response"`
	Status  string   `xml:"status"`
	Code    string   `xml:"code"`
	Message string   `xml:"message"`
	Link    string   `xml:"link"`
}

type Result struct {
	Ident         string `json:"ident"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Size          int64  `json:"size"`
	SizeHuman     string `json:"size_human"`
	PositiveVotes int    `json:"positive_votes"`
	NegativeVotes int    `json:"negative_votes"`
	Password      bool   `json:"password"`
	URL           string `json:"url,omitempty"`
	Error         string `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, defaultBaseURL, nil); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, baseURL string, httpClient *http.Client) error {
	var (
		query           string
		limit           int
		offset          int
		format          string
		timeout         time.Duration
		firstOnly       bool
		includePassword bool
		username        string
		password        string
		token           string
		keepLoggedIn    bool
	)

	fs := flag.NewFlagSet("webshare-search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&query, "query", "", "search text")
	fs.IntVar(&limit, "limit", 10, "maximum search results")
	fs.IntVar(&offset, "offset", 0, "search result offset")
	fs.StringVar(&format, "format", "tsv", "output format: tsv, csv, json")
	fs.DurationVar(&timeout, "timeout", 20*time.Second, "HTTP timeout")
	fs.BoolVar(&firstOnly, "first", false, "return only the first result with a generated URL")
	fs.BoolVar(&includePassword, "include-password", false, "try to generate links for password-protected files too")
	fs.StringVar(&username, "username", env("WEBSHARE_USERNAME", ""), "Webshare username or email; can also use WEBSHARE_USERNAME")
	fs.StringVar(&password, "password", env("WEBSHARE_PASSWORD", ""), "Webshare password; prefer WEBSHARE_PASSWORD")
	fs.StringVar(&token, "wst", env("WEBSHARE_WST", ""), "existing Webshare session token; can also use WEBSHARE_WST")
	fs.BoolVar(&keepLoggedIn, "keep-logged-in", false, "ask Webshare to keep the generated session token logged in")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 && query == "" {
		query = strings.Join(fs.Args(), " ")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("provide search text, for example: webshare-search \"ubuntu iso\"")
	}
	if limit <= 0 {
		return errors.New("-limit must be greater than zero")
	}
	if firstOnly {
		limit = 1
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	client := Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		token:      token,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if client.token == "" && username != "" {
		if password == "" {
			return errors.New("password is required when -username is set; use -password or WEBSHARE_PASSWORD")
		}
		token, err := client.Login(ctx, username, password, keepLoggedIn)
		if err != nil {
			return err
		}
		client.token = token
		fmt.Fprintln(stderr, "logged in to Webshare")
	} else if client.token != "" {
		fmt.Fprintln(stderr, "using Webshare session token")
	}

	search, err := client.Search(ctx, query, offset, limit)
	if err != nil {
		return err
	}
	if len(search.Files) == 0 {
		return fmt.Errorf("no results for %q", query)
	}

	results := make([]Result, 0, len(search.Files))
	for _, file := range search.Files {
		result := Result{
			Ident:         file.Ident,
			Name:          file.Name,
			Type:          file.Type,
			Size:          file.Size,
			SizeHuman:     humanSize(file.Size),
			PositiveVotes: file.PositiveVotes,
			NegativeVotes: file.NegativeVotes,
			Password:      file.Password != 0,
		}
		if file.Password != 0 && !includePassword {
			result.Error = "password protected; skipped"
			results = append(results, result)
			continue
		}
		link, err := client.FileLink(ctx, file.Ident)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.URL = link
		}
		results = append(results, result)
	}

	switch format {
	case "tsv":
		return writeDelimited(stdout, results, '\t')
	case "csv":
		return writeDelimited(stdout, results, ',')
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	default:
		return fmt.Errorf("unsupported -format %q; use tsv, csv, or json", format)
	}
}

func (c Client) Search(ctx context.Context, query string, offset, limit int) (searchResponse, error) {
	values := url.Values{}
	values.Set("what", query)
	values.Set("offset", strconv.Itoa(offset))
	values.Set("limit", strconv.Itoa(limit))

	var response searchResponse
	if err := c.postXML(ctx, "/search/", values, &response); err != nil {
		return response, err
	}
	if response.Status != "OK" {
		return response, apiError(response.Status, response.Code, response.Message)
	}
	return response, nil
}

func (c Client) FileLink(ctx context.Context, ident string) (string, error) {
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

func (c Client) Login(ctx context.Context, usernameOrEmail, password string, keepLoggedIn bool) (string, error) {
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

func (c Client) postXML(ctx context.Context, path string, values url.Values, target any) error {
	if c.token != "" && values.Get("wst") == "" {
		values.Set("wst", c.token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/xml")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "webshare-search/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return xml.NewDecoder(resp.Body).Decode(target)
}

func writeDelimited(w io.Writer, results []Result, comma rune) error {
	cw := csv.NewWriter(w)
	cw.Comma = comma
	if err := cw.Write([]string{
		"ident",
		"name",
		"type",
		"size",
		"size_human",
		"positive_votes",
		"negative_votes",
		"password",
		"url",
		"error",
	}); err != nil {
		return err
	}
	for _, result := range results {
		if err := cw.Write([]string{
			result.Ident,
			result.Name,
			result.Type,
			strconv.FormatInt(result.Size, 10),
			result.SizeHuman,
			strconv.Itoa(result.PositiveVotes),
			strconv.Itoa(result.NegativeVotes),
			strconv.FormatBool(result.Password),
			result.URL,
			result.Error,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func apiError(status, code, message string) error {
	parts := []string{"webshare API returned " + status}
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
