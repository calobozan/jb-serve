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

---

## Repositories

| Repo | Description |
|------|-------------|
| `github.com/calobozan/jb-serve` | Go server/CLI |
| `github.com/calobozan/jb-service` | Python SDK for tool authors |
| `github.com/calobozan/jb-calculator` | Reference oneshot tool (old style) |
| `~/projects/jb-calculator-new` | Reference tool using jb-service |
| `~/projects/jb-whisper` | Audio transcription tool (Whisper) |
| `~/projects/jb-z-image-turbo` | Image generation (Z-Image-Turbo) |

---

## Creating a Tool (with jb-service)

**main.py:**
```python
from jb_service import Service, method, run, FilePath, Audio, Image

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

### File Input Types

Use type hints to control how file inputs are delivered to your method:

```python
from jb_service import Service, method, run, FilePath, Audio, Image

class MediaProcessor(Service):
    name = "media-processor"
    
    @method
    def transcribe(self, audio: FilePath) -> dict:
        # audio = "/path/to/file.wav" (string path)
        # Use when your library handles file loading (like whisper)
        ...
    
    @method
    def analyze_audio(self, audio: Audio) -> dict:
        # audio = (sample_rate, numpy_array)
        # Pre-loaded by jb-service using soundfile/scipy/librosa
        sample_rate, data = audio
        ...
    
    @method
    def describe_image(self, image: Image) -> dict:
        # image = PIL.Image
        # Pre-loaded by jb-service using Pillow
        width, height = image.size
        ...
```

**Available types:**
- `FilePath` — Pass path as string (no loading)
- `Audio` — Load as `(sample_rate, numpy_array)` tuple
- `Image` — Load as `PIL.Image`

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
- [x] **jb-whisper** — Audio transcription tool working (deployed on GPU server)
  - Uses file paths directly (no complex FileRef abstraction needed)
  - Persistent mode with model kept loaded
  - Works via HTTP API: `POST /v1/tools/whisper/transcribe {"audio": "/path/to/file.wav"}`
  - Requires `ffmpeg` on the server: `sudo apt-get install ffmpeg`
- [x] **Multipart file upload** — Upload files directly to tools from remote clients
  - `curl -F "audio=@local-file.wav" -F 'params={...}'`
  - Files saved to temp, cleaned up after call
  - Works alongside JSON paths for server-local files
- [x] **File type hints (jb-service)** — Declarative file handling in Python
  - `FilePath` — pass path string (tool handles loading)
  - `Audio` — pre-load as (sample_rate, numpy_array)
  - `Image` — pre-load as PIL.Image

### Binary Handling (Implemented)
Simplified from the original `docs/BINARY-HANDLING.md` design:

**Input options:**
1. **JSON with path** — file already on server: `{"audio": "/path/on/server.wav"}`
2. **Multipart upload** — file sent from client: `-F "audio=@local.wav"`

**How it works:**
- Server detects `multipart/form-data` content type
- Saves uploaded files to `~/.jb-serve/uploads/`
- Passes file path to tool (tool just sees a path)
- Cleans up temp files after method returns

**Not implemented (not needed yet):**
- URL fetching (`{"audio": {"url": "https://..."}`)
- Base64 inline (`{"audio": {"data": "base64..."}}`)
- Managed output refs (`/v1/files/{ref}` endpoint)

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

## Usage (GPU Server)

See **Development Workflow** above for deployment steps.

```bash
# List tools on server
curl http://192.168.0.107:9800/v1/tools

# Start persistent tool
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/start

# Call methods
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/transcribe \
  -H "Content-Type: application/json" \
  -d '{"audio": "/path/on/server/audio.wav"}'

# Stop tool
curl -X POST http://192.168.0.107:9800/v1/tools/whisper/stop
```

**Note:** File paths in requests must be paths on the GPU server, not local paths.
