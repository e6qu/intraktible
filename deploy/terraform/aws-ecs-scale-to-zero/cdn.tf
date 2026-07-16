# SPDX-License-Identifier: AGPL-3.0-or-later
#
# S3 holds the "dehydrated" site: the adapter-static SvelteKit build plus the wasm
# engine. It is always available and costs ~nothing, so with the backend scaled to zero
# the app still loads and runs the in-browser engine. CloudFront fronts BOTH the S3 static
# origin (default) and the API Gateway origin (/v1, /healthz, /readyz, /wake) under ONE
# domain, so the browser sees a single origin — the web client only ever calls relative
# /v1 paths (no configurable API base URL), so same-origin is what makes the split work
# without any app/CORS change.

resource "aws_s3_bucket" "site" {
  bucket_prefix = "${local.name}-site-"
  force_destroy = false
}

resource "aws_s3_bucket_public_access_block" "site" {
  bucket                  = aws_s3_bucket.site.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "site" {
  bucket = aws_s3_bucket.site.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_cloudfront_function" "spa_routing" {
  name    = "${local.name}-spa-routing"
  runtime = "cloudfront-js-2.0"
  comment = "Rewrite extensionless (SPA) paths to /index.html on the static behavior only"
  publish = true
  code    = file("${path.module}/cloudfront-spa-routing.js")
}

resource "aws_cloudfront_origin_access_control" "site" {
  name                              = "${local.name}-site"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

data "aws_cloudfront_cache_policy" "caching_optimized" {
  name = "Managed-CachingOptimized"
}

data "aws_cloudfront_cache_policy" "caching_disabled" {
  name = "Managed-CachingDisabled"
}

data "aws_cloudfront_origin_request_policy" "all_viewer_except_host" {
  name = "Managed-AllViewerExceptHostHeader"
}

locals {
  s3_origin_id  = "s3-site"
  api_origin_id = "api-gateway"
  api_domain    = "${aws_apigatewayv2_api.this.id}.execute-api.${var.region}.amazonaws.com"

  # Dynamic paths routed to the backend rather than the static bucket.
  api_path_patterns = ["/v1/*", "/healthz", "/readyz", "/wake"]
}

resource "aws_cloudfront_distribution" "this" {
  enabled             = true
  comment             = local.name
  default_root_object = "index.html"
  price_class         = "PriceClass_100"
  aliases             = local.use_custom_domain ? [var.domain_name] : []

  origin {
    origin_id                = local.s3_origin_id
    domain_name              = aws_s3_bucket.site.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.site.id
  }

  origin {
    origin_id   = local.api_origin_id
    domain_name = local.api_domain
    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  # The default is the static S3 site. Always-on deployments can instead route all UI
  # requests to the production binary's embedded UI, avoiding a separate asset upload.
  default_cache_behavior {
    target_origin_id         = var.serve_embedded_ui_from_api ? local.api_origin_id : local.s3_origin_id
    viewer_protocol_policy   = "redirect-to-https"
    allowed_methods          = ["GET", "HEAD", "OPTIONS"]
    cached_methods           = ["GET", "HEAD"]
    compress                 = true
    cache_policy_id          = var.serve_embedded_ui_from_api ? data.aws_cloudfront_cache_policy.caching_disabled.id : data.aws_cloudfront_cache_policy.caching_optimized.id
    origin_request_policy_id = var.serve_embedded_ui_from_api ? data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id : null

    dynamic "function_association" {
      for_each = var.serve_embedded_ui_from_api ? [] : [true]
      content {
        event_type   = "viewer-request"
        function_arn = aws_cloudfront_function.spa_routing.arn
      }
    }
  }

  # Dynamic API paths: no caching, forward everything (except Host) to the backend.
  dynamic "ordered_cache_behavior" {
    for_each = local.api_path_patterns
    content {
      path_pattern             = ordered_cache_behavior.value
      target_origin_id         = local.api_origin_id
      viewer_protocol_policy   = "redirect-to-https"
      allowed_methods          = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
      cached_methods           = ["GET", "HEAD"]
      compress                 = true
      cache_policy_id          = data.aws_cloudfront_cache_policy.caching_disabled.id
      origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = local.use_custom_domain ? null : true
    # Managed-DNS mode uses the cert this module creates+validates; otherwise the BYO ARN.
    # Referencing the *validation* resource makes CloudFront wait for a validated cert.
    acm_certificate_arn      = local.manage_dns ? aws_acm_certificate_validation.cdn[0].certificate_arn : (local.use_custom_domain ? var.acm_certificate_arn : null)
    ssl_support_method       = local.use_custom_domain ? "sni-only" : null
    minimum_protocol_version = local.use_custom_domain ? "TLSv1.2_2021" : null
  }
}

# Restrict the bucket to this distribution via Origin Access Control.
data "aws_iam_policy_document" "site_bucket" {
  statement {
    sid       = "AllowCloudFrontOAC"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.site.arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "site" {
  bucket = aws_s3_bucket.site.id
  policy = data.aws_iam_policy_document.site_bucket.json
}
