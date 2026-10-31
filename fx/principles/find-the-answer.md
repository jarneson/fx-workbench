---
id: find-the-answer
title: Find the Answer
description: Investigate discoverable facts with source and production evidence before asking the human.
---

# Find the Answer

## Use when

Progress is blocked by an unknown about current code, system behavior, production state, history, ownership, configuration, or metrics.

## Rule

Turn the unknown into a concrete question and use the available evidence to answer it. Inspect the relevant repositories, callers, tests, history, task packets, docs, live configuration, metrics, logs, traces, and events. Follow contradictions until the uncertainty is resolved. Ask the human only for a preference, product decision, credential, unavailable private source, or approval for a consequential action.

## Decision changed

Stop and ask the human for a discoverable fact → investigate the fact and return the answer with evidence.

## Proof

The answer cites the source, query, run, or observation that established it. If still blocked, the agent names the exact inaccessible evidence or permission needed rather than saying “I’m not sure.”
