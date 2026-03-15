# miniflux-adder

Bulk-add RSS/Atom feed URLs to a [Miniflux](https://miniflux.app/) instance from a text file.

## Install

```bash
go install github.com/tmcarr/miniflux-adder@latest
```

## Usage

```bash
# Build from source
go build

# Add feeds from a file
./miniflux-adder myfeeds.txt

# List available categories (useful for finding category IDs)
./miniflux-adder --list-categories

# Override config with flags
./miniflux-adder --miniflux-url https://rss.example.com --api-token YOUR_TOKEN myfeeds.txt
```

## Feed File Format

One URL per line. Empty lines and lines starting with `#` are ignored.

```
https://blog.golang.org/feed.atom
https://github.blog/feed/
# This is a comment
https://www.nasa.gov/rss/dyn/breaking_news.rss
```

## Configuration

Create a `config.toml` in the working directory or at `$XDG_CONFIG_HOME/miniflux-adder/config.toml` (defaults to `~/.config/miniflux-adder/config.toml`):

```toml
miniflux_url = "https://rss.example.com"

# API token - plain text or a 1Password secret reference (op://vault/item/field)
api_token = "your-api-token-here"

# Category ID for new feeds (0 = uncategorized)
category_id = 0

# Remove successfully added and duplicate URLs from the feeds file
remove_added = false
```

If `api_token` contains an `op://` URI, it is resolved via `op read` using the [1Password CLI](https://developer.1password.com/docs/cli/).

### Precedence (highest to lowest)

1. Command-line flags (`--miniflux-url`, `--api-token`, `--category-id`)
2. Environment variables (`MINIFLUX_URL`, `MINIFLUX_API_TOKEN`)
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
| `--config` | Path to config file |
