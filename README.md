# package-assistant

Receives CloudMailIn email webhooks and uses a local LLM (llama.cpp server,
OpenAI-compatible chat completions API) to decide whether the email reports a
package as **delivered** — shipping/dispatch/customs/out-for-delivery notices
do not count — and to extract the tracking number and carrier.

The service responds only after the LLM has classified the email: `200` with
`{"tracking": "...", "provider": "..."}` for deliveries, `400` for
non-deliveries and missing tracking numbers, and `502` if the LLM cannot be
reached or its answer cannot be parsed. Callers must tolerate the inference
latency (tens of seconds per email on a small CPU-hosted model).

## Configuration

| Env var       | Default                                          | Description                              |
| ------------- | ------------------------------------------------ | ---------------------------------------- |
| `LLM_API_URL` | `http://llm-classifier:8080/v1/chat/completions` | Chat completions endpoint                |
| `LLM_MODEL`   | `/models/qwen2.5-3b-instruct-q4_k_m.gguf`        | Model name sent in the request           |
| `LLM_API_KEY` | *(empty)*                                        | Bearer token, if the server requires one |

The server listens on port `8080`.
