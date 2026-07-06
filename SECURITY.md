# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Haadex, please report it privately.
**Do not open a public GitHub issue for security problems.**

Email: **053steve@gmail.com**

> A dedicated Futureboard security address will replace this once the official
> domain email is set up. Until then, use the address above — it is actively
> monitored.

Please include:

- A description of the vulnerability and its impact
- Steps to reproduce (proof of concept if possible)
- Affected version(s) or commit SHA
- Any suggested remediation

We aim to acknowledge reports within **72 hours** and to provide a resolution
timeline after triage. Once a fix is released, we're happy to credit reporters
who wish to be named.

## Scope

Haadex runs locally and manages its own Qdrant instance via Docker. Areas of
particular interest for reports:

- Handling of API keys and credentials (e.g. `OPENAI_API_KEY`,
  `OPENROUTER_API_KEY`) read from the environment or `.env`
- The MCP server surface exposed to AI agents
- Any command that executes or shells out based on indexed content

## Supported Versions

Security fixes are applied to the latest released version on the default branch.
