# Providers And Models

packetcode supports six provider slugs:

| Slug | Needs key | Notes |
| --- | --- | --- |
| `openai` | Yes | Uses the OpenAI API. |
| `anthropic` | Yes | Uses the Anthropic Claude Messages API. |
| `gemini` | Yes | Uses the Google Gemini API. |
| `minimax` | Yes | Uses MiniMax's OpenAI-compatible API surface. |
| `openrouter` | Yes | Lists models and pricing from OpenRouter. |
| `ollama` | No | Uses a reachable Ollama server. |
| custom slug | Optional | Uses a user-configured OpenAI-compatible `/v1` endpoint. |

## Configure Keys

First run configures one provider. To add or update another provider later:

1. Open the provider picker with `Ctrl+P` or `/provider`.
2. Focus the provider row.
3. Press `Ctrl+A`.
4. Paste the API key.

The key is validated, saved to `~/.packetcode/config.toml`, and the picker reopens with the row marked `key present`.

`/provider add` opens that same picker. `/provider add <slug>` opens the same key prompt directly for a provider slug.

You can also set keys with environment variables:

```text
PACKETCODE_OPENAI_API_KEY
PACKETCODE_ANTHROPIC_API_KEY
PACKETCODE_GEMINI_API_KEY
PACKETCODE_MINIMAX_API_KEY
PACKETCODE_OPENROUTER_API_KEY
PACKETCODE_MY_PROVIDER_API_KEY
```

Environment variables win over config file keys. Custom provider slugs are
normalized to `PACKETCODE_<SLUG>_API_KEY`, with non-alphanumeric characters
converted to `_`; set `api_key_env` to use a different variable.

## Switch Providers

| Action | Command |
| --- | --- |
| Open provider picker | `Ctrl+P` or `/provider` |
| Add/update provider key | `Ctrl+P` then `Ctrl+A`, `/provider add`, or `/provider add <slug>` |
| Switch directly | `/provider <slug>` |
| Open model picker | `Ctrl+M` or `/model` |
| Switch model directly | `/model <id>` |

When switching providers, packetcode uses that provider's saved `default_model`. If no default model is saved, it falls back to the first model returned by the provider's model list. The chosen provider/model is persisted as the new default.

## Config Example

```toml
[default]
provider = "openai"
model = "gpt-5.5"

[providers.openai]
api_key = "sk-..."
default_model = "gpt-5.5"

[providers.anthropic]
api_key = "sk-ant-..."
default_model = "claude-opus-4-7"

[providers.gemini]
api_key = "AI..."
default_model = "gemini-2.5-pro"

[providers.minimax]
api_key = "sk-..."
default_model = "MiniMax-M2.7-highspeed"

[providers.ollama]
host = "http://localhost:11434"
default_model = "qwen2.5-coder:14b"
```

`host` is only used for Ollama. If omitted, packetcode defaults to `http://localhost:11434`. A bare host like `ollama.internal` is normalized to `http://ollama.internal:11434`. You can also set `PACKETCODE_OLLAMA_HOST` to override the saved host for one machine.

## Custom OpenAI-Compatible Providers

Any service that implements OpenAI-compatible `/models` and
`/chat/completions` endpoints can be added as a provider:

```toml
[providers.localai]
type = "openai_compatible"
display_name = "LocalAI"
base_url = "http://localhost:8080/v1"
default_model = "coder-large"
api_key_required = false

[[providers.localai.models]]
id = "coder-large"
context_window = 32768
supports_tools = true
```

For hosted gateways, keep `api_key_required` omitted or set it to `true` and
store the key in config or an env var:

```toml
[providers.acme]
type = "openai_compatible"
display_name = "Acme Gateway"
base_url = "https://llm.acme.example/v1"
api_key_env = "ACME_LLM_TOKEN"
default_model = "acme-coder"
headers = { "X-Workspace" = "packetcode" }
```

Static `models` entries are used as a fallback when `/models` is unavailable
or incomplete. Unknown custom model prices default to zero and context defaults
to 128k tokens unless configured.
