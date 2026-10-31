---
id: boundary-discipline
title: Boundary Discipline
description: Validate raw data at system boundaries and pass trusted domain values inward.
---

# Boundary Discipline

## Use when

Adding validation, parsing, error handling, framework wiring, storage decoding, CLI/config input, or external API integration.

## Rule

Validate raw or untrusted data when it enters the system, convert it into domain types, and pass those types inward. Do not repeat parsing and defensive checks throughout business logic. Keep transport, storage, and framework details at the edge; keep the core logic independent and pure where practical. Reading persisted or queued data is another boundary; internal assertions remain valid for states that should be impossible.

## Decision changed

Scatter guards and boundary representations through the call chain → validate and translate once, then let internal code work with trusted domain values.

## Proof

Raw boundary types do not leak into the core, validation has one owner, business rules can be tested without the framework, and repeated guards disappear.
