"""
05-openai-agent: wire emberd into a GPT-4o agent as a secure code-execution tool.

The agent loop gives GPT-4o one tool — execute_code — backed by emberd.
When the model decides to run code, a real Firecracker microVM executes it and
the result (stdout / stderr / exit_code) is fed back to the model.

Usage:
  OPENAI_API_KEY=sk-... python main.py
  OPENAI_API_KEY=sk-... python main.py "What is 2^32 in Python?"

Requires:
  pip install openai
  emberd running on 127.0.0.1:7777 (override with EMBERD_ADDR)
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

try:
    from openai import OpenAI
except ImportError:
    sys.exit("openai package not found — run: pip install openai")

BASE_URL = f"http://{os.getenv('EMBERD_ADDR', '127.0.0.1:7777')}"


# ---------------------------------------------------------------------------
# emberd client
# ---------------------------------------------------------------------------


def _req(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        f"{BASE_URL}{path}",
        data=data,
        method=method,
        headers={"Content-Type": "application/json"} if data else {},
    )
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"HTTP {e.code}: {e.read().decode()}") from e


def create_sandbox(language_pack="python"):
    return _req("POST", "/sandboxes", {"language_pack": language_pack})["id"]


def exec_code(sandbox_id, code, timeout_ms=10_000, _retries=6):
    for attempt in range(_retries):
        try:
            return _req(
                "POST",
                f"/sandboxes/{sandbox_id}/exec",
                {"code": code, "timeout_ms": timeout_ms},
            )
        except RuntimeError:
            if attempt == _retries - 1:
                raise
            time.sleep(0.3)


def destroy_sandbox(sandbox_id):
    _req("DELETE", f"/sandboxes/{sandbox_id}")


# ---------------------------------------------------------------------------
# Tool definition the model sees
# ---------------------------------------------------------------------------

TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "execute_code",
            "description": (
                "Execute code safely inside an isolated Firecracker microVM sandbox. "
                "Returns stdout, stderr, and exit code. "
                "Use the 'python' pack for Python and the 'shell' pack for bash/sh commands."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "code": {
                        "type": "string",
                        "description": "The code to execute.",
                    },
                    "language": {
                        "type": "string",
                        "enum": ["python", "shell"],
                        "description": "Language pack. Defaults to 'python'.",
                    },
                },
                "required": ["code"],
            },
        },
    }
]

SYSTEM_PROMPT = """\
You are a helpful assistant with access to a secure, isolated code execution sandbox \
powered by Firecracker microVMs. Use the execute_code tool whenever a question is best \
answered by running code rather than reasoning about it. Prefer concise, correct programs. \
Always explain what the code does and summarise the result in plain language after running it.\
"""


# ---------------------------------------------------------------------------
# Agent loop
# ---------------------------------------------------------------------------


def run_agent(client: OpenAI, user_message: str) -> str:
    sandboxes: dict[str, str] = {}

    def get_sandbox(language: str) -> str:
        if language not in sandboxes:
            print(f"  [emberd] booting {language} sandbox...", flush=True)
            sandboxes[language] = create_sandbox(language)
            print(f"  [emberd] {sandboxes[language]}", flush=True)
        return sandboxes[language]

    messages = [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": user_message},
    ]

    try:
        while True:
            response = client.chat.completions.create(
                model="gpt-5.5",
                messages=messages,
                tools=TOOLS,
                tool_choice="auto",
            )

            choice = response.choices[0]
            msg = choice.message
            messages.append(msg)

            if not msg.tool_calls:
                return msg.content or ""

            for tc in msg.tool_calls:
                args = json.loads(tc.function.arguments)
                language = args.get("language", "python")
                code = args["code"]

                print(f"\n  [tool call] execute_code (language={language})")
                print("  " + "\n  ".join(code.strip().splitlines()))

                sb_id = get_sandbox(language)
                result = exec_code(sb_id, code)

                print(f"  [exit {result['exit_code']} | {result['duration_ms']} ms]")
                if result.get("stdout"):
                    print("  stdout:", result["stdout"].rstrip().replace("\n", "\n  "))

                tool_result = json.dumps(
                    {
                        "stdout": result["stdout"],
                        "stderr": result["stderr"],
                        "exit_code": result["exit_code"],
                        "error": result.get("error", ""),
                    }
                )
                messages.append(
                    {
                        "role": "tool",
                        "tool_call_id": tc.id,
                        "content": tool_result,
                    }
                )
    finally:
        for lang, sb_id in sandboxes.items():
            print(f"\n  [emberd] destroying {lang} sandbox {sb_id}...")
            destroy_sandbox(sb_id)


# ---------------------------------------------------------------------------
# Demo questions
# ---------------------------------------------------------------------------

DEMO_QUESTIONS = [
    "What is 2 to the power of 32?",
    "List the files in /tmp and tell me what's there.",
    "Generate the first 10 Fibonacci numbers and show me the ratio between consecutive terms.",
]


def main():
    api_key = os.getenv("OPENAI_API_KEY")
    if not api_key:
        sys.exit("Set OPENAI_API_KEY before running.")

    client = OpenAI(api_key=api_key)

    questions = sys.argv[1:] if len(sys.argv) > 1 else DEMO_QUESTIONS

    for i, question in enumerate(questions, 1):
        print(f"\n{'=' * 60}")
        print(f"Question {i}: {question}")
        print("=" * 60)
        answer = run_agent(client, question)
        print(f"\nAnswer:\n{answer}")


if __name__ == "__main__":
    main()
