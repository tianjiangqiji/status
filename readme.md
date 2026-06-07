# status

English | [中文](./readme_ZH.md)

A lightweight AI model status monitoring panel. Single-file deployment, zero external dependencies (except SQLite). Supports multi-group, multi-model monitoring with Uptime Kuma-compatible push. Compatible with NewAPI.

## Quick Start

```bash
# Copy config template
cp config.template.json config.json
# Edit config.json with your models and API keys
# Run directly
go run .
# Visit http://localhost:8080
```

## Build

```powershell
# Windows
$env:CGO_ENABLED="0"
go build -ldflags "-s -w" -o status-windows-amd64.exe .

# Linux cross-compile
$env:CGO_ENABLED="0"
$env:GOOS="linux"
$env:GOARCH="amd64"
go build -ldflags "-s -w" -o status-linux-amd64 .
```

Build output is a single executable + `status.html`. Just place them in the same directory to deploy.

## Configuration

Config file is `config.json`. See `config.template.json` for a template.

### Full Structure

```jsonc
{
  "port": 8080,                      // Listen port, default 8080, requires restart
  "interval": 60,                    // Check interval in seconds, default 60
  "frontend": {                      // Frontend display config
    "title": "My API Status",        // Page title (used for both <title> and <h1>)
    "icon": "https://example.com/icon.png",  // Favicon and logo, empty to hide
    "subtitle": "Real-time service monitoring", // Subtitle below the heading
    "footer": "&copy; 2026 <a href=\"/\">Home</a>"  // Footer, supports HTML
  },
  "kuma": {                          // Uptime Kuma push config (optional)
    "baseURL": "https://status.example.com",
    "slug": "my-status-page"
  },
  "groups": [ /* see below */ ]
}
```

### groups Structure

```jsonc
{
  "name": "OpenAI",              // Top-level group name
  "subGroups": [
    {
      "name": "GPT-4o",          // Sub-group name (displayed as model name on page)
      "kumaSlug": "gpt-4o",      // Uptime Kuma independent push slug (optional)
      "models": [
        {
          "id": "gpt-4o",        // Model ID for API calls
          "provider": "openai-compat",  // Protocol type
          "baseURL": "https://api.openai.com/v1",
          "key": "sk-xxx",       // API Key
          "timeout": 30          // Timeout in seconds, default 30
        }
      ]
    }
  ]
}
```

### Supported Providers

| provider | Description | Endpoint |
|---|---|---|
| `openai-compat` | OpenAI-compatible protocol (default) | `POST {baseURL}/chat/completions` |
| `openai-response` | OpenAI Responses API | `POST {baseURL}/responses` |
| `google` | Google Gemini API | `POST {baseURL}/models/{id}:generateContent` |
| `anthropic` | Anthropic Claude API | `POST {baseURL}/messages` |

### Configuration Examples

**OpenAI-compatible (most proxy services):**

```json
{
  "id": "gpt-4o-mini",
  "provider": "openai-compat",
  "baseURL": "https://api.example.com/v1",
  "key": "sk-your-key",
  "timeout": 30
}
```

**Anthropic Claude:**

```json
{
  "id": "claude-sonnet-4-20250514",
  "provider": "anthropic",
  "baseURL": "https://api.anthropic.com",
  "key": "sk-ant-your-key",
  "timeout": 30
}
```

**Google Gemini:**

```json
{
  "id": "gemini-2.5-flash",
  "provider": "google",
  "baseURL": "https://generativelanguage.googleapis.com/v1beta",
  "key": "your-api-key",
  "timeout": 30
}
```

**OpenAI Responses API (Codex):**

```json
{
  "id": "gpt-5.4-mini",
  "provider": "openai-response",
  "baseURL": "https://api.openai.com/v1",
  "key": "sk-your-key",
  "timeout": 30
}
```

## API Endpoints

| Endpoint | Description |
|---|---|
| `GET /` | Status dashboard page |
| `GET /api/status` | Status data (polled by frontend) |
| `GET /api/config` | Config info (interval + frontend) |
| `GET /api/status-page/{slug}` | Uptime Kuma-compatible status page |
| `GET /api/status-page/heartbeat/{slug}` | Uptime Kuma-compatible heartbeat data |

### `/api/status` Response

```jsonc
{
  "title": "My API Status",
  "overall": { "status": "up", "label": "Running" },
  "last_completed_at": "2026-06-07T12:00:00Z",
  "checking": false,
  "sections": [
    {
      "name": "OpenAI",
      "rows": [
        {
          "name": "gpt-4o",
          "sub_name": "GPT-4o",
          "status": "up",
          "label": "Normal",
          "health_score": 99.5,
          "latency_ms": 230,
          "last_checked_at": "2026-06-07T12:00:00Z",
          "history": [{ "s": "up", "v": 1.0 }]
        }
      ]
    }
  ]
}
```

`status` values: `up` / `down` / `degraded` / `unknown`

### `/api/config` Response

```jsonc
{
  "interval": 60,
  "frontend": {
    "title": "My API Status",
    "icon": "https://example.com/icon.png",
    "subtitle": "...",
    "footer": "..."
  }
}
```

## Uptime Kuma Integration

Two push modes supported:

1. **Global push**: Set `kuma.baseURL` and `kuma.slug` to push the average status of all models to one slug
2. **Per-subgroup push**: Set `kumaSlug` in each subGroup for independent push per group

Also compatible with NewAPI and other tools using the Uptime Kuma public status page protocol, providing `/api/status-page/` and `/api/status-page/heartbeat/` endpoints.

## Frontend Customization

The `frontend` config section controls all branding elements without modifying HTML:

- `title`: Browser tab title + page main heading
- `icon`: Favicon + page logo (hidden when empty)
- `subtitle`: Text below the heading
- `footer`: Footer content, supports inline HTML

## Tech Stack

- Go (single file, `modernc.org/sqlite` CGO-free SQLite driver)
- Pure HTML/CSS/JS, no frontend framework dependencies
- Storage: local SQLite (`status.db`)

## License

[MIT](./LICENSE)

## Star History

<a href="https://www.star-history.com/?repos=tianjiangqiji%2Fstatus&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=tianjiangqiji/status&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=tianjiangqiji/status&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=tianjiangqiji/status&type=date&legend=top-left" />
 </picture>
</a>