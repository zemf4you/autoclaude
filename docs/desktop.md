# Claude Desktop

Desktop gets its model menu from `claude.ai`, and the menu lists Claude
models only. Your models reach it in one of two ways; `desktop` in the config
picks which.

```toml
desktop = "slots" # or "entries"

[[model]]
id = "deepseek-v4-pro"
provider = "deepseek"
name = "DeepSeek V4 Pro"
slot = "claude-opus-4-6" # used with desktop = "slots"
```

`autoclaude install` asks which you want and writes your answer here. Run it
again to change your mind, then restart Claude Desktop.

## In place of Claude models: `desktop = "slots"`

The default. A model takes the menu entry of the Claude model named by its
`slot`: the entry keeps its name, and your model answers.

- With the config above, choosing *Opus 4.6* talks to DeepSeek.
- The older models under **More models** make good slots.
- A model without a `slot` is not in the Desktop menu.
- Nothing touches `claude.ai`, and no certificate is added to your account.

## As entries of their own: `desktop = "entries"`

Your models appear in the menu under their own names. Desktop gets the
current list each time it starts.

For that the gateway has to sit on Desktop's connection to `claude.ai`, and
Desktop, unlike Claude Code, trusts only the certificates of your user
account. So `autoclaude install` does two more things:

- **It adds one certificate to the trust store of your user account.** Your
  system asks for your password. The certificate can vouch for `claude.ai`
  only, is created on your machine, and its key never leaves it.
- **It points Claude Desktop, and nothing else, at the gateway** for its
  connections to `claude.ai`, through Desktop's own
  [`egressProxyPacUrl`](https://claude.com/docs/third-party/claude-desktop/network-proxy)
  setting. If the gateway is not running, Desktop connects directly and shows
  the standard menu.

| | Certificate | Desktop setting |
|---|---|---|
| macOS | login keychain | a configuration profile of your user; you confirm it in System Settings |
| Windows | your user's root store | `HKCU\SOFTWARE\Policies\Claude` |
| Linux | your user's NSS database | `/etc/claude-desktop/managed-settings.json`, written with `sudo` |

Verified on macOS. The Windows and Linux paths follow Anthropic's
documentation and have not been run yet: feel free to test them and open a
pull request.

### If the setting cannot be installed

For example, because your organization already manages Claude Desktop on the
machine. Start Desktop with `autoclaude desktop` instead of its icon: it
passes the same proxy on the command line.

### Before you turn it on

- The menu format belongs to Anthropic's servers and may change without
  notice. If it does, the menu falls back to the standard one.
- This changes a response of `claude.ai` on its way to the app. See
  [Anthropic's terms](../README.md#anthropics-terms).

### Turning it off

Set `desktop` back to `"slots"` and run `autoclaude install`: the certificate
and the setting are removed again.
