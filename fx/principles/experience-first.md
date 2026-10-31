---
id: experience-first
title: Experience First
description: Choose the smallest complete experience that makes the user’s job clear and reliable.
---

# Experience First

## Use when

Implementation convenience, internal architecture, option count, or delivery speed conflicts with the experience of the person using, operating, integrating, or maintaining the result.

## Rule

Start with the job that person needs to complete. Protect the core path, ship fewer things completely, justify every option, and include clear feedback, errors, and recovery. Do not leak internal structure into the interface just because it is easier to implement.

## Decision changed

Choose what is easiest to build or exposes the most capability → choose the smallest complete experience that makes the user’s job clear and reliable.

## Proof

The intended user can complete the core job without understanding internal implementation details, and failure states tell them what happened and what to do next.
