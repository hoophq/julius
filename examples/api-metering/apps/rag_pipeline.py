"""rag-pipeline — the real Anthropic SDK streaming API, through julius.

Streaming is forwarded verbatim; julius reconstructs exact usage from the
message_start / message_delta events without buffering the stream.
"""
import anthropic

client = anthropic.Anthropic(
    api_key="demo-key",
    default_headers={"X-Julius-App": "rag-pipeline"},
)

with client.messages.stream(
    model="claude-opus-4-8",
    max_tokens=2048,
    messages=[{"role": "user", "content": "Answer from the retrieved docs: refund status for #4821"}],
) as stream:
    chunks = sum(1 for _ in stream.text_stream)
print(f"[rag-pipeline] streamed answer in {chunks} chunks")
