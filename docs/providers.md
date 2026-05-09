# Providers And Models

packetcode supports five provider slugs:

| Slug | Needs key | Notes |
| --- | --- | --- |
| `openai` | Yes | Uses the OpenAI API. |
| `gemini` | Yes | Uses the Google Gemini API. |
| `minimax` | Yes | Uses MiniMax's OpenAI-compatible API surface. |
| `openrouter` | Yes | Lists models and pricing from OpenRouter. |
| `ollama` | No | Uses a local Ollama server. |

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
PACKETCODE_GEMINI_API_KEY
PACKETCODE_MINIMAX_API_KEY
PACKETCODE_OPENROUTER_API_KEY
```

Environment variables win over config file keys.

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

[providers.gemini]
api_key = "AI..."
default_model = "gemini-2.5-pro"

[providers.ollama]
host = "http://localhost:11434"
default_model = "qwen2.5-coder:14b"
```

`host` is only used for Ollama. If omitted, packetcode uses the Ollama provider default.
