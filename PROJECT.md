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

### Binary File Handling
Design doc: `docs/BINARY-HANDLING.md`

For tools like jb-whisper (audio in) and jb-sdxl (images out), we need:
- File input via path/url/base64
- File output with managed references
- `/v1/files/{ref}` endpoint for retrieval

### Candidates
- [ ] jb-whisper — Audio transcription (needs binary input)
- [ ] Auto-restart on health failure
- [ ] CLI daemon mode
- [ ] Re-enable structured logging (disabled due to REPL interference)

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

# Call methods
jb-serve call calculator.add a=5 b=3
jb-serve call calculator.divide a=10 b=2

# HTTP API
jb-serve serve --port 9800
curl -X POST http://localhost:9800/v1/tools/calculator/add \
  -H "Content-Type: application/json" \
  -d '{"a": 5, "b": 3}'
```
