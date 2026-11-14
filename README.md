# AutoClaude

Use your own models in the official Claude Code — the CLI and Claude Desktop —
next to Claude on your subscription, and switch between them in the middle of
a conversation.

> [!WARNING]
> Anthropic does not support this, and using it with your subscription is
> at your own risk. Read [Anthropic's terms](#anthropics-terms) first.

AutoClaude is a small local gateway. Claude Code sends it every request; the
gateway looks at the `model` field and forwards the request either to
Anthropic, untouched, or to the provider you configured for that model.

```
Claude Code ──► autoclaude ──┬─► api.anthropic.com   (any model not in the config)
                             ├─► DeepSeek
                             └─► vLLM, llama.cpp, OpenRouter, …
```

## What it does

- **Routes by model, nothing else.** You pick the model in Claude Code; the
  gateway never second-guesses that choice.
- **Leaves Claude alone.** Requests for models that are not in the config
  go to Anthropic as they are, with the client's own credentials. The only
  thing ever removed from them is another model's thinking.
- **Keeps your account to Anthropic.** A third-party provider never sees
  your subscription token, nor the account, device and session identifiers
  Claude Code attaches to its requests.
- **Keeps the conversation valid across a model switch.** The history lives
  in Claude Code, and after a switch it holds things the new model would
  reject. The gateway fixes the copy of the request it forwards:
  - *Thinking blocks* are accepted back only by the model that produced
    them. The gateway marks the blocks of third-party models and sends each
    model only its own. If a model still rejects a history it did not write
    alone, the request is retried once without thinking, so the conversation
    can always go on.
  - *Server-side history.* Talking to Anthropic, Claude Code may send only
    the new messages and rely on the server for the rest. A third-party
    model gets the whole history instead.
  - *System messages in the middle of the history*, an Anthropic extension,
    are turned into user messages for third-party models.

Providers must speak the Anthropic Messages API. DeepSeek, OpenRouter, vLLM,
llama.cpp and Ollama do; for an OpenAI-only endpoint, put a translator such
as LiteLLM in front of it.

## Install

Requires Go 1.26.4 or newer.

```bash
go install github.com/zemf4you/autoclaude/cmd/autoclaude@latest
```

## Set up

```bash
autoclaude install
```

The first run creates `~/.autoclaude/config.toml`, with every setting
explained inside. Describe your providers and models there:

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com/anthropic"
api_key = "sk-..."

[providers.local]
base_url = "http://127.0.0.1:8000"

[models]
deepseek-v4-pro = "deepseek"
qwen = "local/Qwen/Qwen3-Coder-30B-A3B-Instruct"

[menu] # names for the model menu of Claude Desktop
deepseek-v4-pro = "DeepSeek V4 Pro"
qwen = "Qwen 3 Coder"
```

Then run `autoclaude install` again. It lists what it is about to change on
your machine and asks before it does (`-y` skips the question). That is the
whole setup. Repeat it whenever the config changes; `autoclaude uninstall`
undoes all of it. Restart Claude Code and Claude Desktop afterwards.

### What install does

1. **Runs the gateway as a service of your user account.** It starts at
   login and is restarted if it ever stops. On macOS and Linux this needs no
   administrator rights; on Windows, services are system-wide and it does.
   Logs go to `~/.autoclaude/`.
2. **Points Claude Code at the gateway.** It adds `HTTPS_PROXY` and
   `NODE_EXTRA_CA_CERTS` to the `env` block of `~/.claude/settings.json` and
   leaves the rest of the file untouched. This covers the CLI and the Code
   sessions of Claude Desktop. The gateway decrypts connections to
   `api.anthropic.com` only, with a certificate authority created on your
   machine that only Claude Code is told to trust; everything else is
   tunnelled through untouched.
3. **With a `[menu]`, puts your models into the menu of Claude Desktop.**
   See below.

The gateway adds your provider keys to requests and passes your subscription
credentials on to Anthropic, so it only listens on a loopback address and
refuses requests made by web pages.

### In the CLI

`/model qwen` and `/model opus` switch back and forth, and
`claude --model qwen` starts with your model. To see your models in the
`/model` picker, list them in `~/.claude/settings.json`
([`modelPicker`](https://code.claude.com/docs/en/settings-reference#modelpicker)):

```json
{
  "modelPicker": {
    "options": [
      { "model": "qwen", "label": "Qwen 3 Coder", "description": "Local vLLM" }
    ]
  }
}
```

### In Claude Desktop

Desktop gets its model menu from `claude.ai` and lists Claude models only.
There are two ways to pick your own model there. They are alternatives: a
config uses one or the other.

#### Your own entries in the menu

With a `[menu]` in the config, your models appear in the menu under the
names you gave them. For that the gateway has to sit on Desktop's connection
to `claude.ai` as well, and Desktop, unlike Claude Code, trusts only the
certificates of your user account. So `autoclaude install` does two more
things:

- It adds one certificate to the trust store of your user account, and your
  system asks for your password. The certificate can vouch for `claude.ai`
  only, is created on your machine, and its key never leaves it.
- It points Claude Desktop, and nothing else, at the gateway for its
  connections to `claude.ai`, through Desktop's own
  [`egressProxyPacUrl`](https://claude.com/docs/third-party/claude-desktop/network-proxy)
  setting. If the gateway is not running, Desktop connects directly and
  shows the standard menu.

| | Certificate | Desktop setting |
|---|---|---|
| macOS | login keychain | a configuration profile of your user; you confirm it in System Settings |
| Windows | your user's root store | `HKCU\SOFTWARE\Policies\Claude` |
| Linux | your user's NSS database | `/etc/claude-desktop/managed-settings.json`, written with `sudo` |

Verified on macOS; the Windows and Linux paths follow Anthropic's
documentation and have not been tested.

If the setting cannot be installed, for example because your organization
already manages Claude Desktop on the machine, start Desktop with
`autoclaude desktop` instead of its icon: it passes the same proxy on the
command line.

Two things to know. The menu format belongs to Anthropic's servers and may
change without notice; if it does, the menu falls back to the standard one.
And this changes a response of `claude.ai` on its way to the app; see
[Anthropic's terms](#anthropics-terms) before you turn it on.

#### Borrowed entries, no certificate

Without a `[menu]`, give your model the ID of a Claude model you do not use.
The older models under **More models** are good candidates:

```toml
[models]
claude-opus-4-6 = "deepseek/deepseek-v4-pro"
claude-sonnet-4-6 = "local/Qwen/Qwen3-Coder-30B-A3B-Instruct"
```

The menu entry keeps its name: choosing *Opus 4.6* now talks to DeepSeek.
Nothing touches `claude.ai`, and no certificate is added to your account.

### Without install

To try the gateway without changing anything, run `autoclaude` in the
foreground and point one CLI session at it. No certificate is involved:

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:8484 claude --model qwen
```

## What a model switch costs

- The prompt cache starts over: a provider cannot reuse another provider's
  cache.
- The new model does not see the previous model's thinking, only its
  visible output and tool calls.
- If a provider refuses the history, typically because the switch happened
  in the middle of a tool loop, the rest of that loop runs without thinking.
  The gateway logs a warning when this happens, and tries the full history
  again on your next message.

## Limitations

- A provider has to implement what Claude Code asks of the Messages API:
  streaming, tool use and, for the context meter, `count_tokens`.
- When Claude Desktop cancels a request to a third-party model before the
  first byte of the answer, the provider keeps generating until it starts
  to respond.

## Development

```bash
make test   # go vet and the race-enabled test suite; providers are faked
make build
```

- `internal/gateway` — routing and request repair, an `http.RoundTripper`.
- `internal/proxy` — accepts direct and proxied traffic on one address.
- `internal/menu` — the model menu of Claude Desktop.
- `internal/claudecode`, `internal/desktop` — what `install` sets up in
  Claude Code and in Claude Desktop.
- `internal/config` — the configuration file.

## Anthropic's terms

AutoClaude is not affiliated with Anthropic, and Anthropic does not support
what it does. Before you use it with your subscription, know what its terms
say. This is a summary, not legal advice; read the documents yourself.

> [!WARNING]
> Anthropic may suspend or terminate access to its services, subscriptions
> included, "at any time without notice"
> ([Consumer Terms](https://www.anthropic.com/legal/consumer-terms), §12).
> You use AutoClaude at your own risk.

What applies to everything AutoClaude does:

- Anthropic "doesn't support routing Claude Code to non-Claude models
  through any gateway"
  ([LLM gateways](https://code.claude.com/docs/en/llm-gateway)). The same
  page describes a gateway in front of a subscription as a working setup:
  "a saved claude.ai login remains the active credential."
- Your subscription credentials pass through the gateway on their way to
  Anthropic. It runs on your machine, does not store them and sends them
  nowhere else, but Anthropic's rules for developers say they may not
  "collect, store, or intermediate Claude.ai credentials or session tokens"
  ([Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)).

What applies to the menu of Claude Desktop in addition:

- With a `[menu]`, the gateway also decrypts Desktop's connection to
  `claude.ai` and adds entries to a response of that service. The Consumer
  Terms (§3) forbid to "interfere with, or disrupt" the services and to
  bypass "any of our systems or protective measures"; whether adding menu
  entries on your own machine falls under that is for Anthropic to judge.
- The gateway does not disguise itself, patch the app or defeat any of its
  checks. The proxy it uses is Desktop's own documented setting.

If that is more than you want to risk, leave `[menu]` out and use the
borrowed entries: nothing touches `claude.ai` then.
