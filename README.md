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
git clone https://github.com/calobozan/jb-serve
cd jb-serve
go install ./cmd/jb-serve
```

## Quickstart: Generate an Image

This walkthrough installs jb-serve, adds an image generation tool, and generates your first image.

### 1. Install jb-serve

```bash
# Clone the repo
git clone https://github.com/calobozan/jb-serve
cd jb-serve

# Build and install
go install ./cmd/jb-serve

# Verify installation
jb-serve --help
```

### 2. Install a tool

Install the Z-Image-Turbo image generation tool (requires GPU with 16GB+ VRAM):

```bash
jb-serve install https://github.com/calobozan/jb-z-image-turbo
```

This will:
- Create an isolated Python 3.11 environment
- Install dependencies (torch, diffusers, transformers, etc.)
- Download the model (~15GB) during setup
- Register the tool with jb-serve

### 3. Start the HTTP server

```bash
jb-serve serve --port 9800
```

### 4. Start the tool

Persistent tools (like ML models) need to be started before use:

```bash
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/start
# {"status":"started","tool":"z-image-turbo"}
```

### 5. Generate an image

```bash
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/generate \
  -H "Content-Type: application/json" \
  -d '{"prompt": "A serene mountain lake at sunset with snow-capped peaks"}'
```

Response:
```json
{
  "image": {
    "ref": "a1b2c3d4",
    "url": "/v1/files/a1b2c3d4.png",
    "path": "/tmp/jb-serve/outputs/a1b2c3d4.png",
    "size": 1547832,
    "media_type": "image/png"
  },
  "width": 1024,
  "height": 1024,
  "prompt": "A serene mountain lake at sunset with snow-capped peaks",
  "seed": null
}
```

### 6. Download the image

```bash
curl -o mountain-lake.png http://localhost:9800/v1/files/a1b2c3d4.png
open mountain-lake.png  # macOS
```

### 7. Stop when done

```bash
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/stop
# {"status":"stopped","tool":"z-image-turbo"}
```

---

## More Examples

```bash
# List installed tools
jb-serve list

# Tool info and available methods
jb-serve info z-image-turbo

# Call via CLI (starts tool automatically)
jb-serve call z-image-turbo.generate prompt="A cat in space"
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
| [jb-whisper](https://github.com/calobozan/jb-whisper) | Audio transcription (OpenAI Whisper) | persistent | msgpack |
| [jb-z-image-turbo](https://github.com/calobozan/jb-z-image-turbo) | Fast image generation (6B params, 8-step) | persistent | msgpack |
| [jb-deepseek-ocr](https://github.com/calobozan/jb-deepseek-ocr) | Document OCR to Markdown | persistent | msgpack |

## License

MIT
