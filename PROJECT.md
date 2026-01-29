# jb-serve Project Plan

## Vision

A generic server that hosts multiple Python tools, each with its own isolated environment. Tools are git repos with manifests that describe their capabilities, dependencies, and RPC interface. An agent (AI or human) can discover available tools and call them via CLI or HTTP API.

## Current State

### Phase 1 ✅ — Core Infrastructure
- Go binary with CLI and HTTP API
- Jumpboot integration for isolated Python environments
- Tool installation from git or local path
- Oneshot execution working

### Phase 2 ✅ — Python SDK (jb-service)
- **jb-service** Python package for tool authors
- Simple API: `Service` base class + `@method` decorator
- Pydantic validation for inputs
- Async method support
- `__jb_call__` protocol wired up between Go and Python

---

## Repositories

| Repo | Description |
|------|-------------|
| `github.com/calobozan/jb-serve` | Go server/CLI |
| `github.com/calobozan/jb-service` | Python SDK for tool authors |
| `github.com/calobozan/jb-calculator` | Reference oneshot tool (old style) |
| `~/projects/jb-calculator-new` | Reference tool using jb-service |
| `~/projects/jb-whisper` | Audio transcription tool (Whisper) |

---

## Creating a Tool (with jb-service)

**main.py:**
```python
from jb_service import Service, method, run

class Calculator(Service):
    name = "calculator"
    version = "1.0.0"
    
    @method
    def add(self, a: float, b: float) -> float:
        """Add two numbers."""
        return a + b

if __name__ == "__main__":
    run(Calculator)
```

**jumpboot.yaml:**
```yaml
name: calculator
version: 1.0.0
description: A simple calculator

runtime:
  python: "3.11"
  mode: oneshot
  packages:
    - pydantic>=2.0
    - git+https://github.com/calobozan/jb-service.git

rpc:
  methods:
    add:
      description: Add two numbers
```

**Install and use:**
```bash
jb-serve install ./my-tool
jb-serve call calculator.add a=5 b=3  # → 8
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  jb-serve (Go)                                          │
│  ├── CLI (cobra)                                        │
│  ├── HTTP API (net/http)                                │
│  ├── Tool Manager (install, list, info)                 │
│  └── Executor                                           │
│      ├── initializeService() — runs main.py            │
│      ├── doCall() — calls __jb_call__(method, params)  │
│      └── parseResponse() — JSON response handling      │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│  jumpboot REPL                                          │
│  ├── Executes Python code sent from Go                 │
│  └── Returns results via stdout                        │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│  jb-service (Python)                                    │
│  ├── Service base class                                │
│  ├── @method decorator                                 │
│  ├── run() — registers __jb_call__ in builtins        │
│  └── Protocol — handles calls, returns JSON           │
└─────────────────────────────────────────────────────────┘
```

---

## Next Up (Phase 3)

### Completed
- [x] **jb-whisper** — Audio transcription tool working
  - Uses file paths directly (no complex FileRef abstraction needed)
  - Persistent mode with model kept loaded
  - Works via HTTP API: `POST /v1/tools/whisper/transcribe {"audio": "/path/to/file.wav"}`

### Binary Handling Simplified
The `docs/BINARY-HANDLING.md` design is overkill for local use. Tools just:
- Take file paths as strings
- Read/write files directly
- Return paths in responses

The FileRef/multipart/managed-refs complexity can wait until we need remote HTTP clients uploading binary data.

### Remaining Candidates
- [ ] CLI daemon mode (so `jb-serve start` persists)
- [ ] Auto-restart on health failure
- [ ] Re-enable structured logging (disabled due to REPL interference)
- [ ] jb-sdxl — Image generation (when ready)

---

## Key Files

```
~/projects/jb-serve/
├── cmd/jb-serve/main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── manifest.go
│   ├── tools/
│   │   ├── manager.go
│   │   └── executor.go      # __jb_call__ protocol here
│   └── server/
│       └── server.go
├── docs/
│   ├── PYTHON-SDK.md        # jb-service documentation
│   └── BINARY-HANDLING.md   # File I/O design (not yet implemented)
└── PROJECT.md

~/projects/jb-service/
├── src/jb_service/
│   ├── __init__.py
│   ├── service.py           # Service base class
│   ├── method.py            # @method decorator
│   ├── protocol.py          # run(), __jb_call__
│   └── schema.py            # Pydantic → JSON schema
├── examples/calculator.py
└── tests/
```

---

## Usage

```bash
# Install a tool
jb-serve install ~/projects/jb-calculator-new
jb-serve install github.com/someone/their-tool

# List tools
jb-serve list

# Call oneshot methods via CLI
jb-serve call calculator.add a=5 b=3
jb-serve call calculator.divide a=10 b=2

# HTTP API (required for persistent tools)
jb-serve serve --port 9800

# Start persistent tool, then call
curl -X POST http://localhost:9800/v1/tools/whisper/start
curl -X POST http://localhost:9800/v1/tools/whisper/transcribe \
  -H "Content-Type: application/json" \
  -d '{"audio": "/path/to/audio.wav"}'
```

**Note:** Persistent tools (mode: persistent) require server mode — the CLI `start` command exits immediately, losing the REPL. Use `jb-serve serve` for persistent tools.
