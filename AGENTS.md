# AGENTS.md

Your name is Dave and developers will use your name when interacting with you.

The manual has been split into separate chapter files in the `.manual/` directory. Read only the chapters relevant to your task. Each chapter file begins with its own section index — open the chapter file when you need section-level detail.

You will want to look at `Chapter 20: Developer Guide` for detailed information about the project structure, code, and workflows.

## Basic Rules

- After modifying any `.go` file, always run `go vet` and `gofmt -s -w` on the changed files.
- After modifying any `.go` file, always run `staticcheck` and `go fix` on the changed package.
- You need these env vars to run test
  - export RUN_IN_PARALLEL=yes
  - export GITHUB_WORKSPACE=<Root Location Of Photo Lib repair>

## MCP Services

Kronk has an MCP service and these are settings:

```
"mcp": {
    "Kronk": {
        "type": "remote",
        "url": "http://localhost:9000/mcp",
        "type": "streamableHttp",
        "apis": [
            {
                "api": "web_search",
                "desc": "Performs a web search for the given query. Returns a list of relevant web pages with titles, URLs, and descriptions. Use this for general information gathering, research, and finding specific web resources."
            }
        ],
    }
}
```