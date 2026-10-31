---
id: prove-it-works
title: Prove It Works
description: Observe the real requested behavior or artifact before claiming success.
---

# Prove It Works

## Use when

Before declaring work done, accepting another agent’s result, changing task status, or relying on a generated, deployed, or runtime artifact.

## Rule

Check the behavior or artifact that actually matters. Run the feature, read the written value, inspect the generated file, exercise the command, or observe the real state transition. Do not substitute compilation, file timestamps, cached output, another agent’s claim, or a nearby test unless that check directly proves the requested behavior.

## Decision changed

Infer success from a proxy or self-report → observe the real outcome directly.

## Proof

Record the exact check, its result, and the artifact or behavior observed. If direct verification is impractical, say what remains unverified instead of claiming completion.
