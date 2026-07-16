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

resource "aws_secretsmanager_secret" "oidc_client" {
  count                   = var.oidc_provider_name == "" ? 0 : 1
  name_prefix             = "${local.name}/oidc-client-"
  description             = "intraktible Shauth OpenID Connect client secret"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "oidc_client" {
  count         = var.oidc_provider_name == "" ? 0 : 1
  secret_id     = aws_secretsmanager_secret.oidc_client[0].id
  secret_string = var.oidc_client_secret
}
