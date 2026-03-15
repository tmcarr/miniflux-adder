# miniflux-adder

Single-file Go CLI (`main.go`) that bulk-adds RSS/Atom feeds to a Miniflux instance from plain-text URL lists, OPML files, or URLs.

## Build & Run

```bash
go build
./miniflux-adder myfeeds.txt
./miniflux-adder feeds.opml
./miniflux-adder --opml-categories=create feeds.opml
./miniflux-adder https://example.com/feeds.opml
./miniflux-adder --list-categories
```

## Project Layout

- `main.go` - Entire application (single file)
- `config.toml` - Local config (gitignored, see `config.toml.example`)
- `myfeeds.txt` - Feed URL list (one per line, `#` comments allowed)
- `feeds.opml` - OPML feed export (auto-detected by `.opml`/`.xml` extension)

## Key Details

- Config precedence: flags > env vars > config file
- `api_token` values starting with `op://` are resolved via `op read` (1Password CLI)
- Config file search: `./config.toml` then `$XDG_CONFIG_HOME/miniflux-adder/config.toml`
- No tests exist
- Uses charmbracelet libs for terminal UI (spinners, styled output)
- `remove_added = true` rewrites the feeds file keeping only failed URLs (plain-text local files only, skipped for OPML and URL inputs)
- URL inputs (`http://`/`https://`) are fetched to a temp file automatically; format detected from URL extension
- Exit code 1 if any feeds fail to add
- OPML support: `.opml`/`.xml` files are auto-detected and parsed with `encoding/xml` (stdlib)
- `opml_categories`: `"ignore"` (default) uses `category_id` for all feeds; `"create"` creates Miniflux categories matching OPML structure

## Dependencies

- `github.com/alecthomas/kong` - CLI argument parsing
- `github.com/BurntSushi/toml` - Config file parsing
- `github.com/charmbracelet/huh/spinner` - Progress spinners
- `github.com/charmbracelet/lipgloss` - Terminal styling
- `miniflux.app/v2/client` - Miniflux API client
