# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Secrets are generated here and injected into the task at runtime from Secrets Manager
# (never baked into the image or task-definition environment in plaintext). NOTE: because
# Terraform generates them, the values land in Terraform state — use an encrypted remote
# backend (e.g. S3 + SSE-KMS with state locking) and restrict access to it. To keep a
# secret entirely out of state instead, create the Secrets Manager secret out-of-band and
# set its value with a null_resource/aws CLI, or rotate immediately after first apply.

# Encryption key for PII / event payloads at rest (INTRAKTIBLE_ENCRYPTION_KEY). The
# backend's production preflight refuses to start without it.
resource "random_password" "encryption_key" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "encryption_key" {
  name_prefix             = "${local.name}/encryption-key-"
  description             = "intraktible at-rest encryption key"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "encryption_key" {
  secret_id     = aws_secretsmanager_secret.encryption_key.id
  secret_string = base64encode(random_password.encryption_key.result)
}

# The first admin credential (INTRAKTIBLE_BOOTSTRAP_API_KEY). Use it to mint managed
# keys / SSO users, then rotate to a managed key and remove this from the task env.
resource "random_password" "bootstrap_api_key" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "bootstrap_api_key" {
  name_prefix             = "${local.name}/bootstrap-api-key-"
  description             = "intraktible bootstrap admin API key (rotate after first use)"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "bootstrap_api_key" {
  secret_id     = aws_secretsmanager_secret.bootstrap_api_key.id
  secret_string = random_password.bootstrap_api_key.result
}

# Aurora master password + the composed Postgres DSN the backend consumes
# (INTRAKTIBLE_POSTGRES_DSN). The DSN points at the Aurora cluster endpoint directly: the
# API service is capped at a handful of tasks, so a connection pooler (RDS Proxy) — which
# is itself always-on and would not scale to zero — is not warranted here. Add one if task
# fan-out grows (see README).
resource "random_password" "db" {
  length  = 40
  special = false # keep the DSN URL-safe without percent-encoding
}

resource "aws_secretsmanager_secret" "db_password" {
  name_prefix             = "${local.name}/db-password-"
  description             = "intraktible Aurora master password"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "db_password" {
  secret_id     = aws_secretsmanager_secret.db_password.id
  secret_string = random_password.db.result
}

resource "aws_secretsmanager_secret" "db_dsn" {
  name_prefix             = "${local.name}/db-dsn-"
  description             = "intraktible Postgres DSN (via RDS Proxy)"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "db_dsn" {
  secret_id = aws_secretsmanager_secret.db_dsn.id
  secret_string = format(
    "postgres://%s:%s@%s:5432/%s?sslmode=require",
    local.db_username,
    random_password.db.result,
    aws_rds_cluster.this.endpoint,
    local.db_name,
  )
}
