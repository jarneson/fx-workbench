---
id: type-system-discipline
title: Type System Discipline
description: Use types to prevent invalid states, swapped values, and unhandled cases.
---

# Type System Discipline

## Use when

Designing state, identifiers, function signatures, protocol values, or variants in a statically typed language.

## Rule

Use the type system to prevent invalid states, swapped values, and unhandled cases. Give different meanings different types when mixing them would be dangerous. Parse external data into those types at the boundary. Derive types from the schema or file that already owns the shape instead of maintaining a parallel copy.

## Decision changed

Represent meaning with comments, loose primitives, and runtime checks → make invalid combinations fail to compile.

## Proof

Invalid states, mismatched IDs, and unhandled variants cause compile errors; protocol types come from one authoritative schema; downstream code needs fewer checks.
