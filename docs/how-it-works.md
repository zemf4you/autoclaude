# How it works

- [Routing](#routing)
- [What a provider receives](#what-a-provider-receives)
- [Switching models](#switching-models)
- [Context windows](#context-windows)
- [What install changes](#what-install-changes)
- [Mistakes in the config](#mistakes-in-the-config)
- [Safety of the gateway](#safety-of-the-gateway)
- [Trying it without install](#trying-it-without-install)

## Routing

The gateway reads the `model` field of a request and nothing else. It does
not look at the prompt, the tools or the mode Claude Code is in, and it never
second-guesses the model you picked.

| The model is | The request goes to | With |
|---|---|---|
| not in your config | `api.anthropic.com` | the client's own credentials |
| in your config | that model's provider | your key for that provider |

## What a provider receives

A third-party provider gets the conversation and your key for it. It does
not get:

- your subscription token;
- the account, device and session identifiers Claude Code attaches to its
  requests;
- any header beyond the ones the Messages API needs.

## Switching models

The conversation lives in Claude Code, which sends all of it with every
request. After a switch, that history holds things the new model would
reject. The gateway repairs the copy of the request it forwards; the history
in Claude Code is never changed.

### Thinking

A model's thinking comes back to it in later requests, and a model accepts
only its own: Claude refuses a block that DeepSeek wrote, and the other way
round.

- The gateway marks the thinking of your models with the model's name as it
  passes to Claude Code.
- On every request it sends a model only the blocks that model wrote.
  Claude's own blocks go back to Claude byte for byte.
- This is the one change ever made to a request for a Claude model: the
  thinking of your models is left out of it.

Some providers still refuse a history they did not write alone, typically
when the switch happened in the middle of a tool loop. The request is then
retried once without thinking, so the conversation can always go on. The
rest of that loop runs without thinking, the gateway logs a warning, and it
tries the full history again on your next message.

### History kept by Anthropic

Talking to Anthropic, Claude Code may send only the new messages and rely on
the server for the rest. A provider has no such memory, so the gateway asks
Claude Code to send the whole history again.

### System messages in the middle

Anthropic allows system messages between the turns of a conversation; other
providers do not. For them these become user messages.

### What a switch costs

- The prompt cache starts over: a provider cannot reuse another provider's
  cache.
- The new model does not see the previous model's thinking, only its visible
  output and its tool calls.

## Context windows

Claude Code needs to know a model's window to show how full it is and to
compact the conversation before it overflows.

**`context` on a model**, from 100,000 to 10,000,000 tokens. A Claude Code
session asks Anthropic's API for the list of models it may use, and a row of
that list carries the model's window. The gateway sets the window in the rows
of your models and adds rows for the models the API does not list. With
`desktop = "slots"` it also sets it on the Claude entry your model takes.

**`context_window` for the rest.** Claude Code keeps one window for all the
models it does not know, a very large one by default. `context_window` sets
it; give the smallest window among your models that have no `context`.

**Without either,** the conversation is compacted once the provider rejects
a request as too long: Claude Code recognizes the overflow errors of the
common providers and runtimes. A provider that truncates the conversation
silently gives it nothing to react to.

Whether Claude Code uses the list of models is decided by Anthropic and can
change at any time. This was checked in Claude Desktop, not in the CLI.

## What install changes

1. **A service of your user account** runs the gateway. It starts at login
   and is restarted if it stops. macOS and Linux need no administrator
   rights; on Windows services are system-wide, and it does. Logs go to
   `~/.autoclaude/`.
2. **`~/.claude/settings.json`** gets `HTTPS_PROXY` and `NODE_EXTRA_CA_CERTS`
   in its `env` block and your models under `modelPicker`. The rest of the
   file is left as it is. This covers the CLI and the Code sessions of Claude
   Desktop.
3. **Claude Desktop** is set up if the config says `desktop = "entries"`; see
   [Claude Desktop](desktop.md).
4. **Your shell's startup file** (`~/.zshrc`, `~/.bashrc` or a file of fish)
   gets a few marked lines that load the Tab completion of `autoclaude`.

`autoclaude uninstall` removes all of it.

`listen` and `desktop` change what is set up on your machine, so run
`autoclaude install` after changing them. For everything else in the config,
`autoclaude reload` is enough.

## Mistakes in the config

A mistake does not take Claude Code down with the gateway.

- A model or provider that cannot be used is left out. `autoclaude status`,
  `autoclaude reload` and the log say which and why; the rest keeps working.
- A file that cannot be read at all leaves the running gateway with the
  configuration it had. A gateway that is starting passes everything to
  Anthropic.
- A session that is still on a model you removed is told so.

## Safety of the gateway

- **It decrypts only what it must.** Connections to `api.anthropic.com`, and
  with `desktop = "entries"` to `claude.ai`. Everything else is tunnelled
  through untouched.
- **Its certificates are yours.** Each certificate authority is created on
  your machine, can vouch for one host only, and its key never leaves the
  machine. The one for `api.anthropic.com` is trusted by Claude Code alone.
- **It is not reachable from outside.** It carries your provider keys and
  passes your subscription credentials on to Anthropic, so it listens on a
  loopback address only and refuses requests made by web pages.

## Trying it without install

Run the gateway in the foreground and point one CLI session at it. Nothing
on your machine changes and no certificate is involved:

```bash
autoclaude run
```

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:8484 claude --model deepseek-v4-pro
```
