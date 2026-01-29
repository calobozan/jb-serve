# jb-serve

A tool server that manages and serves Python tools using [jumpboot](https://github.com/richinsley/jumpboot) environments.

## Overview

jb-serve lets you install Python tools as git repos with manifests, then call them via CLI or HTTP API. Each tool gets its own isolated Python environment managed by jumpboot.

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

# List tools
jb-serve list

# Call a method
jb-serve call calculator.add --a 2 --b 3
# {"result": 5}

# Start HTTP API
jb-serve serve --port 9800
```

## Creating Tools

A tool is a git repo with:
- `jumpboot.yaml` - manifest describing the tool
- `main.py` - Python entrypoint with callable functions

### Example Manifest

```yaml
name: my-tool
version: 1.0.0
description: What this tool does

capabilities:
  - capability one
  - capability two

runtime:
  python: "3.11"
  mode: oneshot
  packages:
    - numpy
    - pandas

rpc:
  methods:
    my_method:
      description: "What it does"
      input:
        type: object
        properties:
          param1:
            type: string
        required: [param1]
      output:
        type: object
        properties:
          result:
            type: string
```

### Example Python

```python
import json

def my_method(param1: str) -> str:
    result = {"result": f"processed {param1}"}
    return json.dumps(result)
```

Functions are called via jumpboot's REPL and should return JSON strings.

## HTTP API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/v1/tools` | GET | List all tools |
| `/v1/tools/{name}` | GET | Tool info |
| `/v1/tools/{name}/schema` | GET | RPC schema |
| `/v1/tools/{name}/{method}` | POST | Call method |

## How It Works

1. **Install**: Clone tool repo, read manifest
2. **Environment**: jumpboot creates isolated Python env with specified version and packages
3. **Execute**: jumpboot's REPL imports tool module and calls functions directly
4. **Result**: JSON response returned to caller

Uses [github.com/richinsley/jumpboot](https://github.com/richinsley/jumpboot) for all Python environment management.

## License

MIT
