# miniflux-adder

Single-file Go CLI (`main.go`) that bulk-adds RSS/Atom feeds to a Miniflux instance.

## Build & Run

```bash
go build
./miniflux-adder myfeeds.txt
./miniflux-adder --list-categories
```

## Project Layout

- `main.go` - Entire application (single file)
- `config.toml` - Local config (gitignored, see `config.toml.example`)
- `myfeeds.txt` - Feed URL list (one per line, `#` comments allowed)

## Key Details

- Config precedence: flags > env vars > config file
- `api_token` values starting with `op://` are resolved via `op read` (1Password CLI)
- Config file search: `./config.toml` then `$XDG_CONFIG_HOME/miniflux-adder/config.toml`
- No tests exist
- Uses charmbracelet libs for terminal UI (spinners, styled output)
- `remove_added = true` rewrites the feeds file keeping only failed URLs
- Exit code 1 if any feeds fail to add

## Dependencies

- `github.com/BurntSushi/toml` - Config parsing
- `github.com/charmbracelet/huh/spinner` - Progress spinners
- `github.com/charmbracelet/lipgloss` - Terminal styling
- `miniflux.app/v2/client` - Miniflux API client
