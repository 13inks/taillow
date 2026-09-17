# taillow

A small LLM gateway that lives on your tailnet and nowhere else.

taillow joins a tailnet as its own node using [`tsnet`](https://tailscale.com/kb/1244/tsnet),
so it needs no public port and no Tailscale install on the host. When a request
arrives it asks the tailnet **who is calling**, reads **what the tailnet policy
grants them**, and only then forwards the prompt to a model. Every request leaves
one audit line. Every refusal says why.

There is no local policy file. Which models someone may use, and how many tokens
a day, live in the tailnet policy file's grants, next to the rules that already
decide who can reach what.

```
caller on the tailnet
   │  POST /v1/complete
   ▼
taillow (its own tailnet node)
   1. WhoIs            who owns this connection?          403 / 503
   2. grant            what does the policy give them?    403
   3. request          is the body exactly what we accept? 400, names the field
   4. model            is this model in their grant?      403
   5. budget           reserve tokens for today           429, says when it resets
   6. upstream         Anthropic or Ollama                502, carries their status
   7. audit line, then the answer
```

## Run it

You need Go 1.27 or newer and a tailnet you can edit the policy file of.

**1. Give taillow a tag and a grant.** In the tailnet policy file:

```jsonc
"tagOwners": {
  "tag:taillow": ["autogroup:admin"]
},
"grants": [{
  "src": ["autogroup:member"],
  "dst": ["tag:taillow"],
  "ip":  ["tcp:80"],
  "app": {
    "github.com/13inks/taillow/cap/llm": [
      {"models": ["claude-opus-5", "qwen3:8b"], "dailyTokens": 200000}
    ]
  }
}]
```

`ip` lets members reach the node at all. `app` is what taillow reads: the models
this caller may use and their daily token budget. Both fields are required in
every grant value, and there is no "zero means unlimited".

**2. Mint an auth key** in the admin console: tagged `tag:taillow`, ephemeral,
pre-authorized. Put it in your environment, never on a command line:

```sh
export TS_AUTHKEY=tskey-auth-...
export ANTHROPIC_API_KEY=...        # only if you grant claude-* models
make run
```

Models named `claude-*` go to the Anthropic API. Everything else goes to an
Ollama at `-ollama-url` (default `http://127.0.0.1:11434`).

**3. Call it from another device on the tailnet:**

```sh
curl http://taillow/whoami

curl http://taillow/v1/complete \
  -d '{"model": "qwen3:8b", "prompt": "Say hello in five words.", "max_tokens": 64}'
```

```json
{
  "model": "qwen3:8b",
  "text": "Hello, it is nice today.",
  "stopReason": "stop",
  "inputTokens": 17,
  "outputTokens": 8,
  "budgetRemaining": 199975,
  "budgetResetAt": "2026-09-18T00:00:00Z"
}
```

### If curl hangs

A request that **times out** instead of being refused is almost always the
tailnet policy, not taillow. A policy drop is silent by design, so the tunnel is
up and HTTP just never arrives. Check the path first:

```sh
tailscale ping taillow      # a pong means the node is up and reachable
```

If the ping works and curl hangs, the `ip` line of the grant is missing or does
not match the caller. If you changed the tag on the auth key, delete
`.tsnet-state/` before restarting: saved node state wins over a new key.

## What a refusal looks like

Refusals are the point of this project. Each one has a status, a reason a
person can act on, and, once the caller is known, who the tailnet thinks they
are.

```json
{"status": 400, "reason": "request field \"temperature\": unknown field",
 "caller": {"login": "alice@example.com", "node": "alice-laptop"}}
```

```json
{"status": 403, "reason": "model \"claude-fable-5-1\" is not in this caller's grant; granted: claude-opus-5, qwen3:8b",
 "caller": {"login": "alice@example.com", "node": "alice-laptop"}}
```

```json
{"status": 429, "reason": "daily token budget exhausted for alice@example.com: this request reserves 969 tokens",
 "caller": {"login": "alice@example.com", "node": "alice-laptop"},
 "budget": {"identity": "alice@example.com", "dailyTokens": 1000, "remaining": 100, "resetAt": "2026-09-18T00:00:00Z"}}
```

```json
{"status": 502, "reason": "anthropic upstream returned 529: ...",
 "caller": {"login": "alice@example.com", "node": "alice-laptop"},
 "upstream": {"provider": "anthropic", "status": 529}}
```

| Status | Meaning |
|---|---|
| 400 | The body is not exactly `{"model", "prompt", "max_tokens"}`. The field is named. |
| 403 | The tailnet does not know the caller, grants them nothing for this app, or does not grant this model. |
| 422 | The model declined the request. Providers report this as a success with no text; taillow does not pass that on as a 200. |
| 429 | Today's token budget cannot cover this request. Says whose budget, the limit, and the reset time. |
| 500 | The audit line could not be written, so the answer was withheld. |
| 502 | The upstream failed. Carries the provider and its own status. |
| 503 | The identity lookup itself failed, or no provider serves this model. |

## The audit log

One JSON line per request, refused ones included, appended to `-audit-log`
(default `taillow-audit.jsonl`, mode 0600):

```json
{"time":"2026-09-17T19:04:11Z","caller":"alice@example.com","node":"alice-laptop","model":"qwen3:8b","inputTokens":17,"outputTokens":8,"decision":"allowed","status":200}
{"time":"2026-09-17T19:04:30Z","caller":"tags:tag:ci","node":"runner-7","model":"claude-opus-5","inputTokens":0,"outputTokens":0,"decision":"refused_budget","status":429,"reason":"daily token budget exhausted for tags:tag:ci: this request reserves 8261 tokens"}
```

The line is written before the response. If it cannot be written, the caller
gets a 500 and not the answer.

## Threat model, briefly

**What taillow trusts:** the tailnet. WireGuard binds each packet's source
address to the sending peer's key, so `WhoIs` on the connection's address cannot
be forged from inside the tailnet. taillow never reads `X-Forwarded-For` or any
other header for identity.

**What it defends against:** a tailnet member using a model or a volume of
tokens the policy did not give them; a malformed or ambiguous grant quietly
reading as "no limits"; a failure anywhere in the chain reaching the caller as
an empty success.

**What it does not defend against:** someone who can edit the tailnet policy
file (they are the authority by design); a compromised device of an authorized
user (it *is* that user, as far as any network can tell); prompt content. taillow
decides who may call which model and how much. It does not inspect what they
say.

**Known limits.** The budget ledger is in memory, so a restart resets the day's
counts; the audit log is the durable record. There is no streaming and no
conversation state: one prompt in, one answer out.

## Layout

```
cmd/taillow/          main.go wires the node and the routes; complete.go is the request path
internal/ident/       WhoIs -> who is calling. A tagged node is its tags, not its creator
internal/grant/       the caller's capability map -> allowed models and a daily budget
internal/strictjson/  exact keys, no repeats: the decoding both of the above rely on
internal/budget/      per-identity, per-UTC-day token ledger: reserve, then settle
internal/upstream/    one Provider interface; Anthropic (official SDK) and Ollama (net/http)
internal/audit/       append-only JSON Lines
DECISIONS.md          every choice someone could reasonably have made differently, and why
```

`make check` runs gofmt, `go vet` and the tests under the race detector.

## Status

Every path above is covered by tests that fake the tailnet's `WhoIs` and the two
providers; the Anthropic tests run the real SDK against a local fake of the
Messages API. The node has joined a real tailnet. The full request path has not
yet been exercised end to end on one, and this README will say so until it has.

## How this was built

With coding agents, under verification, and that is worth being specific about.
Most files started as a written contract: exact signatures plus numbered
behavior rules. A local model wrote one file per contract inside a `go vet`
repair loop, and then a different reviewer read the result against the contract.

That second step earned its keep. The compiler loop reliably produced code that
builds. It did not produce code that was right: review caught a strict-JSON
check that skipped itself when given no allowed keys, a trailing-data check that
rejected every valid document, and an error path that reported an undecodable
200 as "upstream unreachable". Model-written tests were worse than model-written
code, asserting things no contract said (that concurrent writers finish in the
order they started, for one), so the tests here are written by hand.

The design decisions are mine to defend, and they are all in
[DECISIONS.md](DECISIONS.md).

## License

BSD 3-Clause. See [LICENSE](LICENSE).
