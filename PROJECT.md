# jb-serve Project Plan

## Vision

A generic server that hosts multiple Python tools, each with its own isolated environment. Tools are git repos with manifests that describe their capabilities, dependencies, and RPC interface. An agent (AI or human) can discover available tools and call them via CLI or HTTP API.

---

## Development Workflow

**Target: GPU Server** — jb-serve runs on `192.168.0.107` (gpu-server) as a systemd service.

### Production Server
- **URL**: `http://192.168.0.107:9800`
- **Service**: `jb-serve.service` (systemd, auto-starts)
- **Binary**: `~/bin/jb-serve` on gpu-server
- **Tools dir**: `~/.jb-serve/tools/` on gpu-server

### Deploy jb-serve Changes
```bash
# Build for Linux (GPU server is x86_64 Linux)
cd ~/projects/jb-serve
GOOS=linux GOARCH=amd64 go build -o jb-serve-linux ./cmd/jb-serve

# Deploy to GPU server
scp jb-serve-linux calo@192.168.0.107:/home/calo/bin/jb-serve

# Restart service
ssh calo@192.168.0.107 "sudo systemctl restart jb-serve"
# Or if sudo hangs: ssh calo@192.168.0.107 "pkill jb-serve; nohup ~/bin/jb-serve serve --port 9800 &"
```

### Deploy a New Tool
```bash
# Copy tool to GPU server
scp -r ~/projects/jb-whisper calo@192.168.0.107:~/projects/

# Install on server
ssh calo@192.168.0.107 "~/bin/jb-serve install ~/projects/jb-whisper"
```

### Test via HTTP (not local CLI)
```bash
# List tools
curl http://192.168.0.107:9800/v1/tools

# Call with file path (file must exist on server)
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/transcribe \
  -H "Content-Type: application/json" \
  -d '{"audio": "/path/on/gpu-server/file.wav"}'

# Call with file upload (multipart) - file sent from client
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/transcribe \
  -F "audio=@/local/path/to/file.wav" \
  -F 'params={"language": "en"}'

# Start/stop persistent tools
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/start
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/stop

# Get generated files
curl -o output.png http://192.168.0.107:9800/v1/files/{ref}.png
```

### Check Server Status
```bash
ssh calo@192.168.0.107 "systemctl status jb-serve"
ssh calo@192.168.0.107 "journalctl -u jb-serve -f"  # logs
```

### System Dependencies
Some tools need system packages on the GPU server:
```bash
# whisper needs ffmpeg
ssh calo@192.168.0.107 "sudo apt-get install -y ffmpeg"
```

---

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

### Phase 3 ✅ — MessagePack Transport
- **Two transport modes**: REPL (default) and MessagePack
- `runtime.transport: msgpack` in manifest enables MessagePack
- `MessagePackService` base class in jb-service for stdout-safe tools
- Jumpboot's `QueueProcess` for binary-safe RPC (no stdout interference)
- Perfect for tools with progress bars, tqdm, or verbose libraries

---

## Transport Modes

### REPL Transport (default)
- Uses jumpboot's REPL for Python execution
- Simple, works for most tools
- **Caveat**: Stdout must be clean (no progress bars, prints)

```yaml
runtime:
  python: "3.11"
  mode: persistent
  # transport defaults to "repl"
```

### MessagePack Transport
- Uses jumpboot's `QueueProcess` with MessagePack serialization
- Stdout/stderr are completely separate from RPC
- Progress bars, tqdm, logging all work fine

```yaml
runtime:
  python: "3.11"
  mode: persistent
  transport: msgpack  # Enable MessagePack
```

**Python side:**
```python
from jb_service import MessagePackService, method, run

class MyTool(MessagePackService):  # Note: MessagePackService, not Service
    @method
    def generate(self, prompt: str) -> dict:
        # tqdm progress bars work fine here!
        ...

if __name__ == "__main__":
    run(MyTool)
```

---

## Repositories

| Repo | Description |
|------|-------------|
| `github.com/calobozan/jb-serve` | Go server/CLI |
| `github.com/calobozan/jb-service` | Python SDK for tool authors |
| `github.com/calobozan/jb-calculator` | Reference oneshot tool (old style) |
| `~/projects/jb-calculator-new` | Reference tool using jb-service |
| `~/projects/jb-whisper` | Audio transcription tool (Whisper) |
| `~/projects/jb-z-image-turbo` | Image generation (Z-Image-Turbo, uses MessagePack) |

---

## Creating a Tool (with jb-service)

### Basic Tool (REPL transport)

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

### Tool with Progress Bars (MessagePack transport)

**main.py:**
```python
from jb_service import MessagePackService, method, run, save_image

class ImageGenerator(MessagePackService):
    name = "image-gen"
    version = "1.0.0"
    
    def setup(self):
        from diffusers import SomePipeline
        self.pipe = SomePipeline.from_pretrained(...)  # Progress bars OK!
    
    @method
    def generate(self, prompt: str) -> dict:
        result = self.pipe(prompt)  # tqdm progress bars OK!
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
  transport: msgpack  # Enable MessagePack for stdout-safe operation
  packages:
    - diffusers
    - torch
    - git+https://github.com/calobozan/jb-service.git
```

### File Input Types

Use type hints to control how file inputs are delivered to your method:

```python
from jb_service import Service, method, run, FilePath, Audio, Image

class MediaProcessor(Service):
    name = "media-processor"
    
    @method
    def transcribe(self, audio: FilePath) -> dict:
        # audio = "/path/to/file.wav" (string path)
        ...
    
    @method
    def analyze_audio(self, audio: Audio) -> dict:
        # audio = (sample_rate, numpy_array)
        sample_rate, data = audio
        ...
    
    @method
    def describe_image(self, image: Image) -> dict:
        # image = PIL.Image
        ...
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
│      ├── REPL transport (default)                       │
│      │   └── initializeService() + doCall()             │
│      └── MessagePack transport                          │
│          └── QueueProcess with register_handler()       │
└─────────────────────────────────────────────────────────┘
                          │
         ┌────────────────┴────────────────┐
         ▼                                 ▼
┌─────────────────────┐         ┌─────────────────────┐
│  jumpboot REPL      │         │  jumpboot Queue     │
│  (stdout = data)    │         │  (msgpack pipes)    │
└─────────────────────┘         └─────────────────────┘
         │                                 │
         ▼                                 ▼
┌─────────────────────────────────────────────────────────┐
│  jb-service (Python)                                    │
│  ├── Service — REPL transport                          │
│  ├── MessagePackService — MessagePack transport        │
│  ├── @method decorator                                 │
│  └── run() — auto-detects transport from service class │
└─────────────────────────────────────────────────────────┘
```

---

## File Handling

### Output Files (FileRef)
When tools return file paths, jb-serve wraps them as FileRef objects:

```json
{
  "image": {
    "ref": "43af6f50",
    "url": "/v1/files/43af6f50.png",
    "path": "/home/calo/.jb-serve/outputs/43af6f50.png",
    "size": 1211477,
    "media_type": "image/png"
  }
}
```

Fetch files via:
```bash
curl -o image.png http://192.168.0.107:9800/v1/files/43af6f50.png
```

### Input Files
Two options:
1. **JSON with path** — file on server: `{"audio": "/path/on/server.wav"}`
2. **Multipart upload** — file from client: `-F "audio=@local.wav"`

---

## Key Files

```
~/projects/jb-serve/
├── cmd/jb-serve/main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── manifest.go          # Transport config
│   ├── tools/
│   │   ├── manager.go
│   │   └── executor.go          # REPL + MessagePack transports
│   ├── files/
│   │   └── files.go             # FileRef handling
│   └── server/
│       └── server.go            # /v1/files endpoint
├── docs/
│   ├── PYTHON-SDK.md
│   └── BINARY-HANDLING.md
└── PROJECT.md

~/projects/jb-service/
├── src/jb_service/
│   ├── __init__.py
│   ├── service.py               # Service base class (REPL)
│   ├── msgpack_service.py       # MessagePackService base class
│   ├── msgpack_protocol.py      # MessagePack transport protocol
│   ├── method.py                # @method decorator
│   ├── protocol.py              # run(), auto-transport detection
│   └── types.py                 # FilePath, Audio, Image, save_image()
└── tests/
```

---

## Usage Examples

### Image Generation (z-image-turbo)
```bash
# Start the tool
curl -X POST http://192.168.0.107:9800/v1/tools/z-image-turbo/start

# Generate an image
curl -X POST http://192.168.0.107:9800/v1/tools/z-image-turbo/generate \
  -H "Content-Type: application/json" \
  -d '{"prompt": "A peaceful meadow at sunset", "seed": 456}'

# Response includes FileRef:
# {"image": {"ref": "52828c84", "url": "/v1/files/52828c84.png", ...}}

# Download the image
curl -o meadow.png http://192.168.0.107:9800/v1/files/52828c84.png
```

### Audio Transcription (whisper)
```bash
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/start
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/transcribe \
  -H "Content-Type: application/json" \
  -d '{"audio": "/path/to/audio.wav"}'
```

---

## Next Up

### Completed
- [x] **Phase 1**: Core infrastructure
- [x] **Phase 2**: Python SDK (jb-service)
- [x] **Phase 3**: MessagePack transport for stdout-safe tools
- [x] **z-image-turbo**: Image generation with progress bars working
- [x] **FileRef system**: Output files served via `/v1/files/{ref}`

### Remaining Candidates
- [ ] Convert jb-whisper to MessagePack (if needed)
- [ ] CLI daemon mode (`jb-serve start` persists)
- [ ] Auto-restart on health failure
- [ ] Tool hot-reload without restart
