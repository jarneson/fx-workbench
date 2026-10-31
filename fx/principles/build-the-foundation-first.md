---
id: build-the-foundation-first
title: Build the Foundation First
description: Build the smallest shared structural base that later work will actually use.
---

# Build the Foundation First

## Use when

A change has several phases, callers, or follow-up tasks that will all need the same base.

## Rule

Build the smallest structural foundation that every later step will use, then put the first real case through it. Prefer core types, a clear boundary, a test seam, a generator, or an enforced check over repeated coordination in feature code. Keep the foundation narrow and on the critical path; do not build a framework for imagined future use.

## Decision changed

Implement each feature slice with its own glue → first create the shared structure that makes each slice straightforward.

## Proof

Every later phase uses the foundation, the first real case proves it works, and callers need fewer special cases rather than more abstraction.
