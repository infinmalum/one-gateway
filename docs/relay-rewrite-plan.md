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
| `POST /v1/chat/completions` | Any configured channel with the model in the user's group | OpenAI channels now use the native lifecycle and preserve same-protocol JSON/SSE; other channel types still use legacy converters. Official SDK conformance remains. |
| `POST /v1/responses` | OpenAI channels with the model in the user's group | Native synchronous and SSE create, preserving response items and events; requires an explicit `model`. Background mode and retrieval/cancellation routes remain unsupported. |
| `POST /v1/messages` | Anthropic channels with the model in the user's group | Native request and response forwarding, including SSE, usage, mapping, configured system prompt, and retry before response output; no cross-protocol conversion. |
| `POST /v1[beta]/models/{model}:generateContent` and `:streamGenerateContent` | Gemini channels with the model in the user's group | Native forwarding on both versions, with retry before response output; no cross-protocol conversion. |
| Image generation, audio, edits, moderation, and proxy routes | Legacy channel selector and adaptors | Registered routes; require separate conformance fixtures before migration. Completions and both Embeddings entrypoints use the native lifecycle for supported OpenAI channels. |
| Files, fine-tuning, assistants, and threads | None | Registered placeholders return not implemented. Gemini File, Live, and Interactions have no routes. |

The remaining legacy route inventory is:

| Endpoint | Current implementation | Migration concern |
| --- | --- | --- |
| `POST /v1/completions` | Native lifecycle on OpenAI channels without configured system prompt; legacy controller otherwise | Native JSON/SSE, model mapping, extensions, and usage have offline fixtures. |
| `POST /v1/edits` | Legacy text controller and `GeneralOpenAIRequest` | Request shape and stream handling need separate fixtures. |
| `POST /v1/embeddings`, `/v1/engines/{model}/embeddings` | Native lifecycle on OpenAI channels; legacy controller on other channel types | OpenAI batch input, extensions, model mapping, large response usage, errors, and engine path model injection have offline fixtures; other channel types need migration. |
| `POST /v1/moderations` | Legacy text controller and provider adapters | Moderation response semantics need separate fixtures. |
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

## Phases and acceptance gates

### 0. Baseline and contracts

- [x] Record current endpoint and channel capability matrix, including known gaps.
- [x] Add offline fixtures for native request extensions, Anthropic tools and
  thinking events, Gemini SSE usage, upstream errors, channel selection, quota
  settlement, and unknown fields.
- [x] Add native fixtures for multimodal data, cancellation, and the remaining
  normal and streaming response paths.
- [ ] Run client-facing fixtures through official SDKs where applicable.
- [ ] Gate: the current behavior is reproducible without live provider keys.

### 1. Native transport and protocol entrypoints

- [x] Add native Anthropic Messages and Gemini GenerateContent entrypoints under
  the existing token, group, channel, and quota system.
- [x] Route only to channels that can speak the same protocol. Preserve request
  extensions and response bytes; extract usage without changing output.
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
- [ ] Move request metadata, compatible-channel selection, retry policy, quota
  reservation/settlement, and error mapping into one protocol-neutral layer.
  Initial selection and retry for native routes now live in `relay/lifecycle`;
  legacy routes and request metadata still need migration.
- [ ] Remove direct dependencies on Gin and `GeneralOpenAIRequest` from provider
  conversion interfaces.
- [ ] Gate: a cancellation, upstream error, retry, or stream completion settles
  quota once and logs the channel actually used.

### 3. OpenAI Chat and Anthropic Messages

- [ ] Move Chat Completions onto the new lifecycle, preserving same-protocol
  passthrough. OpenAI channels are migrated; converted channel types remain.
- [ ] Add explicit Chat Completions <-> Messages converters, including tools,
  content blocks, thinking, stop reasons, errors, usage, and SSE state.
- [ ] Gate: official OpenAI and Anthropic clients pass the common offline suite.

### 4. Gemini as a first-class protocol

- [x] Route native Gemini `generateContent` and `streamGenerateContent` through
  matching Gemini channels without converting the request to OpenAI format.
- [x] Verify that a newly configured Gemini model ID routes without a code change.
- [ ] Add explicit conversion to/from Chat and Messages for representable features.
- [x] Preserve Gemini-specific tools, thought signatures, grounding, safety
  settings, and usage in the native path. File, Live, and Interactions APIs are
  separate endpoint families with separate acceptance gates.
- [ ] Gate: verify the native Gemini client and new model IDs with offline
  conformance tests.

### 5. OpenAI Responses and remaining operations

- [x] Implement synchronous and streaming `POST /v1/responses` as its own
  protocol for OpenAI channels, preserving response items and SSE event bytes.
- [ ] Add the remaining Responses operations, including background response
  retrieval and cancellation, before claiming full API compatibility.
- [ ] Inventory embeddings, image, audio, and legacy provider routes. Port routes
  that are retained; delete unused adapters and the old shared request type.
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
- Native routes currently require a matching Anthropic, Gemini, or OpenAI
  channel in the caller's group. OpenAI Chat now uses the native lifecycle on
  OpenAI channels; other OpenAI-shaped routes and Chat requests selected onto
  other channel types still use the legacy adapters during migration.
- Phase 0: inventory started; offline fixtures now cover native request
  preservation, SSE preservation, credential substitution, channel selection,
  upstream errors, multimodal input, cancellation, and quota settlement.
  Official SDK and legacy route conformance remain.
- Phase 1: initial native `POST /v1/messages` and Gemini
  `POST /v1[beta]/models/{model}:{generateContent|streamGenerateContent}`
  routes are implemented for matching Anthropic/Gemini channel types. They
  preserve native bodies/events and use the existing token and quota database.
  Configured system prompts use each protocol's native field. Retry can select
  another matching channel before any response is forwarded. Cross-protocol
  routing and native File/Live/Interactions endpoints remain.
- Phase 2: native request execution, initial compatible-channel selection,
  retry, and quota settlement now run in a Gin-free lifecycle package. Legacy
  routes still need migration before the lifecycle is fully shared.
- Phase 3: OpenAI channels now route Chat Completions through the native
  lifecycle. The path preserves unmapped JSON and SSE bytes, replaces upstream
  credentials, applies model and system prompt configuration, retries before
  output, and settles provider-reported Chat usage. Other channel types still
  use legacy converters; official client fixtures and cross-protocol converters
  remain.
- Phase 4: native Gemini routing is in place; client conformance and conversions
  remain. Phase 5 has a native Responses create route, OpenAI-channel
  Embeddings routes, and OpenAI-channel legacy Completions route when no forced
  system prompt is configured. Large non-streaming responses now forward while
  parsing usage without buffering the full body. Background Responses and
  other operations remain.
