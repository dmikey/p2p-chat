"""Contributor-owned agent harness. Credentials and tools stay on the runner.
Extend build_agent() to add bounded skills; never grant accounts implicitly.
"""
import asyncio
import hmac
import json
import math
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from agents import Agent, Runner, ModelSettings, RunConfig, function_tool, set_tracing_disabled
from openai import AsyncOpenAI
from agents.models.openai_provider import OpenAIProvider

set_tracing_disabled(True)


@function_tool
def calculate(operation: str, left: float, right: float) -> str:
    """Perform bounded arithmetic. Operation: add, subtract, multiply or divide."""
    if not all(math.isfinite(x) and abs(x) <= 1e12 for x in (left, right)):
        return "Numbers must be finite and at most one trillion."
    operations = {"add": lambda: left + right, "subtract": lambda: left - right,
                  "multiply": lambda: left * right, "divide": lambda: left / right}
    if operation not in operations or (operation == "divide" and right == 0):
        return "Unsupported operation or division by zero."
    value = operations[operation]()
    return str(value) if math.isfinite(value) else "Result exceeded limits."

SKILLS = {
 "plan": "Turn a goal into an actionable, concise plan. Identify assumptions. Do not claim to take actions.",
 "write": "Help write and revise clear, friendly prose. Return a useful draft, not a claim that it was sent.",
 "numbers": "Help with practical arithmetic. Use calculate for calculations. Explain assumptions and units.",
}

def build_agent(skill: str, model: str) -> Agent:
    return Agent(name="Work assistant", instructions=SKILLS[skill] +
        " You only have the user's selected task. No workspace history, files, accounts, browser or shell access."
        " Be concise. Never claim that payments, messages, bookings or account changes happened.",
        model=model, tools=[calculate] if skill == "numbers" else [],
        model_settings=ModelSettings(max_tokens=600, store=False, parallel_tool_calls=False))

async def execute(payload: dict) -> str:
    prompt = payload.get("prompt", "")
    skill = payload.get("skill", "plan")
    model = os.environ.get("AGENT_MODEL", "gpt-4o-mini")
    if not isinstance(prompt, str) or not 1 <= len(prompt.encode()) <= 4000 or skill not in SKILLS:
        raise ValueError("Invalid task")
    async with AsyncOpenAI(max_retries=0, timeout=45) as client:
        result = await asyncio.wait_for(Runner.run(build_agent(skill, model), prompt,
            max_turns=3, run_config=RunConfig(model_provider=OpenAIProvider(openai_client=client),
            tracing_disabled=True, trace_include_sensitive_data=False)), 55)
    text = str(result.final_output)
    if not text.strip() or len(text.encode()) > 12000:
        raise ValueError("Invalid result")
    return text

capacity = __import__('threading').BoundedSemaphore(2)
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def reply(self, code, data):
        body = json.dumps(data).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_GET(self):
        self.reply(200 if self.path == "/healthz" else 404, {"ok": self.path == "/healthz"})
    def do_POST(self):
        token = os.environ.get("HARNESS_TOKEN", "")
        if not token or not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + token):
            self.reply(401, {"error": "Unauthorized"}); return
        if self.path != "/complete": self.reply(404, {"error": "Not found"}); return
        if not capacity.acquire(blocking=False): self.reply(429, {"error": "Runner busy"}); return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 8192: raise ValueError("Invalid size")
            self.connection.settimeout(10)
            payload = json.loads(self.rfile.read(length))
            text = asyncio.run(execute(payload))
            self.reply(200, {"text": text, "choices": [{"message": {"content": text}}]})
        except Exception:
            # Provider errors may contain request data. Do not log raw exceptions.
            self.reply(502, {"error": "Agent execution unavailable"})
        finally: capacity.release()

if __name__ == "__main__":
    if not os.environ.get("OPENAI_API_KEY") or len(os.environ.get("HARNESS_TOKEN", "")) < 24:
        raise SystemExit("OPENAI_API_KEY and a HARNESS_TOKEN of at least 24 characters are required")
    ThreadingHTTPServer((os.environ.get("HARNESS_BIND", "127.0.0.1"), 8811), Handler).serve_forever()
