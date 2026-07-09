"""Offline stand-in LLM providers so this example runs with no API keys.

Speaks the real Anthropic (:5001) and OpenAI (:5002) wire formats — including
Anthropic SSE streaming — so the julius proxy meters them exactly as it would
the real services. Swap these for the real endpoints and nothing else changes.
"""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ANSWER = ("Based on the retrieved order history, the refund was processed on "
          "July 3rd and should appear within 5 business days.")


def _send(handler, body: bytes, content_type: str):
    handler.send_response(200)
    handler.send_header("Content-Type", content_type)
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


def _anthropic_json():
    return json.dumps({
        "id": "msg_demo", "type": "message", "role": "assistant",
        "model": "claude-opus-4-8",
        "content": [{"type": "text", "text": ANSWER}],
        "stop_reason": "end_turn",
        "usage": {"input_tokens": 2843, "output_tokens": 214,
                  "cache_creation_input_tokens": 512, "cache_read_input_tokens": 2048},
    }).encode()


def _anthropic_sse():
    events = [
        ("message_start", {"type": "message_start", "message": {
            "id": "msg_demo", "type": "message", "role": "assistant",
            "model": "claude-opus-4-8", "content": [], "stop_reason": None,
            "usage": {"input_tokens": 9120, "output_tokens": 1,
                      "cache_creation_input_tokens": 0, "cache_read_input_tokens": 8192}}}),
        ("content_block_start", {"type": "content_block_start", "index": 0,
                                 "content_block": {"type": "text", "text": ""}}),
    ]
    for word in ANSWER.split(" "):
        events.append(("content_block_delta", {"type": "content_block_delta", "index": 0,
                       "delta": {"type": "text_delta", "text": word + " "}}))
    events += [
        ("content_block_stop", {"type": "content_block_stop", "index": 0}),
        ("message_delta", {"type": "message_delta",
                           "delta": {"stop_reason": "end_turn"},
                           "usage": {"output_tokens": 388}}),
        ("message_stop", {"type": "message_stop"}),
    ]
    return "".join(f"event: {n}\ndata: {json.dumps(p)}\n\n" for n, p in events).encode()


def _openai_json():
    return json.dumps({
        "id": "chatcmpl-demo", "object": "chat.completion", "model": "gpt-5",
        "choices": [{"index": 0, "finish_reason": "stop",
                     "message": {"role": "assistant", "content": "Summary: 41 tickets, 3 escalations."}}],
        "usage": {"prompt_tokens": 5210, "completion_tokens": 96,
                  "prompt_tokens_details": {"cached_tokens": 4096}},
    }).encode()


class Anthropic(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if b'"stream": true' in body or b'"stream":true' in body:
            _send(self, _anthropic_sse(), "text/event-stream")
        else:
            _send(self, _anthropic_json(), "application/json")

    def log_message(self, *a):
        pass


class OpenAI(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        _send(self, _openai_json(), "application/json")

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    threading.Thread(
        target=ThreadingHTTPServer(("127.0.0.1", 5001), Anthropic).serve_forever,
        daemon=True,
    ).start()
    ThreadingHTTPServer(("127.0.0.1", 5002), OpenAI).serve_forever()
