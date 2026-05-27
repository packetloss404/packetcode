# Code Intelligence

Packetcode exposes code intelligence as model-facing read-only tools, not slash commands. The agent can list symbols, find likely definitions, find references, and inspect syntax diagnostics when it needs navigation help.

## Tools

- `list_symbols`: lists functions, methods, classes, types, constants, and variables in a file, directory, or workspace.
- `find_definition`: finds likely definition candidates for a symbol.
- `find_references`: finds bounded whole-identifier references with one-line snippets.
- `get_diagnostics`: reports bounded syntax diagnostics. Go syntax diagnostics are supported locally.

All results are scoped to the project root, capped, and rendered as `path:line:column` entries. These tools never edit files and do not require approval under built-in permission profiles.

## Engines

Go files use the standard Go parser and AST for symbols and definition candidates. Other common languages use bounded lexical heuristics for symbols and references. Packetcode does not start `gopls`, `tsserver`, or other language servers in this release; those can be added later as optional accelerators.

The tools return candidates, not guaranteed compiler-level semantic answers. When precision matters, the agent should combine code-intelligence output with `read_file`, `search_codebase`, and tests.

## Background Agents

Background jobs receive the same read-only code-intelligence tools. Write-capable jobs use the job worktree root, so symbol/reference lookups reflect that isolated checkout rather than the foreground workspace.
