# Coding Prompt Pack — Design

**Date:** 2026-04-28
**Project:** Self-Funding Evolutionary AI Agent Swarm (v8)
**Spec source:** `evolutionary-swarm-project-description.md` (1,252 lines)

## Goal

Produce a series of ~33 prompts that drive end-to-end implementation of the swarm project, where each prompt corresponds to one PR on the GitHub repo. Prompts are routed across three coding tools — Claude Code, OpenAI Codex (cloud), and GitHub Copilot Coding Agent — based on which tool best fits each chunk's shape.

## Why prompts, not code

The user is the operator. The user wants to drive the build by feeding pre-written prompts into different coding agents and reviewing PRs. The prompts themselves are the deliverable; this design document records the contract those prompts adhere to.

## Tool split rationale

| Tool | Strength | Used for |
|---|---|---|
| **Codex (cloud)** | Self-contained briefs against a fresh checkout, isolated work | Standalone Go packages with clear interfaces (chain clients, LLM clients, individual tasks, individual orchestrator submodules) |
| **Claude Code** | Reads many files at once, integrates across packages | Bootstrap, NodeRunner, root orchestrator, API server, Next.js scaffold, end-to-end validation |
| **Copilot Coding Agent** | High-volume mechanical UI work, takes a GitHub issue body | Dashboard pages once API + types exist |

## Chunk plan (33 chunks)

The 66 build-order steps in the spec collapse into 33 PR-sized chunks (~1–3 hours each, 1–5 files each, testable in isolation). The full table lives in `docs/coding-prompt-pack.md` at the top of the file. Tool assignments:

- **Claude Code:** chunks 1, 9, 14, 21, 28, 29, 32, 33 (8 chunks — integration / cross-cutting)
- **Codex:** chunks 2–8, 10–13, 15–20, 22–27 (23 chunks — isolated packages)
- **Copilot Coding Agent:** chunks 30, 31 (2 chunks — dashboard pages)

## Prompt template

Every prompt follows the same skeleton:

1. **Header** — chunk number, name, tool, branch name, dependencies, scope estimate
2. **Context** — what's already in the repo, governing spec section
3. **Goal** — one paragraph in plain English
4. **In scope** — files to create/modify, specific behaviors, spec line refs
5. **Out of scope** — explicit non-goals, pointers to later chunks
6. **Acceptance criteria** — concrete checks (compile, vet, tests, behavior)
7. **Tests required** — what to test, at what level
8. **PR** — title format, body checklist

Tool-specific tweaks:
- Codex prompts assume `evolutionary-swarm-project-description.md` is in the checkout (which it is) and reference spec line ranges directly — no need to inline 200-line excerpts
- Claude Code prompts reference local skills (`superpowers:test-driven-development`, `superpowers:verification-before-completion`) and assume access to local Postgres / Redis / RPC keys
- Copilot Coding Agent prompts are formatted as GitHub issue bodies (user story → acceptance criteria checklist → file list)

## Conventions

**Branching:** `feat/NN-slug` off latest `main`, squash-merge.

**Parallelism (DAG):**
- Chunks 2–8 in parallel (foundation packages, no file overlap)
- Chunks 11, 12, 25, 26 in parallel after chunk 10 (the four tasks)
- Chunks 17, 18, 19, 20 in parallel after chunks 8 + 13 (evolution operators)
- Roughly 3–5 Codex tasks can run simultaneously; Claude Code is reserved for integration chunks

**Testing:**
- Every logic chunk ships with unit tests in the same PR
- `ChainClient` and `LLMClient` mocks introduced in chunks 3 and 6 → all later chunks test against fakes
- Devnet integration tests only in chunks 4, 5, 9, 33

**Cost & secrets:**
- All LLM calls go through `LLMClient.Complete` and increment `agent_ledgers` — verified in chunk 13 tests
- zap log redactor (chunk 7) installed before any chunk touching wallets / LLM responses
- `.env` never committed; `.env.example` updated as new vars are added

**Spec fidelity:**
Every prompt includes a "spec sections" pointer. If an agent finds a contradiction with the spec mid-implementation, the rule is: **stop, surface in PR description, do not silently deviate.**

## Prerequisites (before running chunk 1)

- GitHub repo with `gh` CLI authenticated
- Go 1.22+, Node 20+, Docker, `psql`
- Empty local Postgres + Redis (or Render/Railway projects)
- API keys: Anthropic, OpenAI, Helius (Solana), Alchemy (Base), Telegram bot token
- The repo currently contains only `evolutionary-swarm-project-description.md`; chunk 1 scaffolds everything else

## Output

The prompt pack is written to `docs/coding-prompt-pack.md` (single file, all 33 prompts plus index and DAG).
