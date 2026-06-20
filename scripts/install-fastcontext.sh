#!/bin/bash
# install-fastcontext.sh — setup fastcontext skill for Claude Code
# Usage: bash install-fastcontext.sh <BASE_URL> <API_KEY> <MODEL>
# Example: bash install-fastcontext.sh https://9router.gass.web.id/v1 sk-xxx fastcontext
set -e

BASE_URL="${1:?Usage: $0 <BASE_URL> <API_KEY> <MODEL>}"
API_KEY="${2:?Usage: $0 <BASE_URL> <API_KEY> <MODEL>}"
MODEL="${3:?Usage: $0 <BASE_URL> <API_KEY> <MODEL>}"

REPO_DIR="$HOME/tools/fastcontext"
TOOL_DIR="$HOME/.local/share/uv/tools/fastcontext/lib/python3.12/site-packages/fastcontext/agent/tool"
FACTORY="$HOME/.local/share/uv/tools/fastcontext/lib/python3.12/site-packages/fastcontext/agent/agent_factory.py"
CLI="$HOME/.local/share/uv/tools/fastcontext/lib/python3.12/site-packages/fastcontext/cli.py"
SKILL_DIR="$HOME/.claude/skills/fastcontext"

echo "==> Checking uv..."
which uv >/dev/null || { echo "ERROR: uv not found. Install: curl -Ls https://astral.sh/uv/install.sh | sh"; exit 1; }

echo "==> Cloning fastcontext..."
if [ -d "$REPO_DIR" ]; then
  echo "    already exists, pulling..."
  git -C "$REPO_DIR" pull --ff-only
else
  git clone https://github.com/microsoft/fastcontext "$REPO_DIR"
fi

echo "==> Installing CLI..."
cd "$REPO_DIR" && uv tool install . --quiet

echo "==> Patching env var names (API_KEY -> FASTCONTEXT_API_KEY etc)..."
sed -i \
  -e 's/os\.getenv("API_KEY")/os.getenv("FASTCONTEXT_API_KEY")/g' \
  -e 's/os\.getenv("BASE_URL")/os.getenv("FASTCONTEXT_BASE_URL")/g' \
  -e 's/os\.getenv("MODEL")/os.getenv("FASTCONTEXT_MODEL")/g' \
  "$FACTORY"

echo "==> Setting default max-turns to 12..."
sed -i 's/default=4/default=12/' "$CLI"

echo "==> Adding AstGrep tool..."
cat > "$TOOL_DIR/astgrep.py" << 'PYEOF'
import json
import subprocess
from pathlib import Path
from .tool import Tool

class AstGrepTool(Tool):
    name = "AstGrep"
    description = (
        "Search code using AST structural patterns with ast-grep. "
        "Matches code structure, not strings. Use $VAR for metavariables. "
        "E.g. 'foo($A)' matches all calls to foo regardless of formatting."
    )
    parameters = {
        "type": "object",
        "properties": {
            "pattern": {"type": "string", "description": "AST pattern. Use $VAR for metavariables."},
            "lang": {"type": "string", "description": "Language: go, python, javascript, typescript, rust, java, etc."},
            "path": {"type": "string", "description": "Directory or file to search. Defaults to working directory."},
            "head_limit": {"type": "number", "description": "Limit output to first N lines. Default 200."},
        },
        "required": ["pattern", "lang"],
    }
    _ast_grep_path = "/usr/local/bin/ast-grep"

    async def call(self, parameters: str, **kwargs) -> str:
        params = json.loads(parameters)
        cwd = kwargs.get("cwd", Path.cwd().as_posix())
        path = params.get("path", cwd)
        head_limit = int(params.get("head_limit", 200))
        if not Path(path).resolve().is_relative_to(Path(cwd).resolve()):
            return f"Permission error: `{path}` is not within `{cwd}`."
        cmd = [self._ast_grep_path, "run", "--pattern", params["pattern"], "--lang", params["lang"], path]
        result = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
        output = result.stdout or result.stderr or "No matches found."
        lines = output.splitlines()
        if len(lines) > head_limit:
            output = "\n".join(lines[:head_limit]) + f"\n... truncated to {head_limit} lines"
        return output or "No matches found."
PYEOF

echo "==> Adding CodeGraph tool..."
CBM_BIN="$HOME/.local/bin/codebase-memory-mcp"
cat > "$TOOL_DIR/codegraph.py" << PYEOF
import json
import subprocess
from pathlib import Path
from .tool import Tool

CBM_PATH = "$CBM_BIN"

class CodeGraphTool(Tool):
    name = "CodeGraph"
    description = (
        "Query the codebase knowledge graph via codebase-memory-mcp CLI. "
        "Use for: symbol definitions, callers/callees, impact analysis, architecture overview. "
        "Tools: search_graph, trace_path, get_architecture, get_code_snippet, query_graph."
    )
    parameters = {
        "type": "object",
        "properties": {
            "tool": {
                "type": "string",
                "enum": ["search_graph", "trace_path", "get_architecture", "get_code_snippet", "query_graph"],
                "description": "Which graph tool to call.",
            },
            "args": {
                "type": "object",
                "description": (
                    "Args as JSON. search_graph: {query, project}. "
                    "trace_path: {function_name, project, direction, depth}. "
                    "get_architecture: {project}. get_code_snippet: {qualified_name, project}. "
                    "query_graph: {query (Cypher), project}. project always required."
                ),
            },
        },
        "required": ["tool", "args"],
    }

    async def call(self, parameters: str, **kwargs) -> str:
        params = json.loads(parameters)
        cmd = [CBM_PATH, "cli", params["tool"], json.dumps(params.get("args", {}))]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        output = result.stdout or result.stderr or "No output."
        lines = output.splitlines()
        if len(lines) > 300:
            output = "\n".join(lines[:300]) + "\n... truncated to 300 lines"
        return output
PYEOF

echo "==> Registering tools in agent_factory.py..."
grep -q "AstGrepTool" "$FACTORY" || sed -i \
  -e 's/from fastcontext.agent.tool.read import ReadTool/from fastcontext.agent.tool.read import ReadTool\n    from fastcontext.agent.tool.astgrep import AstGrepTool\n    from fastcontext.agent.tool.codegraph import CodeGraphTool/' \
  -e 's/toolset = ToolSet(\[ReadTool(), GlobTool(), GrepTool()\]/toolset = ToolSet([ReadTool(), GlobTool(), GrepTool(), AstGrepTool(), CodeGraphTool()])/' \
  "$FACTORY"

echo "==> Installing skill..."
mkdir -p "$SKILL_DIR"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/../skills/fastcontext/SKILL.md" "$SKILL_DIR/SKILL.md"

echo "==> Writing env vars to ~/.bashrc..."
sed -i '/^export FASTCONTEXT_/d' ~/.bashrc
cat >> ~/.bashrc << ENVEOF
export FASTCONTEXT_BASE_URL="$BASE_URL"
export FASTCONTEXT_API_KEY="$API_KEY"
export FASTCONTEXT_MODEL="$MODEL"
ENVEOF

echo ""
echo "Done! Run: source ~/.bashrc"
echo "Test:  cd /your/project && fastcontext -q 'how does auth work' --citation"
