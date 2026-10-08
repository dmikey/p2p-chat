# Contributor agent runtime

This adapter uses the MIT-licensed [OpenAI Agents SDK](https://github.com/openai/openai-agents-python), pinned in `requirements.lock`. The Go network owns identity, transport, consent, output approval and lifecycle; the SDK owns the bounded model/tool loop. Buyers do not configure or see the harness.

Requires Python 3.12+. Create a venv, install `requirements.lock`, and supply your own `OPENAI_API_KEY` and a random `HARNESS_TOKEN` of at least 24 characters through your private environment. Run `python runtime.py`; it binds only `127.0.0.1:8811`. In native Agent studio, choose **Local agent runtime** and enter the runtime token. The model is selected by `AGENT_MODEL` on the runner (default `gpt-4o-mini`). Do not publish this HTTP port; the Go agent is the P2P entry point.

The Dockerfile provides a non-root runtime. Bind its port to loopback only (`127.0.0.1:8811:8811`), use a read-only root filesystem, drop all capabilities, and pass credentials from a private env file. This process does not mount user directories or grant account access.

Customize `SKILLS` and `build_agent()` to create and modify agent behaviors. The only executable function is bounded arithmetic; there is no shell, filesystem, browser, arbitrary code, or OAuth access. Those require a separately isolated sandbox and explicit grants before they can be offered. Tracing is disabled, Responses storage is disabled, requests have deadlines, and runs permit at most three turns and 600 output tokens per model call. Task prompts are still sent to the selected model provider. This is a text/tool harness, not an arbitrary-code sandbox.

Rad Ninja's commercial rental controller, provider credentials, and pricing are maintained outside this repository. This adapter is reusable by contributors with their own credentials.
