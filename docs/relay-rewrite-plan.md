# Relay rewrite plan

## Objective

Expose OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, and
Gemini GenerateContent as first-class client protocols. Keep account, token,
channel, quota, and administration data in the existing application. Replace
the relay implementation once all retained routes pass protocol conformance
tests.

## Design rules

1. A request has an **inbound protocol** and a separately selected **upstream
   protocol**. Channel type does not determine the response format sent to the
   client.
2. A same-protocol route forwards the original body and SSE frames. It may
   change the model, configured system instruction, authentication, and safe
   transport headers. It must preserve unknown JSON fields and event types.
3. A cross-protocol route uses explicit converters. A feature that cannot be
   represented must return a clear error before sending the upstream request.
   Never silently drop tools, thinking data, media, or provider extensions.
4. The gateway owns channel selection, retries, quota reservation, settlement,
   cancellation, logging, and error presentation. A provider client library is
   an implementation option inside the upstream transport, not the public API.
5. Only retry before the first response byte reaches the client. Treat stream
   termination, cancellation, and missing final usage as explicit settlement
   cases. Do not stack SDK retries on gateway retries.
6. Model identifiers and upstream API versions come from channel configuration
   and request paths; no model-family-specific version switches.

## Baseline endpoint and channel matrix

| Client endpoint | Current upstream selection | Current behavior and gap |
| --- | --- | --- |
| `POST /v1/chat/completions` | Any configured channel with the model in the user's group | OpenAI wire-format channels use native JSON/SSE passthrough with their configured URL and authentication variants. Anthropic channels use an explicit text/user-image/function-tool converter, and Gemini channels an explicit text converter, in both synchronous and streaming forms. Unsupported fields are rejected; provider-specific formats still use legacy converters. OpenAI and Anthropic Go SDK offline fixtures pass. |
| `POST /v1/responses` | OpenAI channels with the model in the user's group | Native synchronous and SSE create, preserving response items and events; requires an explicit `model`. Background mode and retrieval/cancellation routes remain unsupported. |
| `POST /v1/messages` | Anthropic channels first; OpenAI channels, then Gemini channels, for the supported conversion subsets | Native request and response forwarding, including SSE, usage, mapping, configured system prompt, and retry before response output. Synchronous and streaming text/user-image/function-tool requests can convert to OpenAI Chat; synchronous and streaming text requests can convert to Gemini. Assistant-side media and other media conversion remain unsupported. |
| `POST /v1[beta]/models/{model}:generateContent` and `:streamGenerateContent` | Gemini channels with the model in the user's group | Native forwarding on both versions, with retry before response output; no cross-protocol conversion. |
| Image generation, audio, edits, moderation, and proxy routes | Legacy channel selector and adaptors, except OpenAI Moderations | Registered routes; require separate conformance fixtures before migration. Completions, both Embeddings entrypoints, and Moderations use the native lifecycle for OpenAI channels. |
| Files, fine-tuning, assistants, and threads | None | Registered placeholders return not implemented. Gemini File, Live, and Interactions have no routes. |

The remaining legacy route inventory is:

| Endpoint | Current implementation | Migration concern |
| --- | --- | --- |
| `POST /v1/completions` | Native lifecycle on OpenAI channels without configured system prompt; legacy controller otherwise | Native JSON/SSE, model mapping, extensions, and usage have offline fixtures. |
| `POST /v1/edits` | Legacy text controller and `GeneralOpenAIRequest` | Request shape and stream handling need separate fixtures. |
| `POST /v1/embeddings`, `/v1/engines/{model}/embeddings` | Native lifecycle on OpenAI channels; legacy controller on other channel types | OpenAI batch input, extensions, model mapping, large response usage, errors, and engine path model injection have offline fixtures; other channel types need migration. |
| `POST /v1/moderations` | Native lifecycle on OpenAI channels; legacy controller on other channel types | OpenAI text and image request preservation, model mapping, `omni-moderation-latest` default, error refund, response preservation, and usage fallback have offline fixtures. Other channel types need migration. |
| `POST /v1/images/generations` | Legacy image controller | Image-specific quota and provider conversions differ from token billing. |
| `POST /v1/audio/{speech,transcriptions,translations}` | Legacy audio controller | Multipart and binary bodies require a separate transport and billing path. |
| `/v1/oneapi/proxy/{channelid}/*target` | Legacy proxy controller | Explicit channel selection and arbitrary target paths need an isolated security review. |

Native endpoint tests run against local HTTP upstreams and an in-memory quota
database. They cover normal and streaming responses, multimodal request fields,
provider extensions, channel matching, usage, errors, and cancellation. They do
not establish official SDK conformance or cover the legacy routes.

The Responses create and SSE fixtures follow the [official create reference](https://developers.openai.com/api/reference/go/resources/responses/methods/create)
and [streaming event reference](https://developers.openai.com/api/reference/resources/responses/streaming-events).
The Embeddings usage fixture follows the [official embeddings guide](https://developers.openai.com/api/docs/guides/embeddings).
The legacy Completions fixture follows the [official Completions reference](https://developers.openai.com/api/reference/cli/resources/completions).
The Moderations input fixtures follow the [official moderation guide](https://developers.openai.com/api/docs/guides/moderation).
The Chat-to-Messages conversion fixtures follow the [OpenAI Chat reference](https://developers.openai.com/api/reference/typescript/resources/chat/subresources/completions/methods/create) and [Anthropic Messages reference](https://platform.claude.com/docs/en/api/messages/create).

## Features the cross-protocol converters cannot represent

Every converter rejects — before the upstream request, or by failing the
stream — any field it cannot carry into the other protocol. Nothing is
silently dropped.

- Chat Completions to Anthropic Messages: `response_format`, `n`, logprobs,
  image `detail`, audio parts, named tools other than functions, developer or
  system messages after the conversation starts, and `refusal` outputs.
- Assistant-side media remains unsupported in Chat/Messages conversion:
  OpenAI's assistant content-part schema has text and refusal parts, with an
  audio response ID as a separate field; Anthropic's image/document sources
  and signed thinking blocks have no matching Chat assistant fields. Both
  directions reject these blocks instead of moving them to a user turn.
- Anthropic Messages to Chat Completions: `thinking` and `redacted_thinking`
  blocks, citations, server tool blocks, text after a `tool_use` block in the
  same message, non-null `stop_sequence` values, provider containers, and
  usage fields the Chat protocol has no slot for.
- Gemini conversions (Chat and Messages, both directions): tools and
  `functionCall`/`functionResponse` parts, inline or file media parts, thought
  parts and thought signatures, grounding and citation parts, multiple
  candidates, non-zero candidate indexes, and finish reasons other than
  `STOP` and `MAX_TOKENS`.
- Streaming conversions reject unknown upstream event types and unknown delta
  fields mid-stream. Chat clients then receive a final `data:` error event
  without `[DONE]`; Anthropic clients receive a truncated stream without
  terminal events, so an interrupted conversion never looks complete.
- Messages streams converted from OpenAI Chat request the upstream's final
  usage chunk. Their `message_start` input count is provisional; the final
  `message_delta` reports the actual cumulative input and output counts.
  Streams without provider usage do not emit `message_stop`.
- `top_k` exists only on the Messages side of the Gemini conversion; Chat has
  no equivalent and passes nothing.
- A channel's configured system prompt replaces the client's system
  instructions in every converted request, matching native behavior.

## Phases and acceptance gates

### 0. Baseline and contracts

- [x] Record current endpoint and channel capability matrix, including known gaps.
- [x] Add offline fixtures for native request extensions, Anthropic tools and
  thinking events, Gemini SSE usage, upstream errors, channel selection, quota
  settlement, and unknown fields.
- [x] Add native fixtures for multimodal data, cancellation, and the remaining
  normal and streaming response paths.
- [x] Reproduce the implemented native route behavior with local upstreams and
  an in-memory quota database, without live provider keys.
- [ ] Run client-facing fixtures through official SDKs where applicable.
- [ ] Gate: all retained legacy routes have the same offline baseline.

### 1. Native transport and protocol entrypoints

- [x] Add native Anthropic Messages and Gemini GenerateContent entrypoints under
  the existing token, group, channel, and quota system.
- [x] Route native passthrough only to channels that speak the same protocol.
  Preserve request extensions and response bytes; extract usage without
  changing output. Cross-protocol fallbacks use explicit converters.
- [x] Support native authentication headers as gateway token inputs and substitute
  the selected channel credential upstream.
- [x] Reject cross-origin upstream redirects so provider credentials cannot be
  forwarded to the redirect target.
- [x] Rewrite configured system prompts in each protocol's native request field
  while preserving other fields.
- [x] Cover Anthropic normal responses, Anthropic SSE copying, Gemini SSE,
  upstream errors, and mixed-channel selection with offline tests.
- [x] Gate: cover every native normal and streaming route and explicitly verify
  unsupported channel combinations with offline upstreams.

### 2. Shared relay lifecycle

- [x] Extract native request execution, channel retry, quota lifecycle, and
  response copying from the Gin controller into `relay/lifecycle`.
- [x] Retry native requests on another matching channel for upstream transport
  failures, 429, and 5xx before forwarding any response bytes.
- [x] Use one native error mapper in authentication, channel selection, and
  controller errors; log the selected channel at quota settlement.
- [x] Verify native cancellation, upstream error, retry, and stream completion
  settle quota once with the channel actually used.
- [x] Refund legacy Chat Completions reservations when adaptor selection,
  request construction, or upstream transport fails; refresh quota cache.
- [x] Fall back to approximate token counting if the legacy tokenizer was not
  initialized, so a request does not panic before quota handling.
- [x] Prevent legacy retry and a second JSON error after response output has
  already started.
- [x] Verify the migrated native paths settle cancellation, upstream errors,
  retries, and stream completion once, logging the selected channel.
- [x] Move request metadata, compatible-channel selection, retry policy, quota
  reservation/settlement, and upstream error extraction into shared relay
  components. `relay/meta` no longer imports Gin; `relay/ginmeta` extracts its
  HTTP values. Native and legacy routes use the same channel selector, retry
  policy, and once-only quota reservation. Client protocol error envelopes
  remain at their HTTP boundaries.
- [x] Remove direct dependencies on Gin and `GeneralOpenAIRequest` from provider
  conversion interfaces. `ConversionInput` carries the parsed legacy request,
  API key, mode, and transport values; the old request name remains an alias
  for legacy helpers until phase 5 removes them.
- [x] Gate: cancellation, upstream errors, retries, and complete or truncated
  streams settle once on the actual channel across retained metered endpoint
  families. Offline route fixtures cover Chat, Completions, Embeddings,
  Moderations, Edits, images, and all three audio routes; native fixtures
  cover Responses, Messages, Gemini, and native OpenAI operations. The proxy
  route is explicitly unmetered and its no-charge behavior is tested.

### 3. OpenAI Chat and Anthropic Messages

- [x] Move OpenAI-channel Chat Completions onto the shared lifecycle with
  same-protocol JSON and SSE passthrough.
- [x] Convert the supported synchronous Chat-to-Messages text/function-tool
  subset, with explicit rejection of unsupported fields and blocks.
- [x] Convert the supported synchronous Messages-to-Chat text/function-tool
  subset when no matching Anthropic channel exists, with explicit rejection of
  unsupported fields and blocks.
- [x] Convert user image URL and supported base64 image inputs in both
  synchronous Chat/Messages directions; reject image detail settings and
  unrepresentable image sources before forwarding.
- [x] Move streaming Chat-to-Messages conversion onto the shared lifecycle for
  Anthropic and Gemini channels, covering tool calls, stop reasons, usage,
  provider envelope extensions, and upstream error events.
- [x] Document protocol features that cannot be represented in the supported
  conversions; every such field fails the request or stream instead of being
  dropped.
- [x] Move the remaining retained Chat channel types off the legacy controller.
  PaLM, Baidu, Zhipu, Ali, Xunfei, AIProxyLibrary, Tencent, Ollama, AWS
  Claude, Coze, Cohere, Cloudflare, DeepL, VertexAI, Replicate, and Proxy
  now enter the shared Chat lifecycle for reservation, retry, and settlement.
  Their provider-specific wire conversion and response handling remain in
  adapters behind the lifecycle's adapter attempt until the phase 5 transport
  extraction; none is treated as OpenAI-compatible passthrough. Adapter
  streams for Ollama, Replicate, AWS Claude, Ali, Baidu, Zhipu, Cohere, Coze,
  Tencent, AIProxyLibrary, and Cloudflare now require their terminal event
  before emitting `[DONE]`; scanner errors no longer synthesize completion.
- [x] Complete the Chat Completions <-> Messages error-envelope mapping and
  audit assistant-side media. Converted HTTP errors use the client protocol's
  envelope and retain provider details. Assistant-side image and audio blocks
  have no shared standard Chat/Messages representation in the official client
  schemas and fail explicitly.
- [x] Gate: official OpenAI and Anthropic Go clients pass the common offline
  suite for native and converted Chat/Messages requests. Local HTTP fixtures
  cover synchronous text, image input, streaming text, and provider errors;
  SDK retries are disabled so the gateway controls retry policy.

### 4. Gemini as a first-class protocol

- [x] Route native Gemini `generateContent` and `streamGenerateContent` through
  matching Gemini channels without converting the request to OpenAI format.
- [x] Verify that a newly configured Gemini model ID routes without a code change.
- [x] Convert synchronous text-only Chat Completions requests to Gemini
  GenerateContent and return Chat responses with usage and provider metadata.
  Reject tools, media, thinking parts, and unsupported finish reasons.
- [x] Add explicit conversion between Gemini and Chat/Messages for the
  representable text subset in both synchronous and streaming forms, keeping
  provider metadata in a named extension and rejecting unsupported content
  before the upstream request.
- [ ] Convert tools, media, and thinking parts between Gemini and
  Chat/Messages where they are representable.
- [x] Preserve Gemini-specific tools, thought signatures, grounding, safety
  settings, and usage in the native path. File, Live, and Interactions APIs are
  separate endpoint families with separate acceptance gates.
- [ ] Gate: verify the native Gemini client and new model IDs with offline
  conformance tests.

### 5. OpenAI Responses and remaining operations

- [x] Implement synchronous and streaming `POST /v1/responses` as its own
  protocol for OpenAI channels, preserving response items and SSE event bytes.
- [x] Inventory the registered embeddings, image, audio, moderation, and legacy
  provider routes; migrate OpenAI-channel Completions, Embeddings, and Moderations.
- [ ] Add the remaining Responses operations, including background response
  retrieval and cancellation, before claiming full API compatibility.
- [ ] Port the retained image, audio, edits, proxy, and legacy provider routes;
  delete unused adapters and the old shared request type.
- [ ] Gate: all documented routes pass offline conformance tests and no production
  route uses the legacy relay controller.

## SDK evaluation

Evaluate Bifrost Core and GoAI with the same fixtures for field preservation,
streaming, tool calls, cancellation, usage, custom base URLs, and retry control.
Use a library only where its behavior passes the relevant gate. Native HTTP
forwarding remains the default for same-protocol requests.

## Current implementation status

- Current native clients may use `Authorization: Bearer <gateway token>`;
  Anthropic clients may also use `x-api-key`, and Gemini clients may use
  `x-goog-api-key`. The gateway replaces these credentials before forwarding.
- Native routes use a compatible channel in the caller's group. Messages prefer
  Anthropic channels and fall back to OpenAI Chat for the supported synchronous
  and streaming text/user-image/function-tool subset, then to Gemini channels
  for the text subset. OpenAI Chat uses the native lifecycle on OpenAI,
  Anthropic, and Gemini channels in both synchronous and streaming forms;
  OpenAI wire-format channel types use the same lifecycle; provider-specific
  channel formats still use legacy adapters during migration.
- Phase 0: the endpoint inventory and local HTTP fixtures cover native request
  and SSE preservation, credential substitution, channel selection, upstream
  errors, multimodal input, cancellation, and quota settlement. Official SDK
  and retained legacy route conformance remain.
- Phase 1: initial native `POST /v1/messages` and Gemini
  `POST /v1[beta]/models/{model}:{generateContent|streamGenerateContent}`
  routes are implemented for matching Anthropic/Gemini channel types. They
  preserve native bodies/events and use the existing token and quota database.
  Configured system prompts use each protocol's native field. Retry can select
  another matching channel before any response is forwarded. Cross-protocol
  routing beyond the supported Chat/Messages subset and native
  File/Live/Interactions endpoints remain.
- Phase 2: native and legacy routes share protocol-neutral channel selection,
  retry policy, quota reservations, and upstream error extraction. The
  metadata type and provider conversion interfaces no longer depend on Gin.
  Legacy image and audio routes now reserve before forwarding and refund on
  upstream failures; incomplete legacy OpenAI-compatible streams no longer
  synthesize `[DONE]`. Legacy controllers still implement endpoint-specific
  body handling until phase 5 migrates them. The proxy route remains
  intentionally unmetered. Operations without upstream usage can provide a
  fallback input estimate; image Moderations keep the reservation when no
  usage is reported.
- Phase 3: OpenAI channels now route Chat Completions through the native
  lifecycle. The path preserves unmapped JSON and SSE bytes, replaces upstream
  credentials, applies model and system prompt configuration, retries before
  output, and settles provider-reported Chat usage. Synchronous Chat requests
  selected onto Anthropic channels now convert text, function tools, tool
  results, stop reasons, errors, and usage explicitly. Unsupported request
  fields fail before upstream forwarding, and unrepresentable response blocks
  fail before client output. Synchronous Messages can now fall back to OpenAI
  Chat channels for text, user images, and function tools, with the same
  rejection rule and quota settlement. Converted Anthropic responses reject
  unknown content and usage fields instead of silently dropping them.
  Streaming Chat Completions on Anthropic and Gemini channels now also use the
  shared lifecycle: Anthropic events convert to Chat chunks (and Chat chunks to
  Anthropic events) with tool calls, stop reasons, usage, and provider envelope
  extensions preserved. Channels using the OpenAI Chat wire format now use the
  same lifecycle, including their existing path and authentication variants.
  The official OpenAI and Anthropic Go clients pass local native and converted
  synchronous and streaming fixtures, including image inputs and errors.
  Provider-specific Chat formats now enter the shared lifecycle through an
  adapter attempt, so Chat selection, retries, and billing no longer use the
  legacy controller. Their Gin-based wire adapters are retained for phase 5
  extraction. Converted HTTP errors now use the client protocol's envelope and
  retain provider code, parameter, and type details.
- Phase 4: native Gemini routing is in place; client conformance remains. The
  synchronous and streaming Chat-to-Gemini text subset now uses the shared
  lifecycle, preserving Gemini response metadata in a named extension and
  rejecting unsupported content. Messages requests now also convert to Gemini
  channels for the text subset, in both synchronous and streaming forms, with
  Gemini usage mapped to Anthropic usage fields. Gemini tool and media
  conversion, Gemini-to-Messages/Chat as inbound fallbacks, and further
  cross-protocol converters remain. Phase 5 has a native Responses create
  route, OpenAI-channel
  Embeddings routes, and OpenAI-channel legacy Completions route when no forced
  system prompt is configured. OpenAI-channel Moderations now use the same
  lifecycle, preserving text/image inputs and unknown response fields. Large
  non-streaming responses now forward while parsing usage without buffering
  the full body. Background Responses and other operations remain.
