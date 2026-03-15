package main

import (
	"cmp"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/alecthomas/kong"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	miniflux "miniflux.app/v2/client"
)

// --- Types ---

var cli struct {
	MinifluxURL    string `help:"Miniflux instance URL (env: MINIFLUX_URL)." name:"miniflux-url" placeholder:"URL"`
	APIToken       string `help:"API token, plain text or op:// reference (env: MINIFLUX_API_TOKEN)." name:"api-token" placeholder:"TOKEN"`
	CategoryID     *int64 `help:"Category ID for new feeds, 0 for uncategorized." name:"category-id" placeholder:"ID"`
	OPMLCategories string `help:"OPML category handling: ignore or create (env: MINIFLUX_OPML_CATEGORIES)." name:"opml-categories" placeholder:"MODE"`
	Config         string `help:"Path to config file." placeholder:"PATH"`
	ListCategories bool   `help:"List available categories and exit." name:"list-categories"`

	Input string `arg:"" optional:"" help:"Feed file path or URL (.txt, .opml, .xml)."`
}

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
	XMLName  xml.Name      `xml:"opml"`
	Outlines []opmlOutline `xml:"body>outline"`
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
	kong.Parse(&cli,
		kong.Name("miniflux-adder"),
		kong.Description("Bulk-add RSS/Atom feeds to a Miniflux instance from a text file, OPML export, or URL."),
		kong.UsageOnError(),
	)

	cfg, token := resolveConfig()

	if cli.ListCategories {
		listCategories(cfg, token)
		return
	}

	if cli.Input == "" {
		fatal("please provide a feed file path or URL")
	}

	data := readInput(cli.Input)
	isOPML := isOPMLFile(cli.Input)
	client := miniflux.NewClient(cfg.MinifluxURL, token)

	var feeds []feedEntry
	if isOPML {
		feeds = loadOPMLFeeds(data, client, cfg)
	} else {
		feeds = loadTextFeeds(data, cfg.CategoryID)
	}

	fmt.Println(headerStyle.Render("miniflux-adder"))
	fmt.Printf("Adding %d feed(s) to %s\n\n", len(feeds), cfg.MinifluxURL)

	results := addFeeds(client, feeds)
	succeeded, skipped, failed, remainingURLs := tallyResults(results)
	printSummary(succeeded, skipped, failed)

	if cfg.RemoveAdded && !isOPML && !isURL(cli.Input) && len(remainingURLs) != len(feeds) {
		if err := writeURLs(cli.Input, remainingURLs); err != nil {
			fatal("updating %s: %v", cli.Input, err)
		}
		fmt.Println(dimStyle.Render(fmt.Sprintf("  Updated %s (%d URLs remaining)", cli.Input, len(remainingURLs))))
	}

	if failed > 0 {
		os.Exit(1)
	}
}

// --- Config resolution ---

// resolveConfig merges configuration from all sources.
// Precedence (highest to lowest): flags > env vars > config file > defaults.
func resolveConfig() (config, string) {
	fileCfg, err := loadConfig(cli.Config)
	if err != nil {
		fatal("%v", err)
	}

	cfg := config{
		MinifluxURL:    cmp.Or(cli.MinifluxURL, os.Getenv("MINIFLUX_URL"), fileCfg.MinifluxURL),
		APIToken:       cmp.Or(cli.APIToken, os.Getenv("MINIFLUX_API_TOKEN"), fileCfg.APIToken),
		OPMLCategories: cmp.Or(cli.OPMLCategories, os.Getenv("MINIFLUX_OPML_CATEGORIES"), fileCfg.OPMLCategories, "ignore"),
		RemoveAdded:    fileCfg.RemoveAdded,
	}

	if cli.CategoryID != nil {
		cfg.CategoryID = *cli.CategoryID
	} else {
		cfg.CategoryID = fileCfg.CategoryID
	}

	if cfg.MinifluxURL == "" {
		fatal("miniflux URL is required (use --miniflux-url, MINIFLUX_URL, or config file)")
	}
	if cfg.OPMLCategories != "ignore" && cfg.OPMLCategories != "create" {
		fatal("invalid --opml-categories value %q (must be \"ignore\" or \"create\")", cfg.OPMLCategories)
	}

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

	return cfg, token
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

// readInput returns the content of the input as bytes.
// For local files it reads from disk; for URLs it fetches over HTTP.
func readInput(input string) []byte {
	if isURL(input) {
		fmt.Printf("Fetching %s\n", input)
		data, err := fetchURL(input)
		if err != nil {
			fatal("%v", err)
		}
		return data
	}

	data, err := os.ReadFile(input)
	if err != nil {
		fatal("%v", err)
	}
	return data
}

func loadTextFeeds(data []byte, categoryID int64) []feedEntry {
	urls := parseURLs(data)
	if len(urls) == 0 {
		fatal("no URLs found in file")
	}

	feeds := make([]feedEntry, len(urls))
	for i, u := range urls {
		feeds[i] = feedEntry{url: u, categoryID: categoryID}
	}
	return feeds
}

func loadOPMLFeeds(data []byte, client *miniflux.Client, cfg config) []feedEntry {
	opmlFeeds, err := parseOPML(data)
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
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isOPMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".opml" || ext == ".xml"
}

func fetchURL(rawURL string) ([]byte, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: HTTP %d", rawURL, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	return data, nil
}

func parseURLs(data []byte) []string {
	var urls []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls
}

func parseOPML(data []byte) ([]opmlFeed, error) {
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
	walk(doc.Outlines, "")

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

	color := lipgloss.Color("10") // green
	if failed > 0 {
		color = lipgloss.Color("9") // red
	}
	fmt.Println(boxStyle.BorderForeground(color).Render(summary))
}

func fatal(format string, args ...any) {
	fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: "+format, args...)))
	os.Exit(1)
}
