---
id: redesign-from-first-principles
title: Redesign from First Principles
description: Reshape a design when a new requirement would otherwise create side paths or special cases.
---

# Redesign from First Principles

## Use when

A requirement does not fit the current model and would otherwise add a side path, adapter, flag, duplicated state, or repeated special case.

## Rule

Ask what the design would look like if this requirement had existed from the start. Change the core model or boundary so the requirement follows the normal path. Carry that change through types, callers, tests, docs, and examples. Plan the whole shape, then deliver it in small working steps.

## Decision changed

Bolt the requirement onto the existing design → reshape the design so the requirement belongs there.

## Proof

The new behavior uses the same main path as existing behavior, and no temporary side channel or compatibility layer remains without a concrete need.
