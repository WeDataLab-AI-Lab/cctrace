# Changelog

> For the complete documentation index, see [llms.txt](/llms.txt). Markdown versions of documentation pages are available by appending `.md` to the page URL.

> The latest features and updates to the OpenAI API.

Upcoming deprecations are listed on the [deprecations page](/api/docs/deprecations).

## September, 2026

### Sep 3

Feature · Model: gpt-6-astra · API: v1/responses · API: v1/chat/completions

Released [GPT-6 Astra](https://developers.openai.com/api/docs/models/gpt-6-astra), our most capable model, built for the hardest end-to-end work.

Use GPT-6 Astra for reasoning, coding, computer use, research, and document creation. It combines these capabilities to carry complex tasks from an initial request to a finished result, using the context and tools you provide.

Key changes to consider when migrating:

- GPT-6 Astra does not support the `none` reasoning effort level.
- GPT-6 Astra does not support custom `temperature` or `top_p` values or log probabilities (`logprobs`).
- Tool calling requires the Responses API. If you use tools with Chat Completions, follow the [Responses migration guide](https://developers.openai.com/api/docs/guides/migrate-to-responses).
- [Misalignment monitoring](https://developers.openai.com/api/docs/guides/safety-checks/misalignment-monitoring) asynchronously checks for potential issues during agent work in supported Responses API requests. Checks can trigger safety alerts or stop a conversation for review.

Start with [Using GPT-6 Astra](https://developers.openai.com/api/docs/guides/latest-model) for capabilities, prompting, and migration guidance. Explore [computer use](https://developers.openai.com/api/docs/guides/tools-computer-use) for browser and desktop workflows, and see [pricing](https://developers.openai.com/api/docs/pricing) for available inference tiers.

### Sep 3

Feature · API: v1/responses

Added new controls for long-running work with GPT-6 Astra in the Responses API:

- [Async tool calling](https://developers.openai.com/api/docs/guides/async-tool-calling): Let the model continue working while your application runs function or custom tools, then return results as they become available.
- [Mid-turn steering](https://developers.openai.com/api/docs/guides/steering): Send additional instructions while a response is in progress over WebSockets, so the model can incorporate corrections or changing requirements.
- [Change reasoning effort mid-conversation](https://developers.openai.com/api/docs/guides/reasoning#change-reasoning-mid-conversation): Increase effort for difficult work or reduce it for routine follow-ups while preserving the cached prompt prefix.

### Sep 2

Update

Updated API errors so applications can distinguish traffic that increases too quickly from temporary model overload.

Traffic that increases too quickly can return a `429` error with the `slow_down` code. Temporary model overload returns a `503` error with the `server_is_overloaded` code. Both responses may include `Retry-After`. When the header is present, wait at least as long as it specifies before retrying. If it's missing, use exponential backoff. See the [error codes guide](https://developers.openai.com/api/docs/guides/error-codes) and [rate limits guide](https://developers.openai.com/api/docs/guides/rate-limits).

### Sep 1

Update

Connections to api.openai.com can now use IPv6.

## August, 2026

### Aug 29

Feature

[Mutual TLS (mTLS)](https://developers.openai.com/api/docs/guides/mutual-tls) and [X.509 workload identity federation](https://developers.openai.com/api/docs/guides/workload-identity-federation/x509) are now generally available for the OpenAI API. Configure certificates and X.509 identity providers directly in the [Platform console](https://platform.openai.com/settings/organization/security), with access controlled by your organization's roles and permissions.

### Aug 26

Update · Model: whisper-1 · Model: gpt-4o-transcribe · Model: gpt-4o-mini-transcribe · Model: gpt-4o-transcribe-diarize · API: v1/audio/transcriptions · API: v1/realtime

Announced the deprecation of `whisper-1`, `gpt-4o-transcribe`, `gpt-4o-mini-transcribe`, and `gpt-4o-transcribe-diarize`. These models will shut down on February 26, 2027. Migrate to [`gpt-live-transcribe`](https://developers.openai.com/api/docs/models/gpt-live-transcribe) or [`gpt-transcribe`](https://developers.openai.com/api/docs/models/gpt-transcribe). See the [transcription guide](https://developers.openai.com/api/docs/guides/transcription) and [deprecations page](https://developers.openai.com/api/docs/deprecations).

The Assistants API shut down on August 26, 2026. Migrate to the Responses API and Conversations API using the [migration guide](https://developers.openai.com/api/docs/assistants/migration).

### Aug 21

Feature

API customers can now select regional processing for an individual request by using a prefixed domain with an API key from a project having Global geography. Existing eligibility, data retention control, endpoint, and model support requirements continue to apply. Learn more in the [data controls guide](https://developers.openai.com/api/docs/guides/your-data#select-a-processing-region-per-request).

### Aug 21

Update · Model: gpt-5.6-sol

GPT-5.6 Sol now costs $4 per million input tokens and $20 per million output tokens, representing 20% lower input pricing and 33% lower output pricing. GPT-5.6 Sol’s promotional pricing is available at least through November 21, 2026. See [pricing details](https://developers.openai.com/api/docs/pricing).

### Aug 20

Feature

Released the [Prompt Caching dashboard](https://platform.openai.com/usage?usage_section=prompt-caching) on the OpenAI API platform. Track your cache hit rate over time, cache reads per write, and the breakdown of cache-read, cache-write, and uncached tokens to understand your caching efficiency and identify opportunities to improve. Filter metrics by model and service tier.

### Aug 20

Update · Model: gpt-image-2 · Model: gpt-image-2-2026-04-21 · API: v1/images/generations · API: v1/images/edits · API: v1/responses

Transparent backgrounds are now available in preview for `gpt-image-2` and `gpt-image-2-2026-04-21` in the Images API and the Responses API image generation tool. Set `background` to `transparent` and use `png` or `webp` output; `jpeg` does not support transparent backgrounds. Learn more in the [image generation guide](https://developers.openai.com/api/docs/guides/image-generation#customize-image-output).

### Aug 13

Announcement

Announced Ultrafast mode, a new API service tier for GPT-5.6 Sol that runs up to 14x faster than Standard processing. Available in limited preview to select customers. Sign up to receive updates on Ultrafast mode [here](https://openai.com/form/ultrafast/).

### Aug 7

Feature · Model: gpt-5.6-cyber · Model: gpt-daybreak-red-latest · Model: gpt-daybreak-blue-latest · API: v1/responses

Daybreak now offers two access tiers for approved defenders: Daybreak Blue and Daybreak Red. Use them to move from security findings to validated fixes in explicitly authorized engagements.

Start with Daybreak Blue for most defensive security work. It provides access to general-purpose models such as GPT-5.6 Sol for vulnerability discovery, secure code review, detection engineering, incident response, malware analysis, and patch validation. Read more [here](https://developers.openai.com/api/docs/models/gpt-daybreak-blue-latest).

Daybreak Red provides separately approved access to purpose-trained models such as [GPT-5.6 Cyber](https://developers.openai.com/api/docs/models/gpt-5.6-cyber) for authorized vulnerability reproduction, exploit validation, penetration testing, red teaming, and complex system analysis.

These models require separate approval and provisioning. You can apply to join the Daybreak program [here](https://openai.com/daybreak/). More details on pricing [here](https://developers.openai.com/api/docs/pricing).

### Aug 6

Update · Model: chat-latest

Updated the **chat-latest** snapshot, which points to the latest model available in ChatGPT for Plus and Pro users. We recommend leveraging [GPT-5.6 Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol) for production API usage, but feel free to use this model to test the latest improvements for chat use cases. The underlying model snapshot will be regularly updated. Read more [here](https://developers.openai.com/api/docs/models/chat-latest).

### Aug 5

Update · Model: gpt-5.6-sol · Model: gpt-5.6-terra · Model: gpt-5.6-luna

Fast mode now supports long-context requests for GPT-5.6 Sol, GPT-5.6 Terra, and GPT-5.6 Luna. As of today, long-context prompts exceeding 272K tokens can run in [Fast mode](https://developers.openai.com/api/docs/guides/fast-mode), delivering speeds up to 2.5× faster than the Standard tier. See [pricing details](https://developers.openai.com/api/docs/pricing).

### Aug 4

Feature

Customers can now filter and group data by API key in the [Usage and Costs dashboards](https://platform.openai.com/settings/organization/usage). The [Usage API](https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage) and [Costs API](https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs) also support the API key dimension for programmatic reporting and analysis.

## July, 2026

### Jul 30

Update · Model: gpt-5.6-sol · Model: gpt-5.6-terra · Model: gpt-5.6-luna · API: v1/responses · API: v1/chat/completions

Starting July 30, GPT-5.6 Luna costs 80% less, while GPT-5.6 Terra costs 20% less. See [pricing details](https://developers.openai.com/api/docs/pricing).

We're also introducing [Fast mode](https://developers.openai.com/api/docs/guides/fast-mode) in the API, which replaces our Priority Processing offering. For GPT-5.6 Sol, Fast mode now delivers up to 2.5× faster speeds than standard processing at twice the price. This change is backward compatible: requests tagged priority will automatically use Fast mode.

