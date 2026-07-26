# Project Rules & Customizations for FlowForge

## 1. Implementation Plans Management
- Whenever an implementation plan is created or updated, extract and save a copy of the implementation plan file into the `.agents/plans/` directory.

## 2. Action History & Memory Tracking
- Store the history and log of the most recent actions performed inside the `.agents/memory/` directory.
- Keep relevant session context, recent action logs, and design decisions updated in `.agents/memory/`.

## 3. Problem Planning & Issue Remediation
- For every problem, bug, or architectural issue encountered, create a dedicated planning file inside the `.agents/plans/` directory to document and plan the solution before executing fixes.
