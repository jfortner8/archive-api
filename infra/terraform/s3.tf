resource "aws_s3_bucket" "media" {
  bucket = var.s3_bucket_name

  lifecycle {
    prevent_destroy = true
  }
}

# Nothing in this bucket is ever served publicly. Reads go through presigned
# URLs the API mints; a future CloudFront distribution would use OAC, which
# also does not need public access.
resource "aws_s3_bucket_public_access_block" "media" {
  bucket                  = aws_s3_bucket.media.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

# Versioning is the S3 half of "an archive keeps what you put in it": it makes
# an accidental overwrite or delete recoverable. Note the interaction with the
# key scheme - keys embed an immutable fileId, so ordinary edits never
# overwrite an object and versions accumulate only on genuine mistakes.
resource "aws_s3_bucket_versioning" "media" {
  bucket = aws_s3_bucket.media.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  # Browsers and SDKs abandon multipart uploads all the time, and the orphaned
  # parts are billed until something cleans them up. This matters as soon as
  # video slots start using multipart.
  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  # Originals are written once and re-read rarely, because every view is served
  # from a derivative. Glacier Instant Retrieval keeps millisecond reads at a
  # fraction of Standard's storage price.
  #
  # Deliberately scoped to originals/ only: Glacier IR bills a 128 KB minimum
  # per object plus a retrieval charge, so applying it to thumbnails would cost
  # more than it saves.
  dynamic "rule" {
    for_each = var.originals_transition_days > 0 ? [1] : []

    content {
      id     = "originals-to-glacier-ir"
      status = "Enabled"

      filter {
        prefix = "originals/"
      }

      transition {
        days          = var.originals_transition_days
        storage_class = "GLACIER_IR"
      }
    }
  }

  # Keep a bad overwrite recoverable for a month, then stop paying for it.
  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }
}

# The browser PUTs file bytes straight to a presigned S3 URL, bypassing both
# the Next proxy and this API - so unlike the API itself (which is only ever
# called server-side and needs no CORS at all), the bucket does need it.
#
# ETag is exposed because multipart completion needs the per-part ETags.
resource "aws_s3_bucket_cors_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  cors_rule {
    allowed_methods = ["PUT", "GET", "HEAD"]
    allowed_origins = var.media_cors_origins
    allowed_headers = ["*"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3000
  }
}
