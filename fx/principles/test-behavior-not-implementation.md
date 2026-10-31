---
id: test-behavior-not-implementation
title: Test Behavior, Not Implementation
description: Test stable observable contracts rather than private implementation details.
---

# Test Behavior, Not Implementation

## Use when

Writing, reviewing, changing, or keeping a test.

## Rule

Drive the code through the smallest stable interface its user or caller relies on. Derive expected results from the requirement, not from the implementation. Assert observable values, errors, written state, emitted messages, external requests, and invariants—not private helper calls, internal ordering, or temporary data shape.

## Decision changed

Pin the current implementation because it is easy to mock or inspect → prove the contract that must survive a refactor.

## Proof

The test fails when the promised behavior is broken and keeps passing when only the internals change.
