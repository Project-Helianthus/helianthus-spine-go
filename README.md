# spine-go

[![Helianthus fork CI](https://github.com/Project-Helianthus/helianthus-spine-go/actions/workflows/default.yml/badge.svg?branch=helianthus-v0.7)](https://github.com/Project-Helianthus/helianthus-spine-go/actions/workflows/default.yml?query=branch%3Ahelianthus-v0.7)
[![Upstream build (main)](https://github.com/enbility/spine-go/actions/workflows/default.yml/badge.svg?branch=main)](https://github.com/enbility/spine-go/actions/workflows/default.yml?query=branch%3Amain)
[![GoDoc](https://img.shields.io/badge/godoc-reference-5272B4)](https://godoc.org/github.com/enbility/spine-go)
[![Coverage Status](https://coveralls.io/repos/github/enbility/spine-go/badge.svg?branch=main)](https://coveralls.io/github/enbility/spine-go?branch=main)
[![Go report](https://goreportcard.com/badge/github.com/enbility/spine-go)](https://goreportcard.com/report/github.com/enbility/spine-go)
[![CodeFactor](https://www.codefactor.io/repository/github/enbility/spine-go/badge)](https://www.codefactor.io/repository/github/enbility/spine-go)

## Temporary downstream fork status

This repository is a temporary downstream dependency of Project Helianthus. It
is not a Helianthus product layer and owns no Helianthus semantic policy.

Status inspected **20 September 2026**:

| Item | Evidence |
| --- | --- |
| Upstream | [`enbility/spine-go`](https://github.com/enbility/spine-go), current default branch [`dev`](https://github.com/enbility/spine-go/tree/ff669af44e3c4b908b118ce355abce704af06016). |
| Upstream release baseline | [`v0.7.0`](https://github.com/enbility/spine-go/tree/v0.7.0), commit [`0eef075cb6e8f697355a2850344333452f5590cf`](https://github.com/enbility/spine-go/commit/0eef075cb6e8f697355a2850344333452f5590cf). This is the upstream work already contained in the fork baseline. |
| Active Helianthus branch | [`helianthus-v0.7`](https://github.com/Project-Helianthus/helianthus-spine-go/tree/helianthus-v0.7), inspected at [`b0cdd8653ccc0c0d0133706172541e80179de818`](https://github.com/Project-Helianthus/helianthus-spine-go/commit/b0cdd8653ccc0c0d0133706172541e80179de818). It is the fork's default and maintained dependency branch. |
| Fork CI | [`Default`](https://github.com/Project-Helianthus/helianthus-spine-go/actions/workflows/default.yml?query=branch%3Ahelianthus-v0.7) runs for pushes to `helianthus-v0.7` and for pull requests; the badge above targets that exact workflow and branch. |
| License | The inherited [MIT license](./LICENSE) remains in force; this status block changes no license. |
| Local-only divergence | The immutable [`v0.7.0...b0cdd86` comparison](https://github.com/Project-Helianthus/helianthus-spine-go/compare/0eef075cb6e8f697355a2850344333452f5590cf...b0cdd8653ccc0c0d0133706172541e80179de818) contains 9 fork commits. At inspection, `git cherry upstream/dev helianthus-v0.7` marked all 9 as absent by patch identity from upstream `dev`; upstream `dev` had advanced separately by 135 commits. No compatibility or superset relationship is inferred. |
| Why the fork remains | Helianthus consumers still depend on the canonical downstream module path, serialized event/graph behavior, bounded correlated request/reply with unknown-field retention and transport-handoff disposition, and the bounded SPINE 1.3 HVAC corrections. These capabilities are local-only in the comparison above. |
| Upstream proposal and acceptance | No upstream proposal is linked for this exact 9-commit series. Whether upstream would accept any individual capability is **unknown**. Local commits are not presented as merged, proposed, rejected, or scheduled upstream work. |
| Return condition | Return to an upstream release only after it provides the required equivalent capabilities, the SHIP dependency and downstream consumers are migrated off the fork module paths, and the resulting dependency passes their normal compatibility and CI gates. No return version or date is currently established. |

The counts and upstream `dev` revision are an inspection snapshot. Re-run the
comparison before using them for a dependency update; this README does not
authorize an upgrade or upstream submission.

## Introduction

This library provides an implementation of SPINE 1.3 in [go](https://golang.org), which is part of the [EEBUS](https://eebus.org) specification.

Basic understanding of the EEBUS concepts SHIP and SPINE to use this library is required. Please check the corresponding specifications on the [EEBUS specifications and media website](https://www.eebus.org/specifications-media/).

This repository was started as part of the [eebus-go](https://github.com/enbility/eebus-go) before it was moved into its own repository and this separate go package.

__Important:__ In contrast to the EEBUS recommendation to use a "Generic" client feature, this library does not support this for the local device! Instead one should create a feature type with the client role for every required feature.

## Packages

### api

This package contains required interfaces. They are used extensivly to be able to mock everything and implement tests that focus specificaly on a limited set of interface implementations

### integrationtests

This packge contains tests that cover implementations of multiple packages in concert.

### mocks

This package contains auto generated mocks for the interfaces defined in the api package using [Mockery](https://github.com/vektra/mockery).

### model

This package contains the go represenation of the SPINE data model. It makes use of go tags for proper JSON serialization and also for implementing generic SPINE feature to function and data mapping.

### spine

This package contains the implementation for working with the SPINE devices, entites, features, functions and data.

### util

This package contains generic helpers used by most of the packages, e.g. for working with pointers.
