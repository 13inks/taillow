# Design decisions

Each entry is a choice someone could reasonably have made differently, and
why taillow made it this way.

## Serving

**Only `httpSrv.Serve(ln)`, where `ln` comes from `tsnet.Server.Listen`.**
`ListenAndServe` binds the host's own interfaces, which skips the tailnet and
with it every identity check. An early draft did exactly that. The listener
is the security boundary, so there is one way in.

**`/healthz` needs no identity.** It says only that the process is up, and it
is still reachable from the tailnet alone.

## Identity

**The caller is whoever `WhoIs` says owns the connection's address.**
WireGuard binds each packet's source address to the sending peer's key, so the
address cannot be forged from inside the tailnet. taillow never reads
`X-Forwarded-For` or any other header for identity.

**A tagged node is its tags, not its creator.** A CI runner tagged `tag:ci` was
registered by some person, but it acts for the tag. Reporting that person's
login would grant a machine the rights of whoever set it up.

**Unknown caller is 403, a failed lookup is 503.** The first is a verdict about
the caller; the second is taillow being unable to decide. Both refuse. The
lookup's error text goes to the log, never to the response.

## Permissions

**Permissions come from the tailnet policy file's grants, not a local file.**
The people who already decide who can reach what also decide who can use which
model, in one reviewed place. taillow reads them from the capability
`github.com/13inks/taillow/cap/llm`:

```jsonc
"grants": [{
  "src": ["group:eng"],
  "dst": ["tag:taillow"],
  "app": {"github.com/13inks/taillow/cap/llm": [
    {"models": ["claude-sonnet-5"], "dailyTokens": 200000}
  ]}
}]
```

**A missing capability is a refusal, not "no limits".**
`tailcfg.UnmarshalCapJSON` returns `(nil, nil)` when the capability is absent.
Code that reads "no values" as "nothing restricts this caller" lets everyone
through without a log line. taillow does not use that helper; an absent or
empty capability is `ErrNoGrant` and a 403.

**Decoding is strict, and a refusal names the field.**
- Keys must match exactly. Go's struct decoding matches keys case-insensitively,
  so `DailyTokens` would silently count. taillow decodes into a map and
  refuses any key other than `models` and `dailyTokens`.
- A repeated key is refused. `encoding/json` keeps the last one, so
  `{"dailyTokens":1,"dailyTokens":999}` would quietly grant 999.
- `dailyTokens` is required and must be positive. There is no
  zero-means-unlimited.
- One bad value fails the whole grant. A partial grant is a guess.

**Several matching grants merge as: union of models, smallest budget.**
Tailscale grants are additive, so a caller in two groups can use every model
either group allows. The budget goes the other way on purpose: adding a grant
must never raise a spend ceiling, so the smallest `dailyTokens` wins. The cost
is that stacking a generous grant on a default one does not raise the budget;
to give someone more, change the grant they already match.
