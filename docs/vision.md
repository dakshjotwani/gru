# Gru Vision: Autonomous Project Management

## The Core Idea

Gru evolves from a session monitor into an **autonomous project management system** — where agent teams execute on ongoing work, surface decisions to a human operator, and proactively discover and propose new work from signals in the organization's existing tools (Slack, Confluence, etc.).

The human's role shifts from individual contributor to **decision-maker and approver**: reviewing what agents need, unblocking them, and steering project scope.

---

## Key Concepts

### Projects as First-Class Entities

Today, a project and a repo are tied 1:1. In the evolved system, a **project** is the primary unit:

- A project has a **goal**, **success criteria**, and **scope** — not just a repo
- A project can span **multiple repos and resources** (codebases, docs, APIs, data sources)
- Projects persist across many sessions, potentially indefinitely
- Project state (scope, decisions made, open questions, history) is durable and queryable — not just session logs

### Agent Teams

Multiple agents work within a project, potentially in parallel:

- Agents may be specialized (code, review, research, planning) or general
- Agents coordinate within a project's context, sharing state and history
- Agent activity is tied to a project, not just a session

### Human-in-the-Loop Approvals

Agents surface blockers and decisions upward to the operator:

- When an agent needs a decision, it enters a **waiting/attention** state
- The operator sees a queue: "N agents waiting on you, here's what each needs"
- Context is surfaced with each request — not just "what do you approve?" but "why does this matter, what's the tradeoff?"
- This is closer to a **PR review queue** than a monitoring dashboard

### Proactive Project Discovery

Agents periodically scan organizational tools (Slack, Confluence, etc.) to:

- **Suggest new projects**: identify recurring pain points, requests, or opportunities that warrant spinning up a new agent team
- **Update project scope**: surface new information that should change what an existing project's agents are working on
- **Flag stale projects**: identify work that may no longer be relevant

The signal-to-noise problem here is real — this only works with a strong, structured **project schema** that lets agents evaluate whether something clears the bar for a new project, rather than surfacing everything.

---

## Architectural Gaps (Current → Vision)

| Area | Today | Vision |
|------|-------|--------|
| Project identity | Session ≈ project | Project is durable, spans many sessions |
| Project state | Session logs | Structured: goal, scope, decisions, history |
| Resources | Single repo | Multiple repos + external resources |
| Agent coordination | Independent sessions | Teams with shared project context |
| Approval UX | Monitoring dashboard | Decision queue with context |
| Work discovery | Manual | Proactive agents reading org tools |

---

## The Foundational Question: What Is a Project?

Everything downstream — scope updates, agent coordination, approval routing, discovery — flows through the **project schema**: what fields define a project and how is scope expressed?

Options to explore:
- **Linear-style**: structured ticket with fields (priority, status, owner, labels)
- **Notion-style**: freeform document with structured metadata
- **Custom schema**: richer than a ticket, purpose-built for agent execution (goal, constraints, resources, success criteria, open decisions)

Getting this wrong makes everything else unstable. This is the first thing to nail down.

---

## Open Questions for Brainstorming

1. **What is a project?** What schema captures goal, scope, resources, and success criteria in a way agents can reason about?
2. **How do agents coordinate within a project?** Shared memory? Message passing? A coordinator agent?
3. **What does the approval UX look like?** How do you present a decision queue without it becoming overwhelming?
4. **How do you prevent noise in proactive discovery?** What bar must a Slack thread or Confluence doc clear before it becomes a project suggestion?
5. **How does project scope change over time?** Who/what can update scope — agents, the operator, both?
6. **How do you handle long-running projects?** Context compression, project history summarization, handing off between agents?
7. **What's the right granularity for a project?** Is it a feature, an epic, a quarter's worth of work?
8. **How do you measure project health?** Velocity, blockers, agent utilization — what signals matter to the operator?
