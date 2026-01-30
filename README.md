# jb-serve

A tool server that hosts Python tools using [jumpboot](https://github.com/richinsley/jumpboot) for isolated environments and seamless Go-Python communication.

## Features

- **Isolated environments**: Each tool gets its own Python environment (via jumpboot/micromamba)
- **Two execution modes**: Oneshot (per-call) or persistent (always-on with loaded models)
- **Two transport modes**: REPL (simple) or MessagePack (stdout-safe for progress bars)
- **HTTP API**: RESTful endpoints for tool discovery and execution
- **File handling**: Upload inputs, download outputs via `/v1/files/{ref}`

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  Agent (Moltbot, LLM, automation script, human)                     │
└─────────────────────────────────────────────────────────────────────┘
                              │ HTTP
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  jb-serve                                                           │
│  ├── HTTP API (/v1/tools/...)                                       │
│  ├── CLI (install, list, call, serve)                               │
│  ├── Tool Manager (environments, health checks)                     │
│  └── Executor (REPL or MessagePack transport)                       │
└─────────────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
   ┌────────────┐      ┌────────────┐      ┌────────────┐
   │ calculator │      │  whisper   │      │ z-image    │
   │  (oneshot) │      │(persistent)│      │ (msgpack)  │
   └────────────┘      └────────────┘      └────────────┘
```

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
# Install a tool
jb-serve install github.com/calobozan/jb-calculator

# List installed tools
jb-serve list

# Call a method (oneshot)
jb-serve call calculator.add a=2 b=3

# Start HTTP API
jb-serve serve --port 9800
```

## Execution Modes

### Oneshot (default)
Fresh Python process for each call. Best for stateless tools.

```bash
jb-serve call calculator.add a=2 b=3
```

### Persistent
Python process stays alive between calls. Best for ML models with expensive initialization.

```bash
curl -X POST http://localhost:9800/v1/tools/whisper/start
curl -X POST http://localhost:9800/v1/tools/whisper/transcribe -d '{"audio": "/path/to/file.wav"}'
curl -X POST http://localhost:9800/v1/tools/whisper/stop
```

## Transport Modes

### REPL (default)
Simple stdout-based communication. Works for most tools.

### MessagePack
Binary protocol over pipes. Use when your tool has progress bars, tqdm, or writes to stdout.

```yaml
# jumpboot.yaml
runtime:
  python: "3.11"
  mode: persistent
  transport: msgpack  # Enable MessagePack
```

## Creating Tools

Tools use the [jb-service](https://github.com/calobozan/jb-service) Python SDK.

### Basic Tool

**main.py:**
```python
from jb_service import Service, method, run

class Calculator(Service):
    @method
    def add(self, a: float, b: float) -> float:
        return a + b

if __name__ == "__main__":
    run(Calculator)
```

**jumpboot.yaml:**
```yaml
name: calculator
version: 1.0.0

runtime:
  python: "3.11"
  mode: oneshot
  packages:
    - git+https://github.com/calobozan/jb-service.git

rpc:
  methods:
    add:
      description: Add two numbers
```

### Tool with Progress Bars (MessagePack)

**main.py:**
```python
from jb_service import MessagePackService, method, run, save_image

class ImageGenerator(MessagePackService):
    def setup(self):
        from diffusers import SomePipeline
        self.pipe = SomePipeline.from_pretrained(...)  # Progress bars OK!
    
    @method
    def generate(self, prompt: str) -> dict:
        result = self.pipe(prompt)  # tqdm OK!
        path = save_image(result.images[0])
        return {"image": path}

if __name__ == "__main__":
    run(ImageGenerator)
```

**jumpboot.yaml:**
```yaml
name: image-gen
version: 1.0.0

runtime:
  python: "3.11"
  mode: persistent
  transport: msgpack
  packages:
    - diffusers
    - torch
    - git+https://github.com/calobozan/jb-service.git
```

## HTTP API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Server health |
| `/v1/tools` | GET | List all tools |
| `/v1/tools/{name}` | GET | Tool info |
| `/v1/tools/{name}/start` | POST | Start persistent tool |
| `/v1/tools/{name}/stop` | POST | Stop persistent tool |
| `/v1/tools/{name}/{method}` | POST | Call a method |
| `/v1/files/{ref}` | GET | Download output file |

### Examples

```bash
# List tools
curl http://localhost:9800/v1/tools

# Start a persistent tool
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/start

# Generate an image
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/generate \
  -H "Content-Type: application/json" \
  -d '{"prompt": "A meadow at sunset"}'
# Response: {"image": {"ref": "abc123", "url": "/v1/files/abc123.png", ...}}

# Download the image
curl -o output.png http://localhost:9800/v1/files/abc123.png

# Upload a file (multipart)
curl -X POST http://localhost:9800/v1/tools/whisper/transcribe \
  -F "audio=@local-file.wav"
```

## File Handling

### Outputs
Methods returning file paths get wrapped as FileRef objects:

```json
{
  "image": {
    "ref": "43af6f50",
    "url": "/v1/files/43af6f50.png",
    "path": "/absolute/path/to/file.png",
    "size": 1211477,
    "media_type": "image/png"
  }
}
```

### Inputs
Two options:
1. **Server path**: `{"audio": "/path/on/server.wav"}`
2. **Multipart upload**: `-F "audio=@local.wav"`

## CLI Reference

```bash
jb-serve install <url|path>       # Install tool
jb-serve list                     # List tools
jb-serve info <tool>              # Tool details
jb-serve call <tool.method> ...   # Call method
jb-serve start <tool>             # Start persistent
jb-serve stop <tool>              # Stop persistent
jb-serve serve --port 9800        # HTTP server
```

## Example Tools

| Tool | Description | Mode | Transport |
|------|-------------|------|-----------|
| [jb-calculator](https://github.com/calobozan/jb-calculator) | Basic math | oneshot | repl |
| jb-whisper | Audio transcription | persistent | repl |
| jb-z-image-turbo | Image generation | persistent | msgpack |

## License

MIT
