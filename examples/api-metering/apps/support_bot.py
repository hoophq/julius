"""support-bot — a production-style service using the real Anthropic SDK.

Note what is NOT here: no julius import, no proxy code. The only integration
is the ANTHROPIC_BASE_URL env var (set by run.sh) plus one optional header
to label this app's traffic in `julius savings`.
"""
import anthropic

client = anthropic.Anthropic(
    api_key="demo-key",  # normally from env; the offline provider ignores auth
    default_headers={"X-Julius-App": "support-bot"},
)

msg = client.messages.create(
    model="claude-opus-4-8",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Where is order #4821's refund?"}],
)
print(f"[support-bot] answered: {msg.content[0].text[:60]}...")
