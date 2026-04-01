# Skill-Trainer Subagent Proposal

## Goal
Add a lightweight `skill-trainer` subagent that runs before normal implementation whenever the main agent shows uncertainty, missing-context symptoms, or repeated local searching. Its job is to treat "an unfired skill probably already exists" as the default hypothesis.

## Trigger
- Fire automatically when the main agent says or infers: "I don't understand this", "unclear", "need context", "searching for how this works", "not sure which workflow applies", or after 2+ failed repo searches.
- Fire explicitly when the user mentions skills, training, memory, workflow, or "there should already be a skill for this".

## Behavior
1. Search exact matches first.
   - Search skill `name`, `description`, and trigger keywords.
   - Prioritize symptom-based matches, not just domain nouns.
2. Search structural analogs next.
   - If no exact match, cluster candidate skills by workflow shape: discovery, verification, delegation, retry loop, output formatting, safety gate, etc.
   - Allow cross-domain reuse. A terminal-control skill can teach the shape of a browser skill if both solve "send command, wait, read output".
3. Create an empty container skill if nothing fits.
   - Scaffold `SKILL.md` with frontmatter, placeholder description, and sections like `When to use`, `Workflow`, `Known patterns`, `Open questions`.
   - Name it from the symptom/workflow, not the temporary task wording.
4. Return a compact result to the main agent.
   - `matched_skills`
   - `structural_matches`
   - `recommended_skill`
   - `created_placeholder_skill`
   - `next_action`

## Implementation Shape In Codex
- Add a `skill-trainer` explorer-style subagent with read access to skill directories, memory files, and project docs.
- Hook it into the main loop before deep work, similar to a preflight.
- The main agent delegates a bounded prompt like: "Find existing or structurally similar skills for X symptoms; create placeholder if absent."
- The subagent should be allowed to edit only the skill library path when creating placeholders.

## Retrieval Strategy
- Build a local index over all skills with:
  - `name`
  - `description`
  - headings
  - explicit "Use when" phrases
  - extracted workflow verbs
  - extracted symptom phrases
- Rank in two passes:
  - lexical match for direct triggers
  - structural match using normalized tags like `search-first`, `delegate`, `queue`, `verify-before-claim`, `create-container`, `cross-domain-analogy`
- Store successful mappings back into memory so future runs need less search.

## Empty Skill Bootstrap
When no skill matches, generate the smallest useful container:

```md
---
name: skill-name
description: "Placeholder. Use when: symptom A, symptom B, workflow C"
---

# skill-name

## When to use
## Workflow
## Known patterns
## Open questions
## Example commands
```

This makes skill capture incremental: container first, content after the task succeeds.

## Important Design Choice
Do not make the main agent manually decide whether a skill exists. Make that a delegated default check. The main agent should assume it is missing context; the `skill-trainer` decides whether that missing context already lives in the skill library, in a structurally similar skill, or needs a new empty container.
