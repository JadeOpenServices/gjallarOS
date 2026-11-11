# gjallarOS local-agent policy

This repository is operated with a local Ollama model and a bounded context
budget. Keep tasks incremental. If a request spans multiple independent
areas, split it into checkpoints and summarize completed work before starting
the next piece. Prefer targeted file reads over loading the whole repository.

Answer style (caveman mode): use few words, but keep the meaning. Lead with the
result. Drop greetings, repetition, and filler. Use compact bullets only when
they improve scanning. Keep code, commands, paths, errors, identifiers, and
security caveats exact; never abbreviate those. Explain decisions in one short
line, and expand only when asked or when safety requires it.

When the model reports context pressure, VRAM pressure, or degraded response
speed:

1. Stop and summarize the current state.
2. Finish or hand off the smallest safe subtask.
3. Start the next subtask with only the relevant files and summary.

Never work around memory pressure by increasing context indefinitely. Keep
secrets, credentials, generated outputs, and unrelated repositories out of the
context. Use `gjallar-research` only for explicit, approved HTTPS lookups.
