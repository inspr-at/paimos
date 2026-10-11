# Public catalog fixtures

OpenRouter, models.dev, and LiteLLM are reduced public response recordings from
2026-10-11, retaining only fields consumed by this package. Tests read these
files through an injected HTTP transport and never fetch the network.

Native fixtures are reduced documentation response examples (OpenAI/Anthropic)
and a Gemini response-shaped sample for its documented thinking boolean; numeric
context limits are irrelevant to the level assertion. They do not grant accounts,
prove CLI availability, or become bootstrap data.

Sources:
- https://openrouter.ai/api/v1/models
- https://models.dev/api.json
- https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
- https://developers.openai.com/api/reference/resources/models/methods/list
- https://platform.claude.com/docs/en/api/models/list
- https://ai.google.dev/api/models
