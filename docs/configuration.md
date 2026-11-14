# Configuration

The config is `~/.autoclaude/config.toml`. `autoclaude edit` opens it and
applies it when you are done; `autoclaude reload` applies a file you edited
yourself. Keep it private: it holds your provider keys.

```toml
listen = "127.0.0.1:8484"
desktop = "slots"

[providers.deepseek]
base_url = "https://api.deepseek.com/anthropic"
api_key = "sk-..."

[providers.local]
base_url = "http://127.0.0.1:8000"

[[model]]
id = "deepseek-v4-pro"
provider = "deepseek"
name = "DeepSeek V4 Pro"
context = 1000000
slot = "claude-opus-4-6"

[[model]]
id = "qwen"
provider = "local"
upstream = "Qwen/Qwen3-Coder-30B-A3B-Instruct"
```

## The gateway

| Setting | What it is |
|---|---|
| `listen` | The address the gateway listens on. It must be a loopback address. Default `127.0.0.1:8484` |
| `desktop` | How your models appear in Claude Desktop: `"slots"`, in place of Claude models, or `"entries"`, as entries of their own. `autoclaude install` asks and writes it; see [Claude Desktop](desktop.md) |
| `context_window` | The context window, in tokens, that Claude Code assumes for your models that have no `context`. See [context windows](how-it-works.md#context-windows) |

After changing `listen` or `desktop`, run `autoclaude install`. Everything
else takes `autoclaude reload`.

## Providers

A `[providers.<name>]` block for each service that runs your models. The
name is yours to choose. A provider must speak the Anthropic Messages API.

| Setting | What it is |
|---|---|
| `base_url` | Where the provider serves the API, without `/v1/messages` |
| `api_key` | Your key. Leave it out for a local server that needs none |

Some addresses:

| Provider | `base_url` |
|---|---|
| DeepSeek | `https://api.deepseek.com/anthropic` |
| OpenRouter | `https://openrouter.ai/api` |
| vLLM, llama.cpp, Ollama | the address of your server, such as `http://127.0.0.1:8000` |

## Models

A `[[model]]` block for each of your models. A model that is not listed goes
to Anthropic on your subscription.

| Setting | What it is |
|---|---|
| `id` | What you pick in Claude Code: `/model deepseek-v4-pro`. No spaces |
| `provider` | The name of one of your providers |
| `upstream` | The model's name at the provider, if it differs from `id` |
| `name` | What to call the model in menus. The `id` if left out |
| `context` | Its context window in tokens, from 100000 to 10000000. Claude Code shows it and compacts the conversation by it |
| `slot` | With `desktop = "slots"`: the Claude model whose entry in Desktop's menu it takes |
| `extra_body` | Fields to add to every request for the model: the options its provider takes beyond the Messages API |

Good slots are the older models under **More models** in Desktop's menu:
`claude-opus-4-6`, `claude-opus-4-7`, `claude-opus-4-8`, `claude-sonnet-4-6`,
`claude-opus-5`, `claude-sonnet-5`, `claude-fable-5`, `claude-fable-5-1`,
`claude-haiku-4-5-20251001`.

## OpenRouter

OpenRouter spreads the requests for a model over the companies that host it,
and each host has a prompt cache of its own. Claude Code sends the whole
conversation with every request, so without the cache every message costs
the full price. Keep a model on one host:

```toml
[[model]]
id = "deepseek-v4.1-flash"
provider = "openrouter"
upstream = "deepseek/deepseek-v4.1-flash"
extra_body = { provider = { only = ["DeepInfra"] } }
```

The hosts of a model, their prices and what else `provider` takes are in
OpenRouter's [provider routing](https://openrouter.ai/docs/features/provider-routing)
guide. OpenRouter does not count tokens ahead of a request, which Claude Code
asks for; the conversation is not affected.

## Mistakes

A mistake costs the entry it is in, not the gateway. `autoclaude reload` and
`autoclaude status` name what was left out and why; the rest keeps working.
