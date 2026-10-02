"""JetStream stream definitions.

These mirror ``ensureStreams`` in ``internal/storage/nats.go``. A worker pool must
be startable on its own -- the intelligence tier is deployed separately from the
gateway -- so a worker that finds a stream missing creates it rather than refusing
to start.

Retention is chosen per stream rather than globally:

* ``SYNAPASS_USAGE`` keeps a week. Long enough to survive a consumer outage,
  short enough that it never becomes an unmanaged second copy of ClickHouse.
* ``SYNAPASS_JOBS`` uses a work-queue policy, so a job is removed once it is
  acknowledged and a restarted worker does not replay jobs that already ran.
* ``SYNAPASS_EVENTS`` keeps a month: audit and health events are small and are
  consulted historically.
"""

from __future__ import annotations

from typing import Any

from .bus import (
    SUBJECT_AUDIT_EVENT,
    SUBJECT_EVAL_JOB,
    SUBJECT_EVAL_RESULT,
    SUBJECT_PROMPT_ANALYSIS,
    SUBJECT_PROVIDER_HEALTH,
    SUBJECT_PROVIDER_STATUS_CHANGE,
    SUBJECT_REPLAY_JOB,
    SUBJECT_REQUEST_LOGGED,
    SUBJECT_TELEMETRY_ROLLUP,
    SUBJECT_TRACE_RECORDED,
    SUBJECT_USAGE_RECORDED,
)

STREAM_USAGE = "SYNAPASS_USAGE"
STREAM_JOBS = "SYNAPASS_JOBS"
STREAM_EVENTS = "SYNAPASS_EVENTS"

# Retention windows, in seconds. Named so a change is a single visible edit.
USAGE_RETENTION_SECONDS = 7 * 24 * 60 * 60
JOB_RETENTION_SECONDS = 30 * 24 * 60 * 60
EVENT_RETENTION_SECONDS = 30 * 24 * 60 * 60

USAGE_MAX_MESSAGES = 1_000_000
EVENT_MAX_MESSAGES = 500_000
JOB_MAX_MESSAGES = 100_000

# STREAM_DEFINITIONS is consumed by Bus.ensure_streams. The keys are the ones
# required by the NATS client, so this is data rather than a call.
STREAM_DEFINITIONS: tuple[dict[str, Any], ...] = (
    {
        "name": STREAM_USAGE,
        "subjects": [SUBJECT_USAGE_RECORDED, SUBJECT_TRACE_RECORDED, SUBJECT_REQUEST_LOGGED],
        "max_age": USAGE_RETENTION_SECONDS,
        "max_msgs": USAGE_MAX_MESSAGES,
        "storage": "file",
        "retention": "limits",
        # The newest telemetry is more valuable than the oldest under a consumer
        # outage, so old messages are discarded first.
        "discard": "old",
        "replicas": 1,
    },
    {
        "name": STREAM_JOBS,
        "subjects": [SUBJECT_EVAL_JOB, SUBJECT_REPLAY_JOB],
        "max_age": JOB_RETENTION_SECONDS,
        "max_msgs": JOB_MAX_MESSAGES,
        "storage": "file",
        # Work-queue retention removes a message once any consumer acknowledges it,
        # which is what makes a job a unit of work rather than a log entry.
        "retention": "workqueue",
        "replicas": 1,
    },
    {
        "name": STREAM_EVENTS,
        "subjects": [
            SUBJECT_PROVIDER_HEALTH,
            SUBJECT_PROVIDER_STATUS_CHANGE,
            SUBJECT_AUDIT_EVENT,
            SUBJECT_TELEMETRY_ROLLUP,
            SUBJECT_PROMPT_ANALYSIS,
            SUBJECT_EVAL_RESULT,
        ],
        "max_age": EVENT_RETENTION_SECONDS,
        "max_msgs": EVENT_MAX_MESSAGES,
        "storage": "file",
        "retention": "limits",
        "discard": "old",
        "replicas": 1,
    },
)
