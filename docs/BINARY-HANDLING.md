# Binary Handling Design

## Problem

Tools need to handle large binary data (audio, images, video) both as inputs and outputs. Current JSON-over-HTTP approach doesn't scale:

- Base64 adds 33% overhead
- Large payloads strain memory and parsing
- Response times suffer
- Doesn't match how these tools naturally work (file in → file out)

## Goals

1. Efficient binary I/O for local and remote clients
2. Minimal changes to existing JSON-RPC flow for non-binary methods
3. Clean manifest schema that declares binary fields
4. Automatic file lifecycle management

---

## Design

### File References

Introduce a `file` type that can be represented multiple ways:

```typescript
type FileRef = 
  | { path: string }           // Local filesystem path
  | { url: string }            // HTTP(S) URL to fetch
  | { data: string }           // Base64-encoded (small files only)
  | { ref: string }            // jb-serve managed file reference
```

Tools always receive a **local path** — jb-serve resolves the FileRef before invocation:
- `path` → validate exists, pass through
- `url` → download to temp, pass temp path
- `data` → decode to temp, pass temp path
- `ref` → resolve to managed file path

Tools always output a **local path** — jb-serve wraps it:
- Creates a managed reference
- Returns `{ ref, path, url?, size, media_type }`

### Manifest Schema

```yaml
name: whisper
version: 1.0.0
runtime:
  python: "3.10"
  packages: [openai-whisper, torch]

rpc:
  methods:
    transcribe:
      description: "Transcribe audio to text"
      input:
        type: object
        properties:
          audio:
            type: file                    # <-- new type
            media_types:                  # optional validation
              - audio/wav
              - audio/mp3
              - audio/m4a
          language:
            type: string
        required: [audio]
      output:
        type: object
        properties:
          text:
            type: string
          segments:
            type: array
```

For tools that output files:

```yaml
name: sdxl
rpc:
  methods:
    generate:
      input:
        type: object
        properties:
          prompt:
            type: string
          # ...
      output:
        type: object
        properties:
          image:
            type: file                    # <-- file output
            media_type: image/png
```

### Python Side

Tools receive resolved paths and return paths:

```python
# whisper tool
def transcribe(audio: str, language: str = "en") -> dict:
    """audio is always a local path, resolved by jb-serve"""
    model = whisper.load_model("base")
    result = model.transcribe(audio, language=language)
    return {"text": result["text"], "segments": result["segments"]}

# sdxl tool
def generate(prompt: str, width: int = 1024, height: int = 1024) -> dict:
    image = pipeline(prompt, width=width, height=height).images[0]
    
    # Write to temp, return path
    output_path = f"/tmp/jb-serve/outputs/{uuid4()}.png"
    image.save(output_path)
    return {"image": output_path}  # jb-serve wraps this
```

### HTTP API

**Input (multipart for files):**

```http
POST /v1/tools/whisper/transcribe
Content-Type: multipart/form-data

--boundary
Content-Disposition: form-data; name="audio"; filename="recording.wav"
Content-Type: audio/wav

<binary data>
--boundary
Content-Disposition: form-data; name="params"
Content-Type: application/json

{"language": "en"}
--boundary--
```

Or with file reference:

```http
POST /v1/tools/whisper/transcribe
Content-Type: application/json

{
  "audio": {"path": "/data/recordings/meeting.wav"},
  "language": "en"
}
```

**Output (JSON with file refs):**

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "result": {
    "image": {
      "ref": "f7a3b2c1",
      "path": "/home/user/.jb-serve/outputs/f7a3b2c1.png",
      "url": "/v1/files/f7a3b2c1.png",
      "size": 2048576,
      "media_type": "image/png"
    }
  }
}
```

**File retrieval:**

```http
GET /v1/files/f7a3b2c1.png

HTTP/1.1 200 OK
Content-Type: image/png
Content-Length: 2048576

<binary data>
```

### CLI

```bash
# Input from local file
jb-serve call whisper.transcribe audio=@/path/to/file.wav

# Input from URL  
jb-serve call whisper.transcribe audio=url:https://example.com/file.wav

# Input from stdin
cat file.wav | jb-serve call whisper.transcribe audio=@-

# Output handling
jb-serve call sdxl.generate prompt="a cat" --output-dir ./results/
# Writes: ./results/image.png
# Prints: {"image": {"path": "./results/image.png", ...}}
```

### File Management

**Directories:**
```
~/.jb-serve/
├── files/
│   ├── inputs/      # Downloaded/decoded inputs (temp)
│   └── outputs/     # Tool outputs (managed)
├── tools/           # Installed tools
└── envs/            # Python environments
```

**Lifecycle:**
- Input temps: Deleted after method returns
- Output files: Kept until:
  - Explicit delete: `DELETE /v1/files/{ref}`
  - TTL expiry (configurable, default 1 hour?)
  - Manual cleanup: `jb-serve files prune`

**Config:**
```yaml
# ~/.jb-serve/config.yaml
files:
  outputs_dir: ~/.jb-serve/files/outputs
  ttl: 3600              # seconds, 0 = never auto-delete
  max_size_mb: 1000      # total outputs dir limit
  serve_files: true      # enable /v1/files/ endpoint
```

---

## Implementation Plan

### Phase 1: Core File Support
- [ ] Add `type: file` to manifest schema parser
- [ ] FileRef type and resolver (path/url/data/ref)
- [ ] Input resolution before tool invocation
- [ ] Output wrapping after tool invocation
- [ ] File storage manager (outputs dir, refs)

### Phase 2: HTTP API
- [ ] Multipart form parsing for file uploads
- [ ] `/v1/files/{ref}` GET endpoint
- [ ] `/v1/files/{ref}` DELETE endpoint
- [ ] Content-Type detection

### Phase 3: CLI
- [ ] `@path` syntax for file inputs
- [ ] `url:` prefix for URL inputs
- [ ] `--output-dir` flag for saving outputs
- [ ] `jb-serve files list/prune/delete` commands

### Phase 4: Management
- [ ] TTL-based cleanup goroutine
- [ ] Max size enforcement
- [ ] Config file support

---

## Open Questions

1. **Streaming large outputs?** — Could support chunked transfer for very large files, but adds complexity. Start with whole-file approach?

2. **Input size limits?** — Should we enforce max upload size? Configurable?

3. **Authentication for /v1/files/?** — If we add auth later, files endpoint needs same auth. Token in URL param for direct downloads?

4. **Remote file caching?** — If same URL is passed repeatedly, cache it? Or always re-fetch?

5. **Symbolic refs vs UUIDs?** — `f7a3b2c1.png` vs `whisper-transcribe-2024-01-29-abc123.wav`? Readable names help debugging.

---

## Example: jb-whisper

With this design, jb-whisper manifest:

```yaml
name: whisper
version: 1.0.0
description: Audio transcription using OpenAI Whisper
capabilities:
  - transcribe audio to text
  - multiple languages
  - timestamp segments

runtime:
  python: "3.10"
  mode: persistent          # keep model loaded
  packages:
    - openai-whisper
    - torch
    - torchaudio

rpc:
  methods:
    transcribe:
      description: "Transcribe audio file to text"
      input:
        type: object
        properties:
          audio:
            type: file
            description: "Audio file to transcribe"
            media_types: [audio/wav, audio/mp3, audio/m4a, audio/ogg, audio/flac]
          model:
            type: string
            enum: [tiny, base, small, medium, large, large-v2, large-v3]
            default: base
          language:
            type: string
            description: "Language code (auto-detect if not specified)"
          task:
            type: string
            enum: [transcribe, translate]
            default: transcribe
        required: [audio]
      output:
        type: object
        properties:
          text:
            type: string
          language:
            type: string
          segments:
            type: array
            items:
              type: object
              properties:
                start: { type: number }
                end: { type: number }
                text: { type: string }
```

Usage:
```bash
# Local file
jb-serve call whisper.transcribe audio=@meeting.wav model=base

# Remote
curl -X POST http://gpu-server:9800/v1/tools/whisper/transcribe \
  -F "audio=@meeting.wav" \
  -F 'params={"model":"base"}'
```
