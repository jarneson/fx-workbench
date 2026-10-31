---
id: minimize-reader-load
title: Minimize Reader Load
description: Reduce pass-through layers and hidden state so code has one obvious path.
---

# Minimize Reader Load

## Use when

Code is hard to trace, or you are about to add a layer, wrapper, interface, shared field, cache, or mutable state.

## Rule

Minimize both the layers a reader must follow and the hidden state they must remember. Keep a boundary only when it hides a meaningful decision, changes the abstraction, or supports a real second implementation. Prefer local or derived state over synchronized shared state.

## Decision changed

Add indirection or shared state for organization → collapse pass-through layers and keep state near the code that owns it.

## Proof

A reader can follow where a value comes from and what can change it along one obvious path; each remaining boundary removes details the caller no longer needs to understand.
