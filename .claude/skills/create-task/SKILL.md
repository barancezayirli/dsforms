---
name: create-task
description: Create a dsforms task as a GitHub issue on the project board, from a plan we worked out together or from a doc the user provides. Use when the user says "create a task", "open an issue", "add this to the board", or hands over a plan or doc to track.
---

# Create a task

Tasks are big. One plan is one task, one issue, one PR. Do not split a plan into many small issues;
split only when the work needs separate plans and separate releases, and say why (AGENT.md §2,
"Sizing a task"). Steps inside a task are the checklist in its body, never sub-issues.

## Input

- A plan we just worked out in the conversation, or
- A doc the user gives you. Read all of it, then condense it into one task.

## Task shape

Title: short and plain, no prefix, no `feat:`. (The PR title carries the release bump, the issue does
not.)

Body:

```markdown
## Goal
One or two sentences: what is true when this is finished.

## What it does
What it changes or adds for an operator or a client, in plain language.

## Done when
The observable result that means the task is finished.

## Steps
- [ ] Outcome-level step
- [ ] **Checkpoint:** what must work before the next steps are built
- [ ] Outcome-level step
```

- 3 to 8 steps, each an outcome, not a file or function.
- Mark a step that gates the rest as a **Checkpoint**.
- No commit hashes, no test counts, no estimates.
- Keep the whole body short enough to read in under a minute.
- Avoid "honest", "actually", "plain(ly)", "genuine", and "X, not Y" contrasts.
- The repo is public. Write for a reader who never saw our conversation.
- **Never put an unfixed security weakness in an issue**, not even vaguely. Stop and tell the user;
  it gets fixed directly or goes into a private security advisory.

Label: `enhancement` for new behaviour, `bug` for a defect, `documentation` for docs only.

## Procedure

1. Check the plan is not already a task:
   `gh issue list -R barancezayirli/dsforms --state open --search "<key words>"`.
   If it is, propose an update to that issue instead.
2. Show the draft (title, label, body) in chat and **wait for a yes**. Creating an issue publishes it.
3. Write the body to a file in the scratchpad, then create exactly one issue:
   `gh issue create -R barancezayirli/dsforms --title "<title>" --label <label> --body-file <file>`.
   No loops, no batch creation.
4. Add it to the board (skip if the board's auto-add already did):
   `gh project item-add 3 --owner barancezayirli --url <issue-url>`.
5. Verify on GitHub (`gh issue view <n>`) before reporting. A command that was interrupted may
   still have run.
6. Report the issue number and URL (`#<n>`).

The workflow that picks the task up afterwards is in `AGENT.md` §2.
