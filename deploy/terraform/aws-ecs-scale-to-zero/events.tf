# SPDX-License-Identifier: AGPL-3.0-or-later
#
# EventBridge drives the scale-in side of the design:
#   - reaper: every 5 minutes, scale the API service to 0 if the edge saw no traffic.
#   - sweep:  ('scheduled' scheduler_mode) on scheduler_window_cron, wake the scheduler,
#             let it run its timed sweeps, then scale it back to 0.

resource "aws_cloudwatch_event_rule" "reaper" {
  count               = var.api_always_on ? 0 : 1
  name                = "${local.name}-reaper"
  description         = "Scale the API service to zero when the edge is idle"
  schedule_expression = "rate(5 minutes)"
}

resource "aws_cloudwatch_event_target" "reaper" {
  count     = var.api_always_on ? 0 : 1
  rule      = aws_cloudwatch_event_rule.reaper[0].name
  target_id = "controller-reap"
  arn       = aws_lambda_function.controller.arn
  input     = jsonencode({ action = "reap" })
}

resource "aws_lambda_permission" "reaper" {
  count         = var.api_always_on ? 0 : 1
  statement_id  = "AllowReaperInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.controller.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.reaper[0].arn
}

resource "aws_cloudwatch_event_rule" "sweep" {
  count               = var.scheduler_mode == "scheduled" ? 1 : 0
  name                = "${local.name}-sweep"
  description         = "Wake the scheduler on a cron to run a timed-sweep window"
  schedule_expression = var.scheduler_window_cron
}

resource "aws_cloudwatch_event_target" "sweep" {
  count     = var.scheduler_mode == "scheduled" ? 1 : 0
  rule      = aws_cloudwatch_event_rule.sweep[0].name
  target_id = "controller-sweep"
  arn       = aws_lambda_function.controller.arn
  input     = jsonencode({ action = "sweep" })
}

resource "aws_lambda_permission" "sweep" {
  count         = var.scheduler_mode == "scheduled" ? 1 : 0
  statement_id  = "AllowSweepInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.controller.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.sweep[0].arn
}
