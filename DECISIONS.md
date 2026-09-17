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

**Strict decoding lives in one package.** `internal/strictjson` does the exact
key and repeated key checks for both the grant and the request body. Two
copies of a security check drift apart; the second copy is where the bug goes.

## The request

**The checks run in a fixed order: identity, grant, request, model, provider,
budget, upstream.** Nothing in the body is looked at until the tailnet has said
who sent it, and nothing is reserved until the request is one taillow could
actually forward. Each step has its own status and its own reason, so a caller
can tell "you are nobody" from "you may not use that model" from "you are out
of tokens".

**The request body is decoded as strictly as a grant.** An unknown field is a
400 that names it. A caller who sends `temperature` should learn it is ignored
by being told, not by wondering why their output did not change.

**`max_tokens` is required and capped at 16000.** taillow does not stream, and
a non-streaming call asking for much more can outlive the upstream's HTTP
timeout. Required, because the reservation below depends on it.

## Budget

**Reserve an upper bound before the call, settle to the real cost after.**
Charging only afterwards lets ten concurrent requests all pass the check before
any of them is counted. The reservation is `max_tokens + len(prompt) + 64`: a
token is at least one byte, so the prompt's byte length can only overestimate.
The cost of being safe is that a caller near their limit can be refused a
request that would have fit. No tokenizer is involved, so there is no estimate
to be wrong.

**A failed upstream call refunds everything; a declined one does not.** A 5xx
or an unreachable provider billed nothing. A model that declines to answer was
still paid for, so the ledger records it.

**An overrun is recorded even past the limit.** If a provider ever reports
more than was reserved, the ledger takes the real number. It is a record of
what happened first and a limit second.

**A person is budgeted by login, a tagged node by its sorted tags.** The budget
follows a person across their devices. For a tagged node it follows the tag,
for the same reason identity does.

**The ledger is in memory.** A restart resets the day's counts. That is a
stated limit of a sample, not an oversight: persistence is a non-goal here, and
the audit log is the durable record.

## Upstream

**Anthropic through the official SDK, Ollama through `net/http`.** Where a
maintained SDK exists it owns the wire format, the error type and the auth
lookup. Ollama's API is two JSON shapes, so a dependency would cost more than
the code it replaces.

**`claude-*` goes to Anthropic, everything else to Ollama.** One prefix rule.
A routing table is a non-goal until there is a third provider to route to.

**No retries.** The SDK retries 429 and 5xx twice by default; taillow turns
that off. A quiet retry holds the caller's reservation for three attempts and
then reports one. The caller sees the first real answer and decides.

**No server-side model fallback.** The Messages API can re-serve a declined
request on another model. Here that would answer with a model the caller's
grant never named, which is the one thing this gateway exists to prevent.

**A 502 carries the upstream's own status.** `{"upstream": {"provider":
"anthropic", "status": 529}}` sends someone to the right status page. A bare
502 sends them to debug taillow.

**A model's refusal is a 422, not a 200.** The provider reports a declined
request as a success with no text. Passing that through would be the silent
success this project is named against.

**Token counts are the upstream's.** taillow never counts tokens itself. All
three of Anthropic's input counters (uncached, cache write, cache read) are
summed, because all three are billed.

## Audit

**One JSON line per request, refusals included, written before the response.**
A refused request is often the interesting one.

**If the audit line cannot be written, the caller gets a 500, not the answer.**
By then the tokens are spent. Returning the answer anyway would make the audit
log optional in exactly the situation it is for. The log is also opened before
the node joins the tailnet, so an unwritable path stops taillow at startup.

**The file is mode 0600 and append-only.** It names who asked for what.

**An unknown caller's address goes to the audit log, never the response.** It
is the only lead on a caller the tailnet could not name, and nothing the caller
needs to be told.
