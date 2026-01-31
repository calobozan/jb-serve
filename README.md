# jb-serve

A tool server for Python AI tools, similar to [ollama](https://ollama.ai) but for any Python tool. Uses [jumpboot](https://github.com/richinsley/jumpboot) for isolated environments and seamless Go-Python communication.

## Features

- **Ollama-style CLI**: `jb-serve start`, `jb-serve call`, etc. talk to a running server
- **Isolated environments**: Each tool gets its own Python environment (via jumpboot/micromamba)
- **Persistent tools**: Keep ML models loaded in memory between calls
- **MessagePack transport**: Progress bars, tqdm, and stdout work without breaking RPC
- **HTTP API**: RESTful endpoints for tool discovery and execution
- **File handling**: Upload inputs, download outputs via `/v1/files/{ref}`

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  Client (CLI, curl, agent, automation script)                       │
└─────────────────────────────────────────────────────────────────────┘
                              │ HTTP (:9800)
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  jb-serve serve                                                     │
│  ├── HTTP API (/v1/tools/...)                                       │
│  ├── Tool Manager (environments, health checks)                     │
│  └── Executor (REPL or MessagePack transport)                       │
└─────────────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
   ┌────────────┐      ┌────────────┐      ┌────────────┐
   │ calculator │      │  whisper   │      │ z-image    │
   │  (oneshot) │      │(persistent)│      │(persistent)│
   └────────────┘      └────────────┘      └────────────┘
```

## Installation

```bash
git clone https://github.com/calobozan/jb-serve
cd jb-serve
go install ./cmd/jb-serve
```

## Quickstart: Generate an Image

### 1. Install jb-serve

```bash
git clone https://github.com/calobozan/jb-serve
cd jb-serve
go install ./cmd/jb-serve
```

### 2. Start the server

```bash
jb-serve serve
# 2026/01/30 16:00:00 jb-serve API listening on :9800
```

### 3. Install a tool (new terminal)

```bash
jb-serve install https://github.com/calobozan/jb-z-image-turbo
```

This creates an isolated Python environment, installs dependencies, and downloads the model (~15GB).

### 4. Start the tool

```bash
jb-serve start z-image-turbo
# Started z-image-turbo
```

### 5. Generate an image

```bash
jb-serve call z-image-turbo.generate prompt="A cat floating in space"
```

Output:
```json
{
  "image": {
    "ref": "e204490b",
    "url": "/v1/files/e204490b.png",
    "path": "/home/user/.jb-serve/outputs/e204490b.png",
    "size": 1249637,
    "media_type": "image/png"
  },
  "width": 1024,
  "height": 1024,
  "prompt": "A cat floating in space"
}
```

### 6. Download the image

```bash
curl -o cat-in-space.png http://localhost:9800/v1/files/e204490b.png
open cat-in-space.png
```

### 7. Stop when done

```bash
jb-serve stop z-image-turbo
# Stopped z-image-turbo
```

---

## CLI Reference

The CLI communicates with a running `jb-serve serve` instance (like ollama).

```bash
# Server management
jb-serve serve [--port 9800]     # Start the HTTP server (run first!)

# Tool installation (standalone, no server needed)
jb-serve install <url|path>      # Install a tool from git URL or local path

# These commands talk to the server (requires jb-serve serve running)
jb-serve list                    # List installed tools and status
jb-serve info <tool>             # Show tool details and methods
jb-serve start <tool>            # Start a persistent tool
jb-serve stop <tool>             # Stop a persistent tool
jb-serve call <tool.method> ...  # Call a method
jb-serve schema <tool[.method]>  # Show RPC schema

# Connect to a different port
jb-serve --port 9801 list
```

### Calling Methods

```bash
# Key=value parameters
jb-serve call calculator.add a=2 b=3

# JSON parameters
jb-serve call z-image-turbo.generate --json '{"prompt": "A sunset", "width": 512}'

# File parameters (use server path)
jb-serve call whisper.transcribe audio=/path/to/audio.wav
```

## HTTP API

All CLI commands (except `install` and `serve`) use this API under the hood.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Server health check |
| `/v1/tools` | GET | List all tools |
| `/v1/tools/{name}` | GET | Tool info and methods |
| `/v1/tools/{name}/start` | POST | Start persistent tool |
| `/v1/tools/{name}/stop` | POST | Stop persistent tool |
| `/v1/tools/{name}/{method}` | POST | Call a method |
| `/v1/files/{ref}` | GET | Download output file |

### Examples with curl

```bash
# List tools
curl http://localhost:9800/v1/tools

# Start a tool
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/start

# Call a method
curl -X POST http://localhost:9800/v1/tools/z-image-turbo/generate \
  -H "Content-Type: application/json" \
  -d '{"prompt": "A meadow at sunset"}'

# Upload a file (multipart)
curl -X POST http://localhost:9800/v1/tools/whisper/transcribe \
  -F "audio=@recording.wav"

# Download output
curl -o output.png http://localhost:9800/v1/files/abc123.png
```

## Tool Modes

### Oneshot
Fresh Python process for each call. Best for stateless, fast tools.

```bash
jb-serve call calculator.add a=2 b=3
```

### Persistent
Python process stays alive between calls. Best for ML models with expensive initialization.

```bash
jb-serve start whisper          # Load model into memory
jb-serve call whisper.transcribe audio=/path/to/file.wav
jb-serve call whisper.transcribe audio=/path/to/another.wav  # Reuses loaded model
jb-serve stop whisper           # Unload model
```

## Creating Tools

Tools use the [jb-service](https://github.com/calobozan/jb-service) Python SDK.

### Simple Tool (Oneshot)

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

### ML Tool with Progress Bars (Persistent + MessagePack)

**main.py:**
```python
from jb_service import MessagePackService, method, run, save_image

# Model loaded lazily
PIPE = None

def get_pipeline():
    global PIPE
    if PIPE is None:
        from diffusers import SomePipeline
        print("Loading model...")  # OK with MessagePack!
        PIPE = SomePipeline.from_pretrained(...)  # Progress bars OK!
    return PIPE

class ImageGenerator(MessagePackService):
    @method
    def setup(self) -> dict:
        """Called during jb-serve install to download models."""
        pipe = get_pipeline()
        return {"status": "ok", "model_loaded": True}
    
    @method
    def generate(self, prompt: str) -> dict:
        pipe = get_pipeline()
        result = pipe(prompt)  # tqdm OK!
        path = save_image(result.images[0])
        return {"image": path}
    
    @method
    def health(self) -> dict:
        return {"status": "ok", "model_loaded": PIPE is not None}

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

setup:
  method: setup
  timeout: 600  # 10 min for model download

health:
  method: health
  interval: 60

rpc:
  methods:
    setup:
      description: Download and initialize the model
    generate:
      description: Generate an image from a prompt
      input:
        type: object
        properties:
          prompt: { type: string }
        required: [prompt]
    health:
      description: Health check
```

## File Handling

### Outputs
Methods returning file paths are wrapped as FileRef objects:

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
1. **Server path**: `jb-serve call whisper.transcribe audio=/path/on/server.wav`
2. **Multipart upload**: `curl -F "audio=@local.wav" http://localhost:9800/v1/tools/whisper/transcribe`

## Running as a Service

### systemd (Linux)

```ini
# /etc/systemd/system/jb-serve.service
[Unit]
Description=jb-serve Tool Server
After=network.target

[Service]
Type=simple
User=youruser
ExecStart=/usr/local/bin/jb-serve serve --port 9800
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable jb-serve
sudo systemctl start jb-serve
```

## Example Tools

| Tool | Description | Mode |
|------|-------------|------|
| [jb-whisper](https://github.com/calobozan/jb-whisper) | Audio transcription (OpenAI Whisper) | persistent |
| [jb-z-image-turbo](https://github.com/calobozan/jb-z-image-turbo) | Fast image generation (6B params, 8-step) | persistent |
| [jb-deepseek-ocr](https://github.com/calobozan/jb-deepseek-ocr) | Document OCR to Markdown | persistent |

## License

MIT
