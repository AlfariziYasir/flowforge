# FlowForge — Product Requirements Document (MVP)

**Project Codename:** FlowForge
**Product Type:** Multi-tenant workflow automation platform
**Reference Products:** Zapier, n8n
**Document Status:** Draft v3
**Primary Goal:** Build a portfolio-grade MVP that feels like a simplified workflow automation platform, with visual workflow building, reliable execution, run history, and one AI-assisted feature.

---

## 1. Product Vision

FlowForge is a self-hosted workflow automation platform where users can create, version, execute, and monitor automated workflows in real time.

The product should feel like a smaller but credible version of Zapier or n8n:

- **Zapier-like** in its focus on automation use cases and easy workflow creation
- **n8n-like** in its visual node-based workflow editor and execution transparency
- **Portfolio-friendly** in its scope, code quality, and engineering depth

The MVP must be realistic for one engineer with AI assistance.

---

## 2. Product Goals

### 2.1 Business Goals

1. Demonstrate the ability to design and build a workflow automation platform.
2. Show strong backend engineering through a deterministic workflow engine.
3. Show product thinking through versioning, run history, and monitoring.
4. Show modern engineering practices through tests, Docker, CI, and documentation.
5. Include one meaningful AI feature that adds visible value.

### 2.2 Product Goals

1. Users can build workflows visually.
2. Users can save, version, and run workflows.
3. Runs can be inspected step by step.
4. Execution status can be seen in near real time.
5. The system supports multiple tenants with proper isolation.

---

## 3. Success Criteria

The project is successful if a reviewer can clearly see that:

- A workflow can be created and edited.
- A workflow definition can be validated before execution.
- A workflow can run with dependency-aware execution.
- Run progress is visible in the UI.
- Workflow history and logs are available.
- Tenant data is isolated.
- The repository is polished enough to serve as a portfolio project.

---

## 4. Target Users and Roles

### 4.1 Admin

Workspace owner.

- Manage workspace settings
- Invite or remove users
- View and manage all workflows
- View all run history and logs

### 4.2 Editor

Workflow builder.

- Create and edit workflows
- Save workflow drafts
- Publish workflow versions
- Trigger manual runs
- View logs and execution history
- Use the AI workflow helper

### 4.3 Viewer

Read-only user.

- View workflows
- View run history
- View logs and execution results
- Cannot edit or trigger workflows

---

## 5. MVP Scope

This MVP should be intentionally small.

### 5.1 In Scope

- Multi-tenant authentication
- Role-based access control
- Workflow CRUD
- Workflow versioning
- Visual workflow editor
- DAG validation
- Manual workflow execution
- Dependency-aware step execution
- Retry and timeout handling
- Run history and logs
- Real-time run updates
- One AI-assisted feature
- Docker and local development setup
- Tests and CI pipeline
- README and architecture documentation

### 5.2 Out of Scope for MVP

- OAuth credential vault
- Marketplace or plugin system
- Webhook triggers
- Cron scheduling
- Wait-for-event execution
- Loop or foreach nodes
- Compensation flows
- GraphQL
- Billing
- Multi-region deployment
- Full observability stack
- OpenTelemetry
- Advanced analytics dashboards

---

## 6. Core User Flow

A user should be able to:

1. Sign in.
2. Create a new workflow.
3. Add nodes visually.
4. Connect nodes to form a valid DAG.
5. Save a draft version.
6. Publish the workflow.
7. Trigger a manual run.
8. Watch the run progress live.
9. Open the run detail page.
10. Inspect logs and node outputs.
11. Roll back to a previous version if needed.

---

## 7. Workflow Model

Workflows are represented as directed acyclic graphs.

### 7.1 Workflow Definition

A workflow contains:

- `id`
- `tenant_id`
- `name`
- `description`
- `status` (`draft`, `published`, `archived`)
- `version`
- `nodes`
- `edges`
- `trigger_type`
- `retry_policy`
- `timeout_policy`
- `created_by`
- `created_at`
- `updated_at`

### 7.2 Required Node Types for MVP

The MVP should support only a small set of nodes:

- **HTTP** — send a request to an external endpoint
- **DELAY** — wait for a duration
- **CONDITION** — branch based on a boolean expression
- **TRANSFORM** — reshape or map data

These are enough to demonstrate a real workflow platform without overbuilding.

### 7.3 Workflow Rules

- A workflow must be a valid DAG.
- Cycles must be rejected.
- Every node must have a supported type.
- Every edge must reference valid nodes.
- Downstream nodes may only run after dependencies succeed.
- Independent branches may run in parallel.

---

## 8. Workflow Versioning

Versioning is required.

### Requirements

- Draft changes must not affect the published workflow.
- Publishing a workflow creates an immutable version snapshot.
- Users can browse previous versions.
- Users can roll back to a previous published version.
- The current active version must be explicit.

### Version lifecycle

- `draft` — editable
- `published` — active and immutable
- `archived` — historical and read-only

---

## 9. Execution Requirements

### 9.1 Execution behavior

The engine must:

- Validate workflow definitions before execution
- Topologically sort the DAG
- Execute ready nodes in dependency order
- Run parallel branches when possible
- Persist step state and outputs
- Stop on unrecoverable failure
- Respect global workflow timeout
- Respect per-step timeout

### 9.2 Retry policy

- Support configurable retries per step
- Use exponential backoff as the default strategy
- Record each retry attempt
- Mark the run as failed when retry budget is exhausted

### 9.3 Execution states

Workflow run states:

- `queued`
- `running`
- `succeeded`
- `failed`
- `cancelled`
- `timed_out`

Step states:

- `pending`
- `running`
- `succeeded`
- `failed`
- `retrying`
- `skipped`

---

## 10. API Requirements

### 10.1 Authentication and authorization

- JWT-based authentication
- Role-based access control
- Tenant-scoped authorization on all protected endpoints
- Safe handling of unauthorized and invalid requests

### 10.2 Core endpoints

- Create, update, delete, and list workflows
- Create and list workflow versions
- Publish a workflow version
- Roll back to a previous version
- Trigger a manual run
- List workflow runs
- Retrieve run details
- Retrieve step logs
- Retrieve dashboard metrics

### 10.3 API quality requirements

- Input validation on every endpoint
- Pagination on list endpoints
- Filtering where useful
- Rate limiting for high-volume endpoints
- Consistent error responses
- No leakage of cross-tenant data

---

## 11. Multi-Tenant and Security Requirements

### 11.1 Tenant isolation

- Every tenant-scoped record must include `tenant_id`.
- A tenant must never read or modify another tenant’s data.
- Tenant access must be enforced at both application and data layers.

### 11.2 Security

- Passwords must be hashed securely.
- Secrets must not be stored in plain text.
- External input must be validated and sanitized.
- Workflow payloads must not allow unsafe execution by default.
- Sensitive values must not be logged.

### 11.3 RBAC matrix

| Action           | Admin | Editor | Viewer |
| ---------------- | ----: | -----: | -----: |
| View workflows   |   Yes |    Yes |    Yes |
| Create workflow  |   Yes |    Yes |     No |
| Edit workflow    |   Yes |    Yes |     No |
| Publish workflow |   Yes |    Yes |     No |
| Trigger workflow |   Yes |    Yes |     No |
| View logs        |   Yes |    Yes |    Yes |
| Manage users     |   Yes |     No |     No |

---

## 12. Real-Time Monitoring Requirements

The UI must show live run progress.

### Required dashboard capabilities

- Live node status updates
- Visual DAG rendering
- Run history view
- Run detail page with logs and outputs
- Basic health summary for active and recent runs

### Transport

Use SSE or WebSocket. For the MVP, SSE is acceptable if it is simpler to implement and maintain.

---

## 13. AI Feature

The MVP should include exactly one AI-assisted feature.

### Selected AI feature: AI Failure Analysis

When a workflow run fails, the system sends redacted failure context and logs to the LLM provider to return a structured diagnosis explaining the cause and suggesting a fix.

### Requirements

- The model output must be structured and strictly schema-validated (`POST /api/v1/workflow-runs/{runId}/analysis`).
- Redact secrets, sensitive headers, and credentials from logs/payloads before calling LLM.
- The UI presents the diagnosis as a read-only suggestion on the run detail page.
- Prompt strategy and output validation must be documented.

### AI safety rules

- Enforce a strict JSON output schema (`diagnosis`, `possibleCause`, `suggestedFix`, `confidence`).
- Limit retries for malformed model outputs.
- Remove sensitive data before sending context to the model.
- Never execute AI-generated commands automatically without user validation.

---

## 14. Data Requirements

### Core entities

- Tenants
- Users
- Roles
- Workflows
- Workflow versions
- Workflow nodes
- Workflow edges
- Workflow runs
- Step runs
- Execution logs

### Data requirements

- Workflow definitions must be versioned.
- Workflow run records must be immutable after completion.
- Logs must be queryable by run and step.
- The schema should support tenant filtering.
- Migrations must be safe and reversible.

### Storage strategy

- Use a relational database for core entities.
- Store flexible node payloads in JSON or JSON-like fields where appropriate.
- Keep the logging strategy simple enough for MVP while remaining scalable later.

---

## 15. Frontend Requirements

### Required screens

- Login page
- Workflow list page
- Workflow editor
- Workflow detail page
- Run history page
- Run detail and logs page
- Dashboard page
- AI workflow generation panel or page

### UX requirements

- Clear visual DAG editor
- Fast access to logs and status
- Responsive layout
- Simple and readable workflow actions
- Basic optimistic UI where it improves the experience

---

## 16. Non-Functional Requirements

### Reliability

- Failures should be visible and debuggable.
- Retries should be deterministic.
- Worker failures should not corrupt state.

### Performance

- Core list endpoints should paginate.
- Execution updates should stream efficiently.
- The UI should remain responsive during runs.

### Maintainability

- Clear separation of concerns
- Testable code structure
- Simple and understandable architecture
- Small enough scope for one engineer to complete

### Observability

- Structured logs
- Basic metrics for runs, success rate, failure rate, and duration
- Clear execution history

### Scalability

- Stateless API and worker processes
- Queue-based execution model
- Ability to scale workers independently later

---

## 17. Engineering Deliverables

The repository should include:

- Backend implementation
- Frontend implementation
- Tests for core engine and API
- Dockerfile and docker-compose setup
- CI pipeline
- README with setup and architecture
- Architecture diagram
- Database migration files
- REVIEW.md for code review exercise
- Sample workflow data
- Demo-ready example workflow

---

## 18. Definition of Done

The project is done when:

- A user can create, publish, and run a workflow.
- The workflow engine validates DAGs correctly.
- Run progress appears in real time.
- Run history and logs are accessible.
- Tenant isolation and RBAC work correctly.
- The AI feature works safely.
- The project can be run locally with Docker.
- The repository looks strong enough to present as a portfolio project.

---

## 19. Recommended Build Phases

### Phase 1 — Core backend

- Auth
- Tenant isolation
- Workflow CRUD
- Workflow versioning
- DAG validation
- Manual run execution
- Run history

### Phase 2 — Frontend and monitoring

- Visual editor
- Live updates
- Run details and logs
- Basic dashboard

### Phase 3 — AI enhancement

- Natural language workflow generation
- Validation and safe output handling

### Phase 4 — Final polish

- CI/CD
- Documentation
- Demo assets
- Minor performance improvements

---

## 20. Open Questions

1. Should SSE or WebSocket be used for the MVP transport?
2. Should the first release prioritize a better visual editor or a stronger execution engine?
3. Should the AI feature generate the full workflow or only assist with node creation?
4. Should logs stay in the main database for MVP or be separated later?
5. Which sample workflow best demonstrates the product in a portfolio demo?

---

## 21. Recommended Positioning

For the portfolio, position FlowForge as:

> A simplified real-time workflow automation platform inspired by Zapier and n8n, built to demonstrate DAG execution, versioning, monitoring, and AI-assisted workflow creation.

This positioning is realistic, credible, and achievable for a solo developer with AI assistance.
