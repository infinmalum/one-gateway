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

## Phases and acceptance gates

### 0. Baseline and contracts

- [ ] Record current endpoint and channel capability matrix, including known gaps.
- [x] Add offline fixtures for native request extensions, Anthropic tools and
  thinking events, Gemini SSE usage, upstream errors, channel selection, quota
  settlement, and unknown fields.
- [ ] Add fixtures for multimodal data, cancellation, and the remaining normal
  and streaming response paths.
- [ ] Run client-facing fixtures through official SDKs where applicable.
- [ ] Gate: the current behavior is reproducible without live provider keys.

### 1. Native transport and protocol entrypoints

- [x] Add native Anthropic Messages and Gemini GenerateContent entrypoints under
  the existing token, group, channel, and quota system.
- [x] Route only to channels that can speak the same protocol. Preserve request
  extensions and response bytes; extract usage without changing output.
- [x] Support native authentication headers as gateway token inputs and substitute
  the selected channel credential upstream.
- [x] Cover Anthropic normal responses, Anthropic SSE copying, Gemini SSE,
  upstream errors, and mixed-channel selection with offline tests.
- [ ] Gate: cover every native normal and streaming route and explicitly verify
  unsupported channel combinations.

### 2. Shared relay lifecycle

- [ ] Move request metadata, compatible-channel selection, retry policy, quota
  reservation/settlement, and error mapping into one protocol-neutral layer.
- [ ] Remove direct dependencies on Gin and `GeneralOpenAIRequest` from provider
  conversion interfaces.
- [ ] Gate: a cancellation, upstream error, retry, or stream completion settles
  quota once and logs the channel actually used.

### 3. OpenAI Chat and Anthropic Messages

- [ ] Move Chat Completions onto the new lifecycle, preserving same-protocol
  passthrough.
- [ ] Add explicit Chat Completions <-> Messages converters, including tools,
  content blocks, thinking, stop reasons, errors, usage, and SSE state.
- [ ] Gate: official OpenAI and Anthropic clients pass the common offline suite.

### 4. Gemini as a first-class protocol

- [x] Route native Gemini `generateContent` and `streamGenerateContent` through
  matching Gemini channels without converting the request to OpenAI format.
- [ ] Add explicit conversion to/from Chat and Messages for representable features.
- [x] Preserve Gemini-specific tools, thought signatures, grounding, safety
  settings, and usage in the native path. File, Live, and Interactions APIs are
  separate endpoint families with separate acceptance gates.
- [ ] Gate: verify the native Gemini client and new model IDs with offline
  conformance tests.

### 5. OpenAI Responses and remaining operations

- [ ] Implement Responses as its own protocol, including its event stream and
  response items, rather than coercing it into Chat Completions.
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
- Native routes currently require a matching Anthropic or Gemini channel in
  the caller's group. Existing OpenAI-shaped routes continue using the legacy
  adapters during migration.
- Phase 0: inventory started; offline fixtures now cover native request
  preservation, SSE preservation, credential substitution, channel selection,
  upstream errors, and quota settlement. Official SDK conformance remains.
- Phase 1: initial native `POST /v1/messages` and Gemini
  `POST /v1[beta]/models/{model}:{generateContent|streamGenerateContent}`
  routes are implemented for matching Anthropic/Gemini channel types. They
  preserve native bodies/events and use the existing token and quota database.
  Cross-protocol routing, retry/failover, configured system-prompt rewriting,
  and native File/Live/Interactions endpoints are not implemented yet.
- Phase 4: native Gemini routing is in place; client conformance and conversions
  remain. Phases 2, 3, and 5 are pending.
