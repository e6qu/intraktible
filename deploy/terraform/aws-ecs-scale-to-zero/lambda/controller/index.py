# SPDX-License-Identifier: AGPL-3.0-or-later
"""Scale-to-zero controller, invoked by EventBridge with an {"action": ...} payload.

  action=reap  : if the edge (API Gateway) has seen no requests for IDLE_MINUTES, scale
                 the API service back to 0. Runs on a short EventBridge rate schedule.
  action=sweep : wake the singleton scheduler service, let it run its timed sweeps for
                 SCHEDULER_RUN_MINUTES, then scale it back to 0 — so Aurora can pause
                 between windows instead of being held awake by an always-on scheduler.
                 Runs on the scheduler_window_cron schedule ('scheduled' mode only).
"""

import os
import time
from datetime import datetime, timedelta, timezone

import boto3

ecs = boto3.client("ecs")
cw = boto3.client("cloudwatch")

CLUSTER = os.environ["CLUSTER"]
API_SERVICE = os.environ["API_SERVICE"]
SCHEDULER_SERVICE = os.environ["SCHEDULER_SERVICE"]
API_ID = os.environ["API_ID"]
API_STAGE = os.environ.get("API_STAGE", "$default")
IDLE_MINUTES = int(os.environ.get("IDLE_MINUTES", "20"))
SCHEDULER_RUN_MINUTES = int(os.environ.get("SCHEDULER_RUN_MINUTES", "5"))


def _desired(service):
    return ecs.describe_services(cluster=CLUSTER, services=[service])["services"][0]["desiredCount"]


def _scale(service, count):
    ecs.update_service(cluster=CLUSTER, service=service, desiredCount=count)


def _edge_request_count(minutes):
    """Total API Gateway requests over the trailing window (0 when the API is idle)."""
    end = datetime.now(timezone.utc)
    start = end - timedelta(minutes=minutes)
    resp = cw.get_metric_statistics(
        Namespace="AWS/ApiGateway",
        MetricName="Count",
        Dimensions=[
            {"Name": "ApiId", "Value": API_ID},
            {"Name": "Stage", "Value": API_STAGE},
        ],
        StartTime=start,
        EndTime=end,
        Period=max(60, minutes * 60),
        Statistics=["Sum"],
    )
    return sum(point["Sum"] for point in resp["Datapoints"])


def _reap():
    if _desired(API_SERVICE) == 0:
        return {"action": "reap", "state": "already-zero"}
    requests = _edge_request_count(IDLE_MINUTES)
    if requests > 0:
        return {"action": "reap", "state": "busy", "requests": requests}
    _scale(API_SERVICE, 0)
    return {"action": "reap", "state": "scaled-to-zero"}


def _sweep():
    _scale(SCHEDULER_SERVICE, 1)
    time.sleep(SCHEDULER_RUN_MINUTES * 60)
    _scale(SCHEDULER_SERVICE, 0)
    return {"action": "sweep", "state": "completed", "ran_minutes": SCHEDULER_RUN_MINUTES}


def handler(event, _context):
    action = (event or {}).get("action")
    if action == "reap":
        return _reap()
    if action == "sweep":
        return _sweep()
    raise ValueError(f"unknown action: {action!r}")
