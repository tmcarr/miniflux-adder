# miniflux-adder

Bulk-add RSS/Atom feed URLs to a [Miniflux](https://miniflux.app/) instance from a text file, OPML export, or URL.

## Install

```bash
go install github.com/tmcarr/miniflux-adder@latest
```

## Usage

```bash
# Build from source
go build

# Add feeds from a text file
./miniflux-adder myfeeds.txt

# Add feeds from an OPML file (exported from another feed reader)
./miniflux-adder feeds.opml

# Import OPML and create categories in Miniflux to match the OPML structure
./miniflux-adder --opml-categories=create feeds.opml

# Fetch an OPML or text file from a URL
./miniflux-adder https://example.com/feeds.opml

# List available categories (useful for finding category IDs)
./miniflux-adder --list-categories

# Override config with flags
./miniflux-adder --miniflux-url https://rss.example.com --api-token YOUR_TOKEN myfeeds.txt
```

## Input Formats

### Plain text

One URL per line. Empty lines and lines starting with `#` are ignored.

```
https://blog.golang.org/feed.atom
https://github.blog/feed/
# This is a comment
https://www.nasa.gov/rss/dyn/breaking_news.rss
```

### OPML

Files with `.opml` or `.xml` extensions are automatically parsed as OPML — the standard format for importing/exporting RSS feed lists. Most feed readers can export an OPML file.

By default, OPML category information is ignored and all feeds are added to the configured `category_id`. Set `opml_categories = "create"` (or `--opml-categories=create`) to have miniflux-adder create categories in Miniflux matching the OPML structure. Feeds with no category in the OPML fall back to `category_id`.

### URL

Pass an `http://` or `https://` URL as the input and miniflux-adder will fetch it automatically. The file format is detected from the URL's extension (`.opml`/`.xml` for OPML, anything else for plain text).

```bash
./miniflux-adder https://example.com/feeds.opml
./miniflux-adder --opml-categories=create https://example.com/subscriptions.xml
```

> **Note:** `remove_added` has no effect on OPML files or URL inputs.

## Configuration

Create a `config.toml` in the working directory or at `$XDG_CONFIG_HOME/miniflux-adder/config.toml` (defaults to `~/.config/miniflux-adder/config.toml`):

```toml
miniflux_url = "https://rss.example.com"

# API token - plain text or a 1Password secret reference (op://vault/item/field)
api_token = "your-api-token-here"

# Category ID for new feeds (0 = uncategorized)
category_id = 0

# Remove successfully added and duplicate URLs from the feeds file (not supported for OPML or URL inputs)
remove_added = false

# OPML category handling: "ignore" (default) or "create"
#   ignore - all feeds go to category_id
#   create - use OPML category names, creating them in Miniflux if needed
opml_categories = "ignore"
```

If `api_token` contains an `op://` URI, it is resolved via `op read` using the [1Password CLI](https://developer.1password.com/docs/cli/).

### Precedence (highest to lowest)

1. Command-line flags (`--miniflux-url`, `--api-token`, `--category-id`, `--opml-categories`)
2. Environment variables (`MINIFLUX_URL`, `MINIFLUX_API_TOKEN`, `MINIFLUX_OPML_CATEGORIES`)
3. Config file

### Config File Lookup Order

1. Path given via `--config` flag
2. `./config.toml`
3. `$XDG_CONFIG_HOME/miniflux-adder/config.toml`

## Flags

| Flag | Description |
|------|-------------|
| `--miniflux-url` | Miniflux instance URL |
| `--api-token` | API token (plain text or `op://` reference) |
| `--category-id` | Category ID for new feeds (0 = uncategorized) |
| `--list-categories` | List available categories and exit |
| `--opml-categories` | OPML category handling: `ignore` (default) or `create` |
| `--config` | Path to config file |
