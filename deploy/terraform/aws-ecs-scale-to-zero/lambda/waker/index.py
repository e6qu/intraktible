# SPDX-License-Identifier: AGPL-3.0-or-later
"""Wake the API service.

Invoked by API Gateway (POST /wake) from the S3-served front end while the backend is
scaled to zero. It nudges the API ECS service to at least one task and returns
immediately; the caller then polls /readyz until projections have caught up and hydrates
onto the live backend. Idempotent: waking an already-warm service is a no-op.
"""

import json
import os

import boto3

ecs = boto3.client("ecs")

CLUSTER = os.environ["CLUSTER"]
API_SERVICE = os.environ["API_SERVICE"]
WAKE_TO = int(os.environ.get("WAKE_TO", "1"))


def _running(service):
    d = ecs.describe_services(cluster=CLUSTER, services=[service])["services"][0]
    return d["desiredCount"], d["runningCount"]


def handler(event, _context):
    desired, running = _running(API_SERVICE)
    if desired < WAKE_TO:
        ecs.update_service(cluster=CLUSTER, service=API_SERVICE, desiredCount=WAKE_TO)
        desired = WAKE_TO

    body = {
        "waking": True,
        "desiredCount": desired,
        "runningCount": running,
        "poll": "/readyz",
        "message": "backend is warming; poll /readyz until 200, then hydrate",
    }
    return {
        "statusCode": 202,
        "headers": {"content-type": "application/json", "cache-control": "no-store"},
        "body": json.dumps(body),
    }
