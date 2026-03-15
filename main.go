package main

import (
	"bufio"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	miniflux "miniflux.app/v2/client"
)

// --- Types ---

type config struct {
	MinifluxURL    string `toml:"miniflux_url"`
	APIToken       string `toml:"api_token"`
	CategoryID     int64  `toml:"category_id"`
	RemoveAdded    bool   `toml:"remove_added"`
	OPMLCategories string `toml:"opml_categories"`
}

type feedEntry struct {
	url        string
	categoryID int64
}

type feedResult struct {
	url string
	err error
}

type opmlDocument struct {
	XMLName xml.Name `xml:"opml"`
	Body    opmlBody `xml:"body"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr"`
	XMLURL   string        `xml:"xmlUrl,attr"`
	Children []opmlOutline `xml:"outline"`
}

type opmlFeed struct {
	url      string
	category string
}

// --- Styles ---

var (
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// --- Main ---

func main() {
	cfg, token := parseFlags()

	if cfg.listCategories {
		listCategories(cfg.config, token)
		return
	}

	if flag.NArg() < 1 {
		fatal("please provide a path to a file containing feed URLs")
	}
	input := flag.Arg(0)

	filePath, cleanup := resolveInput(input)
	if cleanup != nil {
		defer cleanup()
	}

	isOPML := isOPMLFile(input)
	client := miniflux.NewClient(cfg.MinifluxURL, token)

	feeds := loadFeeds(filePath, isOPML, client, cfg.config)

	fmt.Println(headerStyle.Render("miniflux-adder"))
	fmt.Printf("Adding %d feed(s) to %s\n\n", len(feeds), cfg.MinifluxURL)

	results := addFeeds(client, feeds)
	succeeded, skipped, failed, remainingURLs := tallyResults(results)
	printSummary(succeeded, skipped, failed)

	if cfg.RemoveAdded && len(remainingURLs) != len(feeds) {
		handleRemoveAdded(filePath, remainingURLs, isURL(input), isOPML)
	}

	if failed > 0 {
		os.Exit(1)
	}
}

// parsedFlags bundles the parsed config with the list-categories flag,
// since that flag short-circuits before we need a feed file.
type parsedFlags struct {
	config
	listCategories bool
}

func parseFlags() (parsedFlags, string) {
	flagURL := flag.String("miniflux-url", "", "Miniflux instance URL (env: MINIFLUX_URL)")
	flagToken := flag.String("api-token", "", "Miniflux API token (env: MINIFLUX_API_TOKEN)")
	flagCategory := flag.Int64("category-id", 0, "Category ID for new feeds (0 = uncategorized)")
	flagListCategories := flag.Bool("list-categories", false, "List available categories and exit")
	flagConfig := flag.String("config", "", "Path to config file (default: $XDG_CONFIG_HOME/miniflux-adder/config.toml)")
	flagOPMLCategories := flag.String("opml-categories", "", "OPML category handling: ignore (default) or create (env: MINIFLUX_OPML_CATEGORIES)")
	flag.Parse()

	cfg, err := loadConfig(*flagConfig)
	if err != nil {
		fatal("%v", err)
	}

	// Env vars override config file
	if env := os.Getenv("MINIFLUX_URL"); env != "" {
		cfg.MinifluxURL = env
	}
	if env := os.Getenv("MINIFLUX_API_TOKEN"); env != "" {
		cfg.APIToken = env
	}
	if env := os.Getenv("MINIFLUX_OPML_CATEGORIES"); env != "" {
		cfg.OPMLCategories = env
	}

	// Flags override env vars
	if *flagURL != "" {
		cfg.MinifluxURL = *flagURL
	}
	if *flagToken != "" {
		cfg.APIToken = *flagToken
	}
	if *flagCategory != 0 {
		cfg.CategoryID = *flagCategory
	}
	if *flagOPMLCategories != "" {
		cfg.OPMLCategories = *flagOPMLCategories
	}

	// Defaults and validation
	if cfg.OPMLCategories == "" {
		cfg.OPMLCategories = "ignore"
	}
	if cfg.OPMLCategories != "ignore" && cfg.OPMLCategories != "create" {
		fatal("invalid opml_categories value %q (must be \"ignore\" or \"create\")", cfg.OPMLCategories)
	}
	if cfg.MinifluxURL == "" {
		fatal("miniflux URL is required (use --miniflux-url, MINIFLUX_URL, or config file)")
	}

	// Resolve API token
	token := cfg.APIToken
	if strings.HasPrefix(token, "op://") {
		fmt.Println(dimStyle.Render("Resolving API token from 1Password..."))
		resolved, err := resolveOPSecret(token)
		if err != nil {
			fatal("failed to resolve 1Password secret: %v", err)
		}
		token = resolved
	}
	if token == "" {
		fatal("API token is required (use --api-token, MINIFLUX_API_TOKEN, or config file)")
	}

	return parsedFlags{config: cfg, listCategories: *flagListCategories}, token
}

// --- Commands ---

func listCategories(cfg config, token string) {
	client := miniflux.NewClient(cfg.MinifluxURL, token)
	categories, err := client.CategoriesWithCounters()
	if err != nil {
		fatal("%v", err)
	}
	fmt.Println(headerStyle.Render("Categories"))
	fmt.Println()
	for _, cat := range categories {
		feedCount := 0
		if cat.FeedCount != nil {
			feedCount = *cat.FeedCount
		}
		fmt.Printf("  %s  %s  %s\n",
			successStyle.Render(fmt.Sprintf("%4d", cat.ID)),
			cat.Title,
			dimStyle.Render(fmt.Sprintf("(%d feeds)", feedCount)),
		)
	}
}

func addFeeds(client *miniflux.Client, feeds []feedEntry) []feedResult {
	results := make([]feedResult, 0, len(feeds))
	for _, fe := range feeds {
		var r feedResult
		r.url = fe.url
		catID := fe.categoryID

		err := spinner.New().
			Title(fmt.Sprintf("  Adding %s", fe.url)).
			Action(func() {
				req := miniflux.FeedCreationRequest{
					FeedURL: fe.url,
				}
				if catID > 0 {
					req.CategoryID = catID
				}
				_, r.err = client.CreateFeed(&req)
			}).
			Run()
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("  spinner error: %v", err)))
		}

		results = append(results, r)
	}
	return results
}

// --- Input resolution ---

// resolveInput returns the local file path to read and an optional cleanup function.
// If the input is a URL, the content is fetched to a temp file.
func resolveInput(input string) (filePath string, cleanup func()) {
	if !isURL(input) {
		return input, nil
	}

	fmt.Printf("Fetching %s\n", input)
	downloaded, err := fetchToTempFile(input)
	if err != nil {
		fatal("%v", err)
	}
	return downloaded, func() { os.Remove(downloaded) }
}

// loadFeeds reads feeds from the given file path, dispatching to OPML or
// plain-text parsing. For OPML with opml_categories="create", categories
// are resolved/created in Miniflux.
func loadFeeds(filePath string, isOPML bool, client *miniflux.Client, cfg config) []feedEntry {
	if isOPML {
		return loadOPMLFeeds(filePath, client, cfg)
	}
	return loadTextFeeds(filePath, cfg.CategoryID)
}

func loadTextFeeds(filePath string, categoryID int64) []feedEntry {
	urls, err := readURLs(filePath)
	if err != nil {
		fatal("%v", err)
	}
	if len(urls) == 0 {
		fatal("no URLs found in file")
	}

	feeds := make([]feedEntry, len(urls))
	for i, u := range urls {
		feeds[i] = feedEntry{url: u, categoryID: categoryID}
	}
	return feeds
}

func loadOPMLFeeds(filePath string, client *miniflux.Client, cfg config) []feedEntry {
	opmlFeeds, err := parseOPML(filePath)
	if err != nil {
		fatal("%v", err)
	}
	if len(opmlFeeds) == 0 {
		fatal("no feeds found in OPML file")
	}

	var categoryMap map[string]int64
	if cfg.OPMLCategories == "create" {
		categoryMap, err = resolveOPMLCategories(client, opmlFeeds)
		if err != nil {
			fatal("resolving categories: %v", err)
		}
	}

	feeds := make([]feedEntry, len(opmlFeeds))
	for i, f := range opmlFeeds {
		catID := cfg.CategoryID
		if categoryMap != nil && f.category != "" {
			if id, ok := categoryMap[f.category]; ok {
				catID = id
			}
		}
		feeds[i] = feedEntry{url: f.url, categoryID: catID}
	}
	return feeds
}

// --- Config ---

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "miniflux-adder")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "miniflux-adder")
}

func loadConfig(path string) (config, error) {
	var cfg config
	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("loading config %s: %w", path, err)
		}
		return cfg, nil
	}
	// Try ./config.toml first, then XDG location
	for _, p := range []string{
		"config.toml",
		filepath.Join(configDir(), "config.toml"),
	} {
		if _, err := toml.DecodeFile(p, &cfg); err == nil {
			return cfg, nil
		}
	}
	return cfg, nil
}

func resolveOPSecret(ref string) (string, error) {
	cmd := exec.Command("op", "read", ref)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("op read %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// --- Input parsing ---

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

func isOPMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".opml" || ext == ".xml"
}

func fetchToTempFile(rawURL string) (string, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: HTTP %d", rawURL, resp.StatusCode)
	}

	// Preserve the original URL extension so format detection works
	u, _ := url.Parse(rawURL)
	ext := filepath.Ext(u.Path)
	f, err := os.CreateTemp("", "miniflux-adder-*"+ext)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	return f.Name(), nil
}

func readURLs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return urls, nil
}

func parseOPML(path string) ([]opmlFeed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var doc opmlDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing OPML: %w", err)
	}

	var feeds []opmlFeed
	var walk func(outlines []opmlOutline, category string)
	walk = func(outlines []opmlOutline, category string) {
		for _, o := range outlines {
			if o.XMLURL != "" {
				feeds = append(feeds, opmlFeed{url: o.XMLURL, category: category})
			}
			if len(o.Children) > 0 {
				cat := category
				if o.XMLURL == "" {
					cat = o.Text
					if cat == "" {
						cat = o.Title
					}
				}
				walk(o.Children, cat)
			}
		}
	}
	walk(doc.Body.Outlines, "")

	return feeds, nil
}

func resolveOPMLCategories(client *miniflux.Client, feeds []opmlFeed) (map[string]int64, error) {
	categories, err := client.Categories()
	if err != nil {
		return nil, fmt.Errorf("fetching categories: %w", err)
	}

	catMap := make(map[string]int64)
	for _, c := range categories {
		catMap[c.Title] = c.ID
	}

	needed := make(map[string]bool)
	for _, f := range feeds {
		if f.category != "" {
			if _, exists := catMap[f.category]; !exists {
				needed[f.category] = true
			}
		}
	}

	for name := range needed {
		cat, err := client.CreateCategory(name)
		if err != nil {
			return nil, fmt.Errorf("creating category %q: %w", name, err)
		}
		catMap[name] = cat.ID
		fmt.Printf("  %s Created category %q (ID: %d)\n", successStyle.Render("✓"), name, cat.ID)
	}

	return catMap, nil
}

// --- Output ---

func writeURLs(path string, urls []string) error {
	content := strings.Join(urls, "\n")
	if len(urls) > 0 {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func tallyResults(results []feedResult) (succeeded, skipped, failed int, remainingURLs []string) {
	for _, r := range results {
		if r.err == nil {
			succeeded++
			continue
		}
		if strings.Contains(r.err.Error(), "duplicated feed") || strings.Contains(r.err.Error(), "already exists") {
			skipped++
			continue
		}
		fmt.Println(errorStyle.Render(fmt.Sprintf("  ✗ %s — %v", r.url, r.err)))
		remainingURLs = append(remainingURLs, r.url)
		failed++
	}
	return
}

func printSummary(succeeded, skipped, failed int) {
	if failed > 0 {
		fmt.Println()
	}

	total := succeeded + skipped + failed
	summary := fmt.Sprintf("%d added, %d already existed, %d failed out of %d total", succeeded, skipped, failed, total)

	if failed > 0 {
		fmt.Println(boxStyle.BorderForeground(lipgloss.Color("9")).Render(summary))
	} else {
		fmt.Println(boxStyle.BorderForeground(lipgloss.Color("10")).Render(summary))
	}
}

func handleRemoveAdded(filePath string, remainingURLs []string, isRemote, isOPML bool) {
	if isRemote {
		fmt.Println(dimStyle.Render("  Note: remove_added is not supported for URL inputs"))
	} else if isOPML {
		fmt.Println(dimStyle.Render("  Note: remove_added is not supported for OPML files"))
	} else {
		if err := writeURLs(filePath, remainingURLs); err != nil {
			fatal("updating %s: %v", filePath, err)
		}
		fmt.Println(dimStyle.Render(fmt.Sprintf("  Updated %s (%d URLs remaining)", filePath, len(remainingURLs))))
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: "+format, args...)))
	os.Exit(1)
}
