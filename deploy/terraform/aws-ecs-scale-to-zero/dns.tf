# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Managed-DNS mode (var.route53_zone_id set): the module owns its public DNS, matching the
# convention of the other e6qu service modules — it creates the CloudFront viewer
# certificate (ACM in us-east-1, DNS-validated in the given hosted zone) and the alias
# A/AAAA records pointing the domain at the distribution. The hosted zone itself is NOT
# created here; it is referenced by ID and must already be delegated (its NS records live
# at the parent) before apply, or ACM validation blocks until it times out.
#
# When route53_zone_id is empty the resources below are absent and the distribution uses
# either the default *.cloudfront.net cert or a bring-your-own acm_certificate_arn.

resource "aws_acm_certificate" "cdn" {
  count             = local.manage_dns ? 1 : 0
  provider          = aws.us_east_1 # CloudFront certs must live in us-east-1
  domain_name       = var.domain_name
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "cert_validation" {
  for_each = local.manage_dns ? {
    for o in aws_acm_certificate.cdn[0].domain_validation_options : o.domain_name => o
  } : {}

  zone_id         = var.route53_zone_id
  name            = each.value.resource_record_name
  type            = each.value.resource_record_type
  records         = [each.value.resource_record_value]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "cdn" {
  count                   = local.manage_dns ? 1 : 0
  provider                = aws.us_east_1
  certificate_arn         = aws_acm_certificate.cdn[0].arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation : r.fqdn]
}

# Point the domain at the distribution (both address families).
resource "aws_route53_record" "alias_a" {
  count   = local.manage_dns ? 1 : 0
  zone_id = var.route53_zone_id
  name    = var.domain_name
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "alias_aaaa" {
  count   = local.manage_dns ? 1 : 0
  zone_id = var.route53_zone_id
  name    = var.domain_name
  type    = "AAAA"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}
