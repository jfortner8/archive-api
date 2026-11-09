# Public hosting for the rendered API reference (the Redoc page built from
# openapi.yaml).
#
# It is public on purpose. The page documents endpoints and the names of the
# auth headers and contains no secret, and openapi.yaml itself is already
# readable in the public repository, so hosting it exposes nothing new.
#
# S3 behind CloudFront rather than S3's own website endpoint, because the
# website endpoint is HTTP-only and needs a public bucket. Here the bucket
# stays fully private and only this one distribution may read from it.
#
# CloudFront's always-free tier (1 TB out and 10M requests a month) is far
# beyond a docs page, so this costs effectively nothing. The address is the
# distribution's own *.cloudfront.net name; a custom domain is a later,
# optional addition.

locals {
  docs_bucket_name = "${var.lambda_function_name}-docs-${data.aws_caller_identity.current.account_id}"
}

resource "aws_s3_bucket" "docs" {
  bucket = local.docs_bucket_name
}

resource "aws_s3_bucket_public_access_block" "docs" {
  bucket = aws_s3_bucket.docs.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Origin access control is how CloudFront signs its own requests to a private
# bucket. It replaces the older origin access identity.
resource "aws_cloudfront_origin_access_control" "docs" {
  name                              = "${var.lambda_function_name}-docs"
  description                       = "Lets the docs distribution read the private docs bucket."
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

data "aws_cloudfront_cache_policy" "optimized" {
  name = "Managed-CachingOptimized"
}

resource "aws_cloudfront_distribution" "docs" {
  enabled             = true
  comment             = "archive-api reference"
  default_root_object = "index.html"

  # North America and Europe only. The cheapest tier, and plenty for a page
  # read by a handful of people.
  price_class = "PriceClass_100"

  origin {
    domain_name              = aws_s3_bucket.docs.bucket_regional_domain_name
    origin_id                = "docs"
    origin_access_control_id = aws_cloudfront_origin_access_control.docs.id
  }

  default_cache_behavior {
    target_origin_id       = "docs"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    compress               = true
    cache_policy_id        = data.aws_cloudfront_cache_policy.optimized.id
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = true
  }
}

# Only this distribution may read objects. Without the SourceArn condition any
# CloudFront distribution in any account could be pointed at the bucket.
data "aws_iam_policy_document" "docs_bucket" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.docs.arn}/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.docs.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "docs" {
  bucket = aws_s3_bucket.docs.id
  policy = data.aws_iam_policy_document.docs_bucket.json

  # The policy has to land after the public-access block, which otherwise
  # races it and rejects the policy as public.
  depends_on = [aws_s3_bucket_public_access_block.docs]
}

# The role CI assumes to publish the page, with no stored key. Unlike the
# read-only plan role in ci.tf this one can write, so it is trusted only for
# runs on main - never for pull requests, which anyone able to open one could
# otherwise use to overwrite the live page.
data "aws_iam_policy_document" "github_docs_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${var.github_repo_subjects["archive-api"]}:ref:refs/heads/main"]
    }
  }
}

resource "aws_iam_role" "github_docs" {
  name                 = "${var.lambda_function_name}-ci-docs"
  description          = "Publishes the API reference page to the docs bucket, from main only."
  assume_role_policy   = data.aws_iam_policy_document.github_docs_assume.json
  max_session_duration = 3600
}

# One object and one invalidation, nothing else. The page is a single file, so
# the role is allowed to write exactly that key.
data "aws_iam_policy_document" "github_docs_publish" {
  statement {
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.docs.arn}/index.html"]
  }

  statement {
    actions   = ["cloudfront:CreateInvalidation"]
    resources = [aws_cloudfront_distribution.docs.arn]
  }
}

resource "aws_iam_role_policy" "github_docs_publish" {
  name   = "publish-docs"
  role   = aws_iam_role.github_docs.id
  policy = data.aws_iam_policy_document.github_docs_publish.json
}

output "docs_url" {
  description = "Where the API reference is served. Linked from both READMEs."
  value       = "https://${aws_cloudfront_distribution.docs.domain_name}"
}

output "docs_bucket_name" {
  description = "Set as AWS_DOCS_BUCKET in the repository's Actions variables."
  value       = aws_s3_bucket.docs.bucket
}

output "docs_distribution_id" {
  description = "Set as AWS_DOCS_DISTRIBUTION_ID in the repository's Actions variables."
  value       = aws_cloudfront_distribution.docs.id
}

output "docs_publish_role_arn" {
  description = "Set as AWS_DOCS_ROLE_ARN in the repository's Actions variables."
  value       = aws_iam_role.github_docs.arn
}
