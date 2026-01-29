# jb-serve

A service that hosts Python tools using [jumpboot](https://github.com/richinsley/jumpboot) as Go middleware for Python.

## Vision

**Jumpboot** is Go middleware for Python — it can create isolated Python environments, start/manage/communicate with multiple Python instances (each with different Python versions and packages) from a single Go binary.

**jb-serve** uses jumpboot to provide a tool server that:
- Accepts git repositories containing tool definitions (manifest + Python code)
- Creates isolated Python environments for each tool automatically
- Runs tools as **oneshot** services (launch → execute → shutdown) or **persistent** services (always-on)
- Exposes an HTTP API for agents (AI or human) to discover, manage, and call tools

```
┌─────────────────────────────────────────────────────────────────────┐
│  Agent (Moltbot, LLM, automation script, human)                     │
└─────────────────────────────────────────────────────────────────────┘
                              │ HTTP
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  jb-serve                                                           │
│  ├── HTTP API (/v1/tools/...) with auth                             │
│  ├── CLI (install, list, call, serve)                               │
│  ├── Tool Manager (install from git, registry, environments)        │
│  └── Executor (oneshot or persistent calls via jumpboot REPL)       │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  jumpboot (github.com/richinsley/jumpboot)                          │
│  ├── micromamba (isolated Python environments)                      │
│  ├── pip/conda package installation                                 │
│  └── REPL (Python execution without subprocess spawn overhead)      │
└─────────────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
   ┌────────────┐      ┌────────────┐      ┌────────────┐
   │ calculator │      │  whisper   │      │    sdxl    │
   │  (py3.11)  │      │  (py3.10)  │      │  (py3.10)  │
   │  oneshot   │      │ persistent │      │ persistent │
   └────────────┘      └────────────┘      └────────────┘
```

## Design Decisions

These decisions were made early to keep scope manageable:

| Decision | Choice | Rationale |
|----------|--------|-----------|
| RPC Transport | HTTP | Universal — every language can call it. Auth layer included. |
| Tool Discovery | CLI + HTTP | `jb-serve list --json` for local use, `/v1/tools` for agent queries |
| Environment Management | Jumpboot only | Jumpboot manages all Python environments via micromamba. No pyenv/uv. |
| Oneshot Cold Start | Pay cost every call | Simple first. Can optimize with warm pools later. |
| GPU Scheduling | User's problem | Too complex for v1. Tool manifest can specify GPU requirements as hints. |

## Installation

```bash
go install github.com/calobozan/jb-serve/cmd/jb-serve@latest
```

Or build from source:
```bash
git clone https://github.com/calobozan/jb-serve
cd jb-serve
go build -o jb-serve ./cmd/jb-serve
```

## Quick Start

```bash
# Install a tool from git
jb-serve install github.com/calobozan/jb-calculator

# List installed tools
jb-serve list

# Call a method (oneshot - starts Python, runs, exits)
jb-serve call calculator.add a=2 b=3
# {"result": 5}

# Or with JSON
jb-serve call calculator.add --json '{"a": 2, "b": 3}'

# Start HTTP API for agent access
jb-serve serve --port 9800
```

## Execution Modes

### Oneshot (default)
- Fresh Python REPL created for each call
- REPL closed after call completes
- Best for: stateless tools, infrequent calls, low-memory environments

```bash
jb-serve call calculator.add a=2 b=3
```

### Persistent
- Python REPL stays alive between calls
- Tool maintains state (loaded models, caches, connections)
- Must be explicitly started/stopped
- Best for: ML models, tools with expensive initialization (loading weights, warming up)

```bash
jb-serve start whisper           # Load model once
jb-serve call whisper.transcribe --audio file.wav
jb-serve call whisper.transcribe --audio another.wav  # Model already loaded
jb-serve stop whisper            # Unload
```

## Creating Tools

A tool is a git repository containing:
- `jumpboot.yaml` — Manifest describing environment and RPC interface
- `main.py` — Python entrypoint with callable functions (or custom entrypoint)

### Manifest Format (`jumpboot.yaml`)

```yaml
name: my-tool
version: 1.0.0
description: |
  What this tool does. Can be multi-line.
  Agents use this for discovery.

# Capabilities for agent discovery - what can this tool do?
capabilities:
  - "transcribe audio to text"
  - "supports 50+ languages"
  - "GPU accelerated"

runtime:
  python: "3.11"              # Python version
  mode: oneshot               # "oneshot" or "persistent"
  entrypoint: main.py         # Python file with functions (default: main.py)
  startup_timeout: 60         # Seconds to wait for persistent tools to start
  
  # Package installation (in order: conda → pip → requirements.txt)
  conda_packages:             # Installed via micromamba
    - ffmpeg
    - cudatoolkit=11.8
  packages:                   # Installed via pip
    - torch
    - transformers
  requirements: requirements.txt  # Optional requirements file

# Resource hints (informational for now, may be used for scheduling later)
resources:
  gpu: true
  vram_gb: 12
  ram_gb: 16

# RPC interface - what methods does this tool expose?
rpc:
  methods:
    transcribe:
      description: "Transcribe audio file to text"
      input:
        type: object
        properties:
          audio_path:
            type: string
            description: "Path to audio file"
          language:
            type: string
            description: "Language code (auto-detect if omitted)"
            default: null
        required: [audio_path]
      output:
        type: object
        properties:
          text:
            type: string
          language:
            type: string
          duration_seconds:
            type: number

# Health check for persistent tools
health:
  endpoint: /health           # Default health check endpoint
  interval: 30                # Seconds between checks
```

### Python Implementation

Functions are called via jumpboot's REPL and must return JSON strings:

```python
import json

def transcribe(audio_path: str, language: str = None) -> str:
    """Transcribe audio to text."""
    # Your implementation here
    result = {
        "text": "Hello world",
        "language": "en",
        "duration_seconds": 3.5
    }
    return json.dumps(result)

def health() -> str:
    """Health check for persistent mode."""
    return json.dumps({"status": "ok"})
```

## HTTP API

All endpoints return JSON. Use `Authorization: Bearer <token>` header when auth is configured.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Server health check |
| `/v1/tools` | GET | List all tools with capabilities |
| `/v1/tools/{name}` | GET | Tool info and available methods |
| `/v1/tools/{name}/schema` | GET | Full RPC schema for all methods |
| `/v1/tools/{name}/{method}` | POST | Call a method with JSON body |

### Examples

```bash
# List tools (agent discovery)
curl http://localhost:9800/v1/tools

# Get tool info
curl http://localhost:9800/v1/tools/calculator

# Call a method
curl -X POST http://localhost:9800/v1/tools/calculator/add \
  -H "Content-Type: application/json" \
  -d '{"a": 2, "b": 3}'
# {"result": 5}
```

## CLI Reference

```bash
# Tool management
jb-serve install <git-url|path>   # Install tool from git repo or local path
jb-serve list [--json]            # List installed tools
jb-serve info <tool>              # Show tool details and methods
jb-serve schema <tool>[.method]   # Show RPC schema

# Execution
jb-serve call <tool.method> [key=value ...]   # Call with key=value params
jb-serve call <tool.method> --json '{...}'    # Call with JSON params
jb-serve start <tool>             # Start a persistent tool
jb-serve stop <tool>              # Stop a persistent tool

# Server
jb-serve serve [--port 9800]      # Start HTTP API server
```

## Configuration

jb-serve stores data in `~/.jb-serve/`:

```
~/.jb-serve/
├── config.yaml     # Optional configuration
├── tools/          # Installed tools (cloned repos or symlinks)
├── envs/           # Jumpboot Python environments
└── run/            # Runtime state (PIDs, etc.)
```

### config.yaml

```yaml
tools_dir: ~/.jb-serve/tools
envs_dir: ~/.jb-serve/envs
run_dir: ~/.jb-serve/run
api_port: 9800
auth_token: "your-secret-token"  # Optional - enables auth on HTTP API
```

## Current Status

### ✅ Working (v0.1.0)
- [x] Install tools from git URL or local path
- [x] Isolated Python environments via jumpboot/micromamba
- [x] Oneshot execution (call → run → exit)
- [x] HTTP API: `/health`, `/v1/tools`, `/v1/tools/{name}`, `/v1/tools/{name}/schema`, `/v1/tools/{name}/{method}`
- [x] CLI: `install`, `list`, `info`, `schema`, `call`, `serve`
- [x] Schema-aware parameter parsing (string → number/bool conversion)
- [x] Auth token middleware (via config.yaml)
- [x] Dynamic parameters via `key=value` syntax or `--json`

### 🚧 In Progress
- [ ] Persistent tool mode (`start`/`stop` commands exist, needs testing)

### 📋 Planned
- [ ] Health checks for persistent tools
- [ ] Better error messages and validation
- [ ] GPU resource hints in scheduling
- [ ] Tool hot-reload without restart
- [ ] Warm pool for frequently-called oneshot tools

## Example Tools

| Tool | Description | Mode |
|------|-------------|------|
| [jb-calculator](https://github.com/calobozan/jb-calculator) | Reference implementation — basic math | oneshot |
| jb-whisper | Audio transcription (planned) | persistent |
| jb-embed | Text embeddings (planned) | persistent |
| jb-sdxl | Image generation (planned) | persistent |

## License

MIT
