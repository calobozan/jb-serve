# jb-serve Project Plan

## Vision

A generic server that hosts multiple Python tools, each with its own isolated environment. Tools are git repos with manifests that describe their capabilities, dependencies, and RPC interface. An agent (AI or human) can discover available tools and call them via CLI or HTTP API.

---

## Build Requirements

**CGO is required.** jb-serve uses:
- `github.com/mattn/go-sqlite3` for file store metadata (requires CGO)
- Future: semaphores and shared memory for IPC

This means **cross-compilation doesn't work** — you must build on the target platform or use a CGO-enabled cross-compiler.

### Local Development (macOS)
```bash
cd ~/projects/jb-serve
go build -o jb-serve ./cmd/jb-serve
```

### Production Build (Linux)
Build directly on the target server:
```bash
# Sync source to server
rsync -av --exclude '.git' ~/projects/jb-serve/ calo@192.168.0.107:~/projects/jb-serve/

# Build on server
ssh calo@192.168.0.107 "cd ~/projects/jb-serve && go build -o ~/bin/jb-serve ./cmd/jb-serve"
```

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
# Sync source and build on server (CGO required - can't cross-compile)
rsync -av --exclude '.git' ~/projects/jb-serve/ calo@192.168.0.107:~/projects/jb-serve/
ssh calo@192.168.0.107 "cd ~/projects/jb-serve && go build -o ~/bin/jb-serve ./cmd/jb-serve"

# Restart service
ssh calo@192.168.0.107 "pkill jb-serve; sleep 1; nohup ~/bin/jb-serve serve --port 9800 > /tmp/jb-serve.log 2>&1 &"
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

### Single Server Mode
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

### Broker Mode (Distributed)
```
                         Clients
                            │
                            ▼
                ┌───────────────────────┐
                │  jb-serve broker      │
                │  :9800                │
                │  - aggregates tools   │
                │  - routes requests    │
                └───────────┬───────────┘
                            │
         ┌──────────────────┼──────────────────┐
         ▼                  ▼                  ▼
  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
  │ gpu-server-1│    │ gpu-server-2│    │ cpu-server  │
  │ :9801       │    │ :9801       │    │ :9801       │
  │ - whisper   │    │ - z-image   │    │ - embed     │
  │ - ocr       │    │ - flux      │    │ - calculator│
  └─────────────┘    └─────────────┘    └─────────────┘
```

**Broker mode** allows distributing tools across multiple servers:
- Single entry point for clients (the broker)
- Child servers register and send heartbeats
- Broker routes requests based on tool availability
- Automatic failover when children become unhealthy

---

## Broker Mode

### Starting a Broker
```bash
jb-serve broker --port 9800
```

The broker doesn't run tools itself — it only aggregates and routes.

### Registering Children
Child servers register with the broker on startup:
```bash
# On GPU server 1
jb-serve serve --port 9801 \
  --broker http://broker-host:9800 \
  --self-url http://gpu1:9801 \
  --name "gpu-server-1"

# On GPU server 2  
jb-serve serve --port 9801 \
  --broker http://broker-host:9800 \
  --self-url http://gpu2:9801 \
  --name "gpu-server-2"
```

### Broker Endpoints
```bash
# List connected children
curl http://broker:9800/v1/broker/children

# Aggregated tool list (from all healthy children)
curl http://broker:9800/v1/tools

# Tool requests are proxied to appropriate child
curl -X POST http://broker:9800/v1/tools/z-image-turbo/generate \
  -d '{"prompt": "..."}'
# → Routed to whichever child has z-image-turbo

# Health includes child status
curl http://broker:9800/health
# {"status":"ok","mode":"broker","children_total":2,"children_healthy":2}
```

### Child Registration Protocol
1. Child POSTs to `/v1/broker/register` with ID, URL, name, and tool list
2. Broker returns heartbeat interval
3. Child sends heartbeats to `/v1/broker/heartbeat` periodically
4. If heartbeats stop, broker marks child unhealthy then removes it

---

## File Store

The file store provides persistent, shared file storage for all tools. Files are stored as blobs with UUID names and metadata in SQLite.

### Features
- **UUID-based storage**: Files get a unique ID, original name preserved in metadata
- **TTL support**: Files can expire automatically (0 = permanent)
- **Garbage collection**: Expired files cleaned up every 5 minutes
- **SHA256 checksums**: Integrity verification
- **Cross-tool sharing**: Any tool can read files imported by another
- **Direct filesystem access**: Python tools read blobs directly (no HTTP overhead)

### Server Configuration

```bash
# Start with default file store (~/.jb-serve)
jb-serve serve --port 9800

# Custom file store location
jb-serve serve --port 9800 --store-path /data/jb-files

# Disable file store entirely
jb-serve serve --port 9800 --no-store
```

### Storage Layout

```
~/.jb-serve/
├── files.db          # SQLite metadata (name, size, sha256, timestamps)
├── blobs/            # Flat directory of UUID-named files
│   ├── 2737077c-5fb3-42cf-860c-2a5a141a2cae
│   ├── 5ffd33dc-1bc1-4d3b-897c-a5337ccce756
│   └── ...
└── ...
```

### Integrating File Store in Python Tools

Every `Service` and `MessagePackService` subclass has `self.files` automatically available — a `FileStore` client connected to jb-serve.

#### Basic Pattern: Import Generated Files

```python
import os
from jb_service import MessagePackService, method, run, save_image

class ImageGenerator(MessagePackService):
    name = "image-gen"
    
    @method
    def generate(self, prompt: str, ttl: int = 3600) -> dict:
        # 1. Generate output to temp file
        image = self.pipeline(prompt)
        temp_path = save_image(image, format="png")
        
        # 2. Import into file store
        file_id = self.files.import_file(
            temp_path,
            name=f"generated-{prompt[:20]}.png",
            ttl=ttl  # seconds until auto-delete (0 = permanent)
        )
        
        # 3. Clean up temp file (optional - the store has its own copy)
        os.remove(temp_path)
        
        # 4. Return file_id for retrieval
        return {"file_id": file_id}
```

#### Reading Files from Store

```python
@method
def process_stored_file(self, file_id: str) -> dict:
    # Get direct filesystem path (efficient, no HTTP)
    path = self.files.get_path(file_id)
    
    with open(path, 'rb') as f:
        data = f.read()
    
    return {"size": len(data)}
```

#### File Metadata

```python
@method
def get_file_info(self, file_id: str) -> dict:
    info = self.files.info(file_id)
    return {
        "id": info.id,
        "name": info.name,
        "size": info.size,
        "sha256": info.sha256,
        "path": info.path,
        "created_at": info.created_at,
        "expires_at": info.expires_at,  # 0 = permanent
    }
```

#### Listing and Managing Files

```python
@method
def list_my_files(self) -> dict:
    files = self.files.list()
    return {"files": [{"id": f.id, "name": f.name} for f in files]}

@method
def rename_file(self, file_id: str, new_name: str) -> dict:
    info = self.files.rename(file_id, new_name)
    return {"name": info.name}

@method
def extend_ttl(self, file_id: str, ttl: int) -> dict:
    info = self.files.set_ttl(file_id, ttl)
    return {"expires_at": info.expires_at}

@method
def delete_file(self, file_id: str) -> dict:
    self.files.delete(file_id)
    return {"deleted": file_id}
```

#### Cross-Tool File Sharing

Tool A generates a file:
```python
# In tool A
file_id = self.files.import_file("/tmp/output.wav", name="audio.wav", ttl=7200)
return {"file_id": file_id}
```

Tool B processes it:
```python
# In tool B
@method
def process(self, file_id: str) -> dict:
    path = self.files.get_path(file_id)  # Direct access to same blob
    # ... process file ...
```

### HTTP API

```bash
# List files
curl http://localhost:9800/v1/store
# Response: {"files": [{"id": "...", "name": "...", "size": 123, ...}]}

# Import file (server-side path)
curl -X POST http://localhost:9800/v1/store \
  -H "Content-Type: application/json" \
  -d '{"path": "/path/to/file.png", "name": "myfile.png", "ttl": 3600}'

# Import file (multipart upload)
curl -X POST http://localhost:9800/v1/store \
  -F "file=@local.png" -F "name=uploaded.png" -F "ttl=3600"

# Get file info
curl http://localhost:9800/v1/store/{id}

# Download file content
curl http://localhost:9800/v1/store/{id}/content -o file.png

# Rename or update TTL
curl -X PATCH http://localhost:9800/v1/store/{id} \
  -H "Content-Type: application/json" \
  -d '{"name": "newname.png", "ttl": 7200}'

# Delete file
curl -X DELETE http://localhost:9800/v1/store/{id}
```

### CLI

```bash
# List files
jb-serve files ls

# Import a file
jb-serve files import /path/to/file.png --name "myfile.png" --ttl 3600

# Get info
jb-serve files info <uuid>

# Delete
jb-serve files rm <uuid>
```

### TTL and Garbage Collection

- **TTL = 0**: File is permanent (never auto-deleted)
- **TTL > 0**: File expires after TTL seconds from import time
- **GC runs every 5 minutes**: Scans for expired files and removes them
- **Clients can extend TTL**: Use `set_ttl()` or `PATCH` to update expiration

---

## File Handling (Legacy)

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
├── cmd/jb-serve/main.go         # CLI with files subcommand
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── manifest.go          # Transport config
│   ├── tools/
│   │   ├── manager.go
│   │   └── executor.go          # REPL + MessagePack transports
│   ├── files/
│   │   └── files.go             # FileRef handling (legacy)
│   ├── filestore/
│   │   └── filestore.go         # Persistent file store with SQLite
│   ├── client/
│   │   └── client.go            # HTTP client (includes Files* methods)
│   └── server/
│       └── server.go            # /v1/store endpoints
├── docs/
│   ├── PYTHON-SDK.md
│   └── BINARY-HANDLING.md
└── PROJECT.md

~/projects/jb-service/
├── src/jb_service/
│   ├── __init__.py
│   ├── service.py               # Service base class (includes self.files)
│   ├── msgpack_service.py       # MessagePackService base class
│   ├── msgpack_protocol.py      # MessagePack transport protocol
│   ├── method.py                # @method decorator
│   ├── protocol.py              # run(), auto-transport detection
│   ├── filestore.py             # FileStore client for Python
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

### Phase 4 ✅ — File Store
- **Persistent file storage** with SQLite metadata
- UUID-based blob storage in flat directory
- TTL support with garbage collection
- SHA256 checksums for integrity
- HTTP API: `/v1/store` for import, list, info, delete
- Python `FileStore` client in jb-service base class
- CLI commands: `jb-serve files ls/import/info/rm`

### Phase 5 🚧 — Broker Mode (In Progress)
- **Broker server** aggregates tools from multiple children
- **Child registration** with heartbeat
- **Request proxying** to appropriate backend
- File store proxy (TODO)

### Remaining Candidates
- [ ] Convert jb-whisper to MessagePack (if needed)
- [ ] CLI daemon mode (`jb-serve start` persists)
- [ ] Auto-restart on health failure
- [ ] Tool hot-reload without restart
- [ ] Broker: shared/distributed file store
