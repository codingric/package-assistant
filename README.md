# package-assistant

Receives CloudMailIn email webhooks and uses a local LLM (llama.cpp server,
OpenAI-compatible chat completions API) to decide whether the email reports a
package as **delivered** — shipping/dispatch/customs/out-for-delivery notices
do not count — and to extract the tracking number and carrier.

Webhooks are acknowledged immediately with `200 {"status": "accepted"}` and
classified in the background, because LLM inference is slower than webhook
senders' timeouts. The verdict — delivery with carrier/tracking and evidence,
not-a-delivery, or classification error — is written to the logs.

## Configuration

| Env var       | Default                                          | Description                              |
| ------------- | ------------------------------------------------ | ---------------------------------------- |
| `LLM_API_URL` | `http://llm-classifier:8080/v1/chat/completions` | Chat completions endpoint                |
| `LLM_MODEL`   | `/models/qwen2.5-3b-instruct-q4_k_m.gguf`        | Model name sent in the request           |
| `LLM_API_KEY` | *(empty)*                                        | Bearer token, if the server requires one |

The server listens on port `8080`.
