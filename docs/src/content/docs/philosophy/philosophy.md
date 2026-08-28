---
title: Philosophy
description: Why wi treats AI-assisted development as durable case work.
---

Most agent workflows begin with a terminal, a checkout, and a conversation. Those are useful tools, but they are poor identities for work. Processes stop. tmux sessions disappear. Worktrees get reused. A task often outlives all three.

`wi` makes the work item the durable center instead.

## Work should survive its current interface

A description, branch, conversation, and history should still make sense after a restart or after replacing the process that created them. Tmux and Pi are current access methods, not the definition of the task.

## Capture and execution are different decisions

Recording backlog work should not launch an agent or allocate a checkout. Starting work is an explicit transition with resource consequences.

This also makes failure less destructive. If creation succeeds but runtime startup fails, the task remains captured rather than disappearing because the final step failed.

## State should say one thing

Lifecycle records human intent. Agent status records process activity. Worktree status records checkout condition. Combining them into a single status creates contradictions such as an item becoming "done" merely because an agent stopped.

`wi` keeps these facts separate while showing them together when you inspect an item.

## Local control matters

Source code, prompts, conversations, and credentials stay on the user's machine unless the chosen agent or Git workflow sends them elsewhere. `wi` does not require a hosted control service.

## Agents are participants, not owners

An agent can work on an item, file a constrained follow-up, and add to its history. The durable item still belongs to the user's workflow. Cleanup, lifecycle changes, and destructive operations remain explicit.
