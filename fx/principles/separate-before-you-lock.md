---
id: separate-before-you-lock
title: Separate Before You Lock
description: Remove shared write targets before adding locks or other serialization.
---

# Separate Before You Lock

## Use when

Concurrent agents, workers, or processes might write the same file, branch, key, record, directory, or in-memory state.

## Rule

First ask whether they truly need one mutable object. If not, give each actor its own owned target and combine results at a read or integration boundary. If one shared target is a real requirement, enforce access structurally with a single writer, sequential phases, lock, transaction, compare-and-swap, or atomic replacement. Instructions and conventions are not concurrency control.

## Decision changed

Add coordination or a lock around shared writes → remove the shared write target when ownership can be separated.

## Proof

Writers have disjoint owned targets, or the genuinely shared target has one explicit structural serialization mechanism; correctness does not depend on timing or agents remembering a rule.
