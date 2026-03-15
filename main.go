package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	miniflux "miniflux.app/v2/client"
)

type config struct {
	MinifluxURL string `toml:"miniflux_url"`
	APIToken    string `toml:"api_token"`
	CategoryID  int64  `toml:"category_id"`
	RemoveAdded bool   `toml:"remove_added"`
}

var (
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

func main() {
	flagURL := flag.String("miniflux-url", "", "Miniflux instance URL (env: MINIFLUX_URL)")
	flagToken := flag.String("api-token", "", "Miniflux API token (env: MINIFLUX_API_TOKEN)")
	flagCategory := flag.Int64("category-id", 0, "Category ID for new feeds (0 = uncategorized)")
	flagListCategories := flag.Bool("list-categories", false, "List available categories and exit")
	flagConfig := flag.String("config", "", "Path to config file (default: $XDG_CONFIG_HOME/miniflux-adder/config.toml)")
	flag.Parse()

	// Load config: file -> env vars -> flags (last wins)
	cfg, err := loadConfig(*flagConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: %v", err)))
		os.Exit(1)
	}

	if env := os.Getenv("MINIFLUX_URL"); env != "" {
		cfg.MinifluxURL = env
	}
	if env := os.Getenv("MINIFLUX_API_TOKEN"); env != "" {
		cfg.APIToken = env
	}

	if *flagURL != "" {
		cfg.MinifluxURL = *flagURL
	}
	if *flagToken != "" {
		cfg.APIToken = *flagToken
	}
	if *flagCategory != 0 {
		cfg.CategoryID = *flagCategory
	}

	if cfg.MinifluxURL == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: miniflux URL is required (use --miniflux-url, MINIFLUX_URL, or config file)"))
		os.Exit(1)
	}

	// Resolve API token: if the value looks like an op:// reference, resolve it via 1Password CLI.
	token := cfg.APIToken
	if strings.HasPrefix(token, "op://") {
		resolved, err := resolveOPSecret(token)
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: failed to resolve 1Password secret: %v", err)))
			os.Exit(1)
		}
		token = resolved
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: API token is required (use --api-token, MINIFLUX_API_TOKEN, or config file)"))
		os.Exit(1)
	}

	if *flagListCategories {
		client := miniflux.NewClient(cfg.MinifluxURL, token)
		categories, err := client.CategoriesWithCounters()
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: %v", err)))
			os.Exit(1)
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
		return
	}

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: please provide a path to a file containing feed URLs"))
		os.Exit(1)
	}
	filePath := flag.Arg(0)

	urls, err := readURLs(filePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: %v", err)))
		os.Exit(1)
	}

	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: no URLs found in file"))
		os.Exit(1)
	}

	fmt.Println(headerStyle.Render("miniflux-adder"))
	fmt.Printf("Adding %d feed(s) to %s\n\n", len(urls), cfg.MinifluxURL)

	client := miniflux.NewClient(cfg.MinifluxURL, token)

	type feedResult struct {
		url string
		err error
	}

	results := make([]feedResult, 0, len(urls))
	for _, url := range urls {
		var r feedResult
		r.url = url

		err := spinner.New().
			Title(fmt.Sprintf("  Adding %s", url)).
			Action(func() {
				req := miniflux.FeedCreationRequest{
					FeedURL: url,
				}
				if cfg.CategoryID > 0 {
					req.CategoryID = cfg.CategoryID
				}
				_, r.err = client.CreateFeed(&req)
			}).
			Run()
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("  spinner error: %v", err)))
		}

		results = append(results, r)
	}

	var succeeded, skipped, failed int
	var remainingURLs []string
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

	if cfg.RemoveAdded && len(remainingURLs) != len(urls) {
		if err := writeURLs(filePath, remainingURLs); err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error updating %s: %v", filePath, err)))
			os.Exit(1)
		}
		fmt.Println(dimStyle.Render(fmt.Sprintf("  Updated %s (%d URLs remaining)", filePath, len(remainingURLs))))
	}

	if failed > 0 {
		os.Exit(1)
	}
}

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

func writeURLs(path string, urls []string) error {
	content := strings.Join(urls, "\n")
	if len(urls) > 0 {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
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
