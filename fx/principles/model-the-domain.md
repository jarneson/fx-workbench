---
id: model-the-domain
title: Model the Domain
description: Encode repeated states, rules, and ownership in one clear structure.
---

# Model the Domain

## Use when

Stateful code keeps gaining branches, multiple booleans must stay in sync, shape assumptions repeat across files, or several phases each recreate the same rule.

## Rule

Identify the real entities, states, transitions, invariants, ownership, and access patterns. Encode them in one boring structure with one owner—a state machine, typed model, registry, lookup table, union, reducer, or domain module—rather than scattering the rule across callers and phases.

## Decision changed

Add another conditional, flag, or phase-specific copy of the rule → model the rule once and make callers use it.

## Proof

Branches and duplicated rules disappear, invalid combinations become impossible or difficult, ownership is obvious, and callers no longer coordinate the same decision themselves.
