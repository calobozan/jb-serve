# jb-service: Python SDK for jb-serve

## Vision

Tool authors write a Python class. That's it. No knowledge of Go, REPL protocols, or jb-serve internals required.

```python
from jb_service import Service, method

class Calculator(Service):
    """A simple calculator service."""
    
    @method
    def add(self, a: float, b: float) -> float:
        """Add two numbers."""
        return a + b
    
    @method
    def multiply(self, a: float, b: float) -> float:
        """Multiply two numbers."""
        return a * b
```

That's a complete, working tool.

## Key Decisions

- **Package name:** `jb-service` (matches Go service)
- **Validation:** Pydantic for input validation + schema generation
- **Async:** First-class support for `async def` methods
- **Logging:** `self.log` exposed, routes to jb-serve
- **Streaming:** Deferred, but protocol designed to support it later

---

## Core API

### Service Base Class

```python
from jb_service import Service, method

class MyService(Service):
    """Service docstring becomes the description."""
    
    # Optional metadata (can also come from jumpboot.yaml)
    name = "my-service"          # default: class name lowercase
    version = "1.0.0"            # default: "0.0.0"
    
    def setup(self):
        """Called once when service starts. Load models, warm caches, etc."""
        pass
    
    def teardown(self):
        """Called when service stops. Cleanup resources."""
        pass
    
    @method
    def my_method(self, arg1: str, arg2: int = 10) -> dict:
        """Method docstring becomes the description.
        
        Args:
            arg1: Description of arg1
            arg2: Description of arg2
            
        Returns:
            Description of return value
        """
        return {"result": arg1 * arg2}
```

### The `@method` Decorator

Marks a method as an RPC endpoint. Extracts schema from:
- Type hints → JSON schema types
- Docstring → descriptions
- Default values → optional vs required

```python
@method
def transcribe(
    self,
    audio: str,                    # required string
    language: str = "en",          # optional, default "en"  
    timestamps: bool = False,      # optional, default False
) -> dict:
    """Transcribe audio file to text.
    
    Args:
        audio: Path to audio file
        language: Language code (e.g., 'en', 'es', 'fr')
        timestamps: Include word-level timestamps
        
    Returns:
        Transcription result with text and optional timestamps
    """
    ...
```

Generates schema:
```json
{
  "name": "transcribe",
  "description": "Transcribe audio file to text.",
  "input": {
    "type": "object",
    "properties": {
      "audio": {"type": "string", "description": "Path to audio file"},
      "language": {"type": "string", "description": "Language code", "default": "en"},
      "timestamps": {"type": "boolean", "description": "Include word-level timestamps", "default": false}
    },
    "required": ["audio"]
  },
  "output": {
    "type": "object"
  }
}
```

### Type Mapping (via Pydantic)

We use Pydantic for validation and schema generation. Type hints are converted automatically:

| Python Type | JSON Schema |
|-------------|-------------|
| `str` | `{"type": "string"}` |
| `int` | `{"type": "integer"}` |
| `float` | `{"type": "number"}` |
| `bool` | `{"type": "boolean"}` |
| `list` | `{"type": "array"}` |
| `list[str]` | `{"type": "array", "items": {"type": "string"}}` |
| `dict` | `{"type": "object"}` |
| `None` | `{"type": "null"}` |
| `str \| None` | `{"type": ["string", "null"]}` |
| `Literal["a", "b"]` | `{"type": "string", "enum": ["a", "b"]}` |
| `pydantic.BaseModel` | Full object schema from model |

**Pydantic models for complex inputs:**

```python
from pydantic import BaseModel, Field
from jb_service import Service, method

class GenerateRequest(BaseModel):
    prompt: str = Field(description="Text prompt for generation")
    width: int = Field(default=1024, ge=256, le=2048)
    height: int = Field(default=1024, ge=256, le=2048)
    seed: int | None = Field(default=None, description="Random seed")

class ImageGen(Service):
    @method
    def generate(self, request: GenerateRequest) -> dict:
        # request is already validated by Pydantic
        ...
```

Or inline with Annotated:

```python
from typing import Annotated
from pydantic import Field

@method
def generate(
    self,
    prompt: Annotated[str, Field(min_length=1, max_length=1000)],
    width: Annotated[int, Field(ge=256, le=2048)] = 1024,
) -> dict:
    ...
```

Pydantic handles:
- Type coercion (string "123" → int 123)
- Validation (min/max, patterns, etc.)
- Schema generation (with descriptions, constraints)
- Clear error messages on invalid input

### Lifecycle

```
jb-serve calls tool
        │
        ▼
┌─────────────────────┐
│  Service.__init__() │
└─────────────────────┘
        │
        ▼
┌─────────────────────┐
│  Service.setup()    │  ← Load models, init resources (once)
└─────────────────────┘
        │
        ▼
┌─────────────────────┐
│  [RPC Loop]         │  ← Handle method calls
│  method() → result  │
│  method() → result  │
│  ...                │
└─────────────────────┘
        │
        ▼
┌─────────────────────┐
│  Service.teardown() │  ← Cleanup (on shutdown)
└─────────────────────┘
```

---

## Entry Point

Every tool needs a `main.py` that runs the service:

```python
# main.py
from jb_service import run
from my_service import MyService

if __name__ == "__main__":
    run(MyService)
```

Or even simpler, if service is in main.py:

```python
# main.py
from jb_service import Service, method, run

class Calculator(Service):
    @method
    def add(self, a: float, b: float) -> float:
        return a + b

if __name__ == "__main__":
    run(Calculator)
```

The `run()` function:
1. Instantiates the service
2. Calls `setup()`
3. Enters the REPL loop (communicates with jb-serve via jumpboot protocol)
4. Dispatches RPC calls to methods
5. Calls `teardown()` on exit

---

## Communication Protocol

The service author never sees this. It's handled by the `Service` base class.

**jb-serve → Python (via jumpboot REPL):**
```python
__jb_call__("method_name", {"arg1": "value", "arg2": 123})
```

**Python → jb-serve (via stdout):**
```json
{"ok": true, "result": {"output": "value"}, "done": true}
```

Or on error:
```json
{"ok": false, "error": {"type": "ValueError", "message": "invalid input"}, "done": true}
```

**The `done` flag:** Always present. Currently always `true`. This allows future streaming support without protocol changes:

```json
{"ok": true, "chunk": "partial result", "done": false}
{"ok": true, "chunk": "more data", "done": false}
{"ok": true, "result": "final", "done": true}
```

Go side reads responses until `done: true`. For now, that's always the first response.

**Internal implementation:**

```python
class Service:
    def __init__(self):
        self._methods = {}
        for name in dir(self):
            attr = getattr(self, name)
            if hasattr(attr, '_jb_method'):
                self._methods[name] = attr
    
    def _handle_call(self, method_name: str, params: dict) -> Any:
        """Called by the REPL loop."""
        if method_name not in self._methods:
            raise AttributeError(f"Unknown method: {method_name}")
        
        method = self._methods[method_name]
        return method(**params)
    
    def _get_schema(self) -> dict:
        """Return full service schema for discovery."""
        ...
```

The `run()` function registers a global `__jb_call__` that jumpboot can invoke:

```python
def run(service_class):
    service = service_class()
    service.setup()
    
    # Register global for jumpboot to call
    def __jb_call__(method: str, params: dict):
        try:
            result = service._handle_call(method, params)
            return {"ok": True, "result": result}
        except Exception as e:
            return {"ok": False, "error": {"type": type(e).__name__, "message": str(e)}}
    
    # Make available to REPL
    import builtins
    builtins.__jb_call__ = __jb_call__
    builtins.__jb_schema__ = service._get_schema
    
    # Signal ready
    print("__JB_READY__", flush=True)
    
    # Keep alive (jumpboot manages the REPL)
    import signal
    signal.pause()
```

---

## Async Support

Methods can be sync or async. The service handles both:

```python
from jb_service import Service, method
import httpx

class WebFetcher(Service):
    def setup(self):
        self.client = httpx.AsyncClient()
    
    async def teardown(self):
        await self.client.aclose()
    
    @method
    async def fetch(self, url: str) -> dict:
        """Fetch a URL and return response info."""
        response = await self.client.get(url)
        return {
            "status": response.status_code,
            "length": len(response.content),
        }
    
    @method
    def sync_method(self, x: int) -> int:
        """Sync methods work too."""
        return x * 2
```

The `run()` function detects async methods and runs the appropriate event loop.

**Why async matters:**
- I/O-bound operations (HTTP, file, database) don't block
- Multiple concurrent calls can be handled efficiently  
- Natural fit for tools that call external APIs

---

## Logging

Services have a built-in logger that routes to jb-serve:

```python
from jb_service import Service, method

class MyService(Service):
    def setup(self):
        self.log.info("Loading model...")
        self.model = load_model()
        self.log.info("Model loaded", extra={"params": 1000000})
    
    @method
    def process(self, data: str) -> dict:
        self.log.debug(f"Processing {len(data)} bytes")
        try:
            result = self.model.run(data)
            return {"result": result}
        except Exception as e:
            self.log.error(f"Processing failed: {e}")
            raise
```

Log levels: `debug`, `info`, `warning`, `error`, `critical`

Logs are sent to jb-serve via a side channel (separate from method results) so they appear in server logs and can be forwarded to the caller if requested.

**Log protocol (internal):**
```json
{"log": {"level": "info", "message": "Loading model...", "extra": {}}}
```

---

## Manifest Generation

The manifest (`jumpboot.yaml`) can be:
1. **Hand-written** — full control
2. **Auto-generated** — from the Python class

```bash
# Generate manifest from service class
jb-service manifest my_service.py > jumpboot.yaml

# Or during install, jb-serve can introspect
jb-serve install ./my-tool  # reads main.py, generates schema
```

Minimal hand-written manifest (runtime only):

```yaml
runtime:
  python: "3.10"
  packages:
    - torch
    - transformers
```

jb-serve fills in the rest from the Python class at install time.

---

## Package Structure

```
jb-service/
├── pyproject.toml
├── README.md
├── src/
│   └── jb_service/
│       ├── __init__.py      # Public API: Service, method, run
│       ├── service.py       # Service base class + logging
│       ├── method.py        # @method decorator
│       ├── schema.py        # Pydantic → JSON schema
│       ├── protocol.py      # Jumpboot communication (async)
│       └── cli.py           # jb-service CLI
└── tests/
    └── ...
```

**Repo:** `github.com/calobozan/jb-service`

**Public API:**
```python
from jb_service import Service, method, run

# That's it. Three imports.
```

---

## Examples

### Minimal (Calculator)

```python
from jb_service import Service, method, run

class Calculator(Service):
    @method
    def add(self, a: float, b: float) -> float:
        return a + b
    
    @method
    def eval(self, expression: str) -> float:
        """Evaluate a math expression."""
        return float(eval(expression))  # yes, unsafe, it's an example

if __name__ == "__main__":
    run(Calculator)
```

### Stateful (Counter)

```python
from jb_service import Service, method, run

class Counter(Service):
    def setup(self):
        self.value = 0
    
    @method
    def increment(self, by: int = 1) -> int:
        self.value += by
        return self.value
    
    @method
    def get(self) -> int:
        return self.value
    
    @method
    def reset(self) -> int:
        self.value = 0
        return self.value

if __name__ == "__main__":
    run(Counter)
```

### ML Model (Whisper)

```python
from jb_service import Service, method, run
from typing import Literal

class Whisper(Service):
    """Audio transcription using OpenAI Whisper."""
    
    version = "1.0.0"
    
    def setup(self):
        import whisper
        self.models = {}
        # Lazy load models on first use
    
    def _get_model(self, size: str):
        if size not in self.models:
            import whisper
            self.models[size] = whisper.load_model(size)
        return self.models[size]
    
    @method
    def transcribe(
        self,
        audio: str,
        model: Literal["tiny", "base", "small", "medium", "large"] = "base",
        language: str | None = None,
    ) -> dict:
        """Transcribe audio file to text.
        
        Args:
            audio: Path to audio file
            model: Whisper model size
            language: Language code (auto-detect if not specified)
        """
        m = self._get_model(model)
        result = m.transcribe(audio, language=language)
        return {
            "text": result["text"],
            "language": result["language"],
            "segments": [
                {"start": s["start"], "end": s["end"], "text": s["text"]}
                for s in result["segments"]
            ]
        }
    
    @method  
    def available_models(self) -> list[str]:
        """List available model sizes."""
        return ["tiny", "base", "small", "medium", "large"]

if __name__ == "__main__":
    run(Whisper)
```

---

## Open Questions

1. ~~**Package name**~~: `jb-service` ✓

2. ~~**Async support**~~: Yes, first-class ✓

3. **Streaming**: Deferred. Protocol supports it (`done` flag). Implementation later with `@method(stream=True)` + `yield`.

4. ~~**Validation**~~: Pydantic ✓

5. ~~**Logging**~~: Yes, `self.log` ✓

6. **Nested Pydantic models**: How deep do we go with schema generation? Probably just let Pydantic handle it.

7. **Timeouts**: Should methods have configurable timeouts? Or leave to jb-serve?

8. **Context passing**: Should methods receive a context object with request ID, cancellation, etc.?

---

## Implementation Plan

### Phase 1: Core
- [ ] `Service` base class with `setup()`/`teardown()`
- [ ] `@method` decorator  
- [ ] `run()` entry point with asyncio support
- [ ] Pydantic integration for validation
- [ ] Jumpboot REPL protocol integration (with `done` flag)
- [ ] `self.log` logger

### Phase 2: Schema & Tooling
- [ ] Schema generation from Pydantic + type hints
- [ ] Docstring parsing for descriptions
- [ ] `jb-service manifest` CLI command
- [ ] `jb-service test` local runner (call methods without jb-serve)

### Phase 3: Polish
- [ ] Error handling with proper tracebacks
- [ ] `jb-service init` scaffolding
- [ ] Package publishing (PyPI)

### Phase 4: Advanced (Later)
- [ ] Streaming support (`@method(stream=True)` + `yield`)
- [ ] File type handling
- [ ] Context/cancellation support

## Dependencies

```toml
[project]
dependencies = [
    "pydantic>=2.0",
]
```
