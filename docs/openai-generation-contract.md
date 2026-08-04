# OpenAI generation contract

Status: implementation contract for 0.8.5 (WI-903)

## Decision

Windshift's `llm.Client` exposes a provider-neutral `Complete` operation using
`CompletionRequest` and `CompletionResponse`. Wire contracts belong to provider
adapters and must not leak into agent, handler, scheduler, or service code.

The built-in OpenAI provider uses the native Responses API. OpenAI-compatible
providers (OpenRouter, Gemini's compatibility endpoint, Z.AI, local models, and
custom gateways) continue to use Chat Completions. Anthropic continues to use
its Messages contract.

An OpenAI connection with a custom base URL defaults to Chat Completions because
the upstream may be an opaque LiteLLM-style gateway. Operators can select a
contract explicitly with the reserved provider configuration field:

```json
{"api_contract":"responses"}
```

Allowed values are `auto`, `responses`, and `chat_completions`. The field is
consumed by Windshift and is never forwarded upstream.

## Normalized mapping

| Windshift | OpenAI Responses | Compatible Chat Completions |
| --- | --- | --- |
| `Messages` | typed `input` items | `messages` |
| `MaxTokens` | `max_output_tokens` | `max_completion_tokens`, negotiated to legacy `max_tokens` only after explicit rejection |
| `StructuredOutput` | `text.format` | `response_format` |
| function definition | flat function tool | nested `function` tool |
| tool result | `function_call_output` item | `tool` message |
| token usage | input/output token fields normalized to prompt/completion fields | prompt/completion token fields |

OpenAI Responses calls set `store: false`. Every raw output item is retained in
opaque, non-serialized `Message.ProviderState` and replayed on the next tool
turn. This preserves encrypted reasoning items and function-call context while
keeping persistence and business logic independent of OpenAI's item union.

The compatibility adapter starts with `max_completion_tokens`. It retries once
with `max_tokens` only when a 400/422 response explicitly rejects the modern
field, then remembers the legacy capability for that client lifetime. It never
retries unrelated client errors.

## Invariants

- Provider-specific request fields are assembled only inside provider adapters.
- OpenAI Responses output always normalizes to one `Choice`; function call items
  produce `finish_reason: tool_calls`.
- Unknown OpenAI output items are ignored by normalized consumers but retained
  intact in provider continuation state.
- Provider continuation state is in-memory only and never enters Windshift's
  public Chat Completions proxy or persisted conversation history.
- Custom provider configuration cannot override generated prompt, model, tool,
  storage, or token-limit fields.

## Sources

- [Migrate to the Responses API](https://developers.openai.com/api/docs/guides/migrate-to-responses)
- [Preserve reasoning without stored responses](https://developers.openai.com/api/docs/guides/reasoning#preserve-reasoning-without-stored-responses)
- [Create a Chat Completion](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
