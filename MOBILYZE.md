# mobilyze-llc/CLIProxyAPI

A private copy of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that adds the `soonest-reset` credential selector (MASTRA-765). The selector sends each new session to the account whose weekly window resets soonest, among accounts that still have quota.

- Base: upstream tag `v8.0.13` (d7914afd). Our commits sit on top of it on `main`.
- To move to a new upstream tag: `git fetch upstream --tags`, then rebase our commits onto the tag.
- Never push upstream tags to this repo. Upstream's release and Docker workflows were removed, but a pushed upstream tag still carries them in its own tree.
- Our releases use tags of the form `v8.0.13-mob.N`.
- Selector source: zsprackett/CLIProxyAPI commits `faa4ccff`, `01ad0021` and `67f5749d` (MIT), with authorship kept. `4d7b2e84` (model-scoped weekly windows) was left out.
