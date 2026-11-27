# package-assistant

Receives CloudMailIn email webhooks and uses a local LLM (llama.cpp server,
OpenAI-compatible chat completions API) to decide whether the email reports a
package as **delivered** — shipping/dispatch/customs/out-for-delivery notices
and expected or estimated delivery dates do not count — and to extract the
tracking number and carrier. A delivery verdict is only accepted when the
model's `evidence_phrase` appears verbatim in the email **and** describes a
completed delivery — wording about future deliveries ("arriving", "expected",
"out for delivery", ...) or bare status labels from order-progress trackers
("Delivered") are treated as non-deliveries.

The service responds only after the LLM has classified the email: `200` with
`{"tracking": "...", "provider": "..."}` for deliveries, `400` for
non-deliveries and missing tracking numbers, and `502` if the LLM cannot be
reached or its answer cannot be parsed. Callers must tolerate the inference
latency (tens of seconds per email on a small CPU-hosted model).

## Endpoints

| Path      | Method | Description                                                                     |
| --------- | ------ | ------------------------------------------------------------------------------- |
| `/parse`  | POST   | CloudMailIn webhook; classifies the email as described above                    |
| `/health` | GET    | `200 {"status":"ok"}` when the LLM API is reachable, `503` otherwise            |

`/health` queries the LLM server's `/v1/models` (no inference) and is suitable
as a Kubernetes readiness probe. Point the CloudMailIn webhook URL at `/parse`.

## Configuration

| Env var       | Default                                          | Description                              |
| ------------- | ------------------------------------------------ | ---------------------------------------- |
| `LLM_API_URL` | `http://llm-classifier:8080/v1/chat/completions` | Chat completions endpoint                |
| `LLM_MODEL`   | `/models/qwen2.5-3b-instruct-q4_k_m.gguf`        | Model name sent in the request           |
| `LLM_API_KEY` | *(empty)*                                        | Bearer token, if the server requires one |

The server listens on port `8080`.
