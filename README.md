# FlowForge

FlowForge is a high-performance backend platform built with Go, PostgreSQL, and Redis following Clean Architecture principles.

## Prerequisites & Requirements

- **Go**: 1.22+
- **PostgreSQL**: 15+ (required for column-scoped `ON DELETE SET NULL` clause in migration schemas)
- **Redis**: 6.2+ (for session registry, user revocation index, and token blacklisting)

## Getting Started

```bash
# Build and run tests
make build
make ci
```