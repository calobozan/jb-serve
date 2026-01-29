# jb-serve Project Plan

## Vision

A generic server that hosts multiple Python tools, each with its own isolated environment. Tools are git repos with manifests that describe their capabilities, dependencies, and RPC interface. An agent (AI or human) can discover available tools and call them via CLI or HTTP API.

## Current State (Phase 1 ✅)

### What We Have

**jb-serve** (`github.com/calobozan/jb-serve`)
- Go binary that manages Python tools using jumpboot
- CLI commands: `install`, `list`, `info`, `schema`, `call`, `start`, `stop`, `serve`
- HTTP API: `/v1/tools`, `/v1/tools/{name}`, `/v1/tools/{name}/{method}`
- Uses `jumpboot.CreateEnvironmentMamba()` for isolated Python environments
- Uses `jumpboot.NewREPLPythonProcess()` for executing tool methods
- Oneshot mode working (spins up REPL per call)

**jb-calculator** (`github.com/calobozan/jb-calculator`)
- Reference tool demonstrating the manifest format
- Methods: add, subtract, multiply, divide, eval
- Python 3.11, no external dependencies

**Manifest Format** (`jumpboot.yaml`)
```yaml
name: tool-name
version: 1.0.0
description: What the tool does
capabilities:
  - what it can do (for agent discovery)
runtime:
  python: "3.11"
  mode: oneshot | persistent
  packages: [pip-packages]
  conda_packages: [conda-packages]
  requirements: requirements.txt
rpc:
  methods:
    method_name:
      description: "What it does"
      input:
        type: object
        properties: { ... }
        required: [...]
      output:
        type: object
        properties: { ... }
```

### What Works
- ✅ Install tools from local path or git URL
- ✅ List installed tools with status
- ✅ Get tool info and method schemas
- ✅ Call oneshot tool methods via CLI
- ✅ Schema-aware parameter type conversion
- ✅ HTTP API for programmatic access
- ✅ Jumpboot environment creation (micromamba)
- ✅ Pip package installation

### Known Limitations
- Persistent mode (long-running tools) not fully tested
- Debug output from jumpboot REPL shows in output
- No auth on HTTP API yet
- Limited CLI parameter flags (hardcoded common ones)

---

## Phase 2: Robustness & Persistent Tools

### Goals
- [ ] Persistent tool mode working reliably
- [ ] Suppress jumpboot debug output
- [ ] Dynamic CLI flags from schema
- [ ] Auth token support for HTTP API
- [ ] Better error handling and reporting
- [ ] Health checks for persistent tools

---

## Phase 3: Real Tools

### Goals
Build actual useful tools:
- [ ] **jb-whisper** - Audio transcription (whisper)
- [ ] **jb-embed** - Text embeddings (sentence-transformers) 
- [ ] **jb-sdxl** - Image generation (diffusers)
- [ ] **jb-llama** - LLM inference (llama.cpp or similar)

Each demonstrates different patterns:
- Whisper: File input, GPU optional
- Embed: Batch processing, model loading
- SDXL: Heavy GPU, long generation
- Llama: Streaming output, conversation state

---

## Phase 4: Advanced Features

### Goals
- [ ] Tool hot-reload without restart
- [ ] GPU resource tracking
- [ ] LRU environment eviction (for memory)
- [ ] Tool registry/discovery service
- [ ] Moltbot integration (as a skill or tool source)

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  jb-serve                                               │
│  ├── CLI (cobra)                                        │
│  ├── HTTP API (net/http)                                │
│  ├── Tool Manager                                       │
│  │   ├── Install (git clone + manifest parse)          │
│  │   ├── Environment (jumpboot.CreateEnvironmentMamba) │
│  │   └── Registry (tracks installed tools)             │
│  └── Executor                                           │
│      ├── Oneshot (new REPL per call)                   │
│      └── Persistent (keep REPL alive)                  │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│  jumpboot (github.com/richinsley/jumpboot)              │
│  ├── CreateEnvironmentMamba (micromamba envs)           │
│  ├── PipInstallPackages                                 │
│  └── NewREPLPythonProcess (Python execution)            │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────┐  ┌──────────┐  ┌──────────┐
│calculator│  │ whisper  │  │  sdxl    │  ...
│ (py3.11) │  │ (py3.10) │  │ (py3.10) │
└──────────┘  └──────────┘  └──────────┘
```

## Key Files

```
~/projects/jb-serve/
├── cmd/jb-serve/main.go     # CLI entry point
├── internal/
│   ├── config/
│   │   ├── config.go        # Global config (~/.jb-serve/)
│   │   └── manifest.go      # jumpboot.yaml schema
│   ├── tools/
│   │   ├── manager.go       # Tool install/registry
│   │   └── executor.go      # Method execution
│   └── server/
│       └── server.go        # HTTP API
└── PROJECT.md               # This file

~/projects/jb-calculator/
├── jumpboot.yaml            # Tool manifest
└── main.py                  # Python implementation
```

## Usage Examples

```bash
# Install
jb-serve install github.com/calobozan/jb-calculator
jb-serve install ~/projects/my-tool

# Discover
jb-serve list
jb-serve info calculator
jb-serve schema calculator.add

# Call
jb-serve call calculator.add --a 2 --b 3
jb-serve call calculator.eval --expression "2+3*4"

# HTTP
jb-serve serve --port 9800
curl http://localhost:9800/v1/tools
curl -X POST http://localhost:9800/v1/tools/calculator/add -d '{"a":2,"b":3}'
```
