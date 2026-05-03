#!/bin/bash
# Gru's Claude Code hook entry point (rev 3).
#
# All status-affecting hook events flow through `gru hook ingest`,
# which translates Claude's payload into gru's grammar and appends
# one line to ~/.gru/events/<sid>.jsonl. Validation, sibling-Claude
# guard, and event translation live Go-side; this script is a one-
# liner shim so the binary's logic is the single source of truth.
#
# Registered hooks: see cmd/gru/init.go (hookTypes).
exec gru hook ingest
