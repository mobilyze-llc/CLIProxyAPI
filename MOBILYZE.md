# mobilyze-llc/CLIProxyAPI

A private copy of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that adds the `soonest-reset` credential selector (MASTRA-765). The selector sends each new session to the account whose weekly window resets soonest, among accounts that still have quota.

- Base: upstream tag `v8.0.13` (d7914afd). Our commits sit on top of it on `main`.
- To move to a new upstream tag: `git fetch upstream --tags`, then rebase our commits onto the tag.
- Never push upstream tags to this repo. Upstream's release and Docker workflows were removed, but a pushed upstream tag still carries them in its own tree.
- Our releases use tags of the form `v8.0.13-mob.N`.
- Selector source: zsprackett/CLIProxyAPI commits `faa4ccff`, `01ad0021` and `67f5749d` (MIT), with authorship kept. `4d7b2e84` (model-scoped weekly windows) was left out.

## Subscription reserves

Configure a percentage to keep available for direct use of a Claude or Codex OAuth account:

```yaml
routing:
  quota-reserves:
    - provider: claude
      email: personal@example.com
      percent: 20
    - provider: codex
      email: personal@example.com
      percent: 20
```

A 20% reserve excludes the account once either its active five-hour or weekly window is at
least 80% used. This applies before session affinity, pinned credentials and selector fallback,
so existing sticky sessions move to another eligible account. If none are available the request
fails; the reserve is never relaxed. Set `percent: 0` or remove the rule to disable it. Account
matching ignores email case and whitespace. Reserves apply to all models using the matching account.

Protected accounts wait for usable window usage, rather than account metadata alone. Claude
requires both windows; Codex plans that report only a weekly window remain supported. Reserve
enforcement retains each observed window across partial readings, credential refreshes and
restarts. A missing window or an unanchored zero cannot clear a known high reading. A valid
reading with a reset time can clear it, and a known reset still releases the account when
later readings omit that window. High usage without a reset time stays blocked until a usable
new reading clears it. Usage is observed from upstream responses and the existing five-minute
background usage reader, including usage outside the proxy. Reporting delays and requests already in flight can overshoot the threshold. An expired
window no longer blocks the account; a newer reading below the threshold also restores eligibility.
Reserves do not cancel requests already in progress or prevent direct use outside the proxy.

With reserves configured, credential selection uses request-time filtering rather than the
cached scheduler. Other accounts retain their existing availability and routing rules.

Reserve rules require local credential selection. An active reserve combined with `home.enabled`
is rejected at startup, SDK construction and reload; Home dispatch also fails closed if an
invalid combination is installed directly through the low-level manager API.
