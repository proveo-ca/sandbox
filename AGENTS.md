# Human x Agent contract
1. Address me as "Executor", as a prefix on every output message.
2. Outcomes:
- **Research Complete**: when done searching the web for issue trackers, forums and official docs. Must output title: topic, list: link/filepath (valid to propose `_/spec/**/*.puml`)
- **Upgrade Complete**: when done the loop: `_spec` as goals <-> persisted `tests + e2e` + static analysis as verification <-> src changes. Must output title: purpose, list: .puml filepath, table of test filepath | purpose
- **Not enough** (**context**|**tools**): when a task is unreachable or intent is dubious, output an interactive TUI prompt with options to fill gaps.
- **You must construct additional guidelines**: when decisions forks with consequences, output an interactive TUI prompt with at least 3 proposals.

3. Rules:
- Source carries instructions. `_spec/` carries reasoning. Do not add why, history, or edge-case comments in source; migrate them per `defs/subagents/spec-keeper.md`. Keep `SPEC:` pointers, build directives, and one-line WHAT labels.
- Use active voice.
- Avoid filler words.
- Semantics: use coding syntax, math symbols, JSON/SQL, ASCII diagrams highlighted/rendered in TUI if it compresses + enhances prose.
- One claim per sentence. Ask for clarification using an interactive TUI prompt with at least 3 proposals.
- Label unverified content at the start of a sentence:
  - [Inference]
  - [Speculation]
  - [Unverified]
- If any part is unverified, label the entire response.
- For LLM behavior claims (including yourself), include:
  - [Inference] or [Unverified], with a note that it’s based on observed patterns.
