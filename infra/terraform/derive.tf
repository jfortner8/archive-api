# The derivative pipeline: an S3 notification on originals/ invokes a second
# Lambda that renders thumbnails and records them on the item.
#
# A separate function rather than part of the API because resizing a
# 20-megapixel scan needs far more memory and time than a request handler
# should ever hold, and because it gets its own narrow role: this one can read
# originals and write derivatives, and nothing else.

data "archive_file" "derive_placeholder" {
  type        = "zip"
  output_path = "${path.module}/.terraform-tmp/derive-placeholder.zip"

  source {
    filename = "bootstrap"
    content  = "#!/bin/sh\nexit 1\n"
  }
}

resource "aws_cloudwatch_log_group" "derive" {
  name              = "/aws/lambda/${var.lambda_function_name}-derive"
  retention_in_days = var.log_retention_days
}

resource "aws_iam_role" "derive" {
  name               = "${var.lambda_function_name}-derive-exec"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

resource "aws_iam_role_policy_attachment" "derive_logs" {
  role       = aws_iam_role.derive.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "derive" {
  # Reads originals, writes derivatives, and cannot do the reverse. If this
  # function were ever made to write into originals/ it would re-trigger
  # itself, so the asymmetry is a guard rather than tidiness.
  statement {
    sid       = "ReadOriginals"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.media.arn}/originals/*"]
  }

  statement {
    sid       = "WriteDerivatives"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.media.arn}/derived/*"]
  }

  # Recording the result is a read-modify-write of one item, conditional on
  # its version. No Query: this function never lists anything.
  statement {
    sid       = "RecordOnItem"
    actions   = ["dynamodb:GetItem", "dynamodb:PutItem"]
    resources = [aws_dynamodb_table.archive.arn]
  }
}

resource "aws_iam_role_policy" "derive" {
  name   = "${var.lambda_function_name}-derive-access"
  role   = aws_iam_role.derive.id
  policy = data.aws_iam_policy_document.derive.json
}

resource "aws_lambda_function" "derive" {
  function_name = "${var.lambda_function_name}-derive"
  role          = aws_iam_role.derive.arn

  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"

  filename         = data.archive_file.derive_placeholder.output_path
  source_code_hash = data.archive_file.derive_placeholder.output_base64sha256

  # Decoding a 20-megapixel image to RGBA is about 80 MB before any resizing,
  # and Lambda scales CPU with memory - so this is as much about finishing
  # quickly as about fitting. Unlike the API, nobody is waiting on a request
  # here, but a slow function still costs the same per millisecond.
  memory_size = var.derive_memory_mb
  timeout     = 60

  environment {
    variables = {
      DYNAMODB_TABLE       = aws_dynamodb_table.archive.name
      S3_BUCKET            = aws_s3_bucket.media.bucket
      COGNITO_USER_POOL_ID = var.cognito_user_pool_id
      COGNITO_REGION       = var.aws_region

      # Unused by this function - it verifies no tokens - but config.Load
      # fails fast on a missing value, and one loader shared by both binaries
      # is worth more than a second one that differs subtly.
      API_KEY = var.api_key
    }
  }

  depends_on = [
    aws_iam_role_policy.derive,
    aws_cloudwatch_log_group.derive,
  ]

  lifecycle {
    # Code is CI's, configuration is Terraform's - same split as the API.
    ignore_changes = [filename, source_code_hash]
  }
}

resource "aws_lambda_permission" "derive_from_s3" {
  statement_id  = "AllowExecutionFromS3"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.derive.function_name
  principal     = "s3.amazonaws.com"
  source_arn    = aws_s3_bucket.media.arn
}

resource "aws_s3_bucket_notification" "media" {
  bucket = aws_s3_bucket.media.id

  lambda_function {
    lambda_function_arn = aws_lambda_function.derive.arn
    events              = ["s3:ObjectCreated:*"]

    # Scoped to originals/ so a derivative landing in the bucket cannot
    # trigger another round of derivation. The function checks the prefix
    # again for itself, because an unbounded recursion billed per
    # invocation is worth two guards.
    filter_prefix = "originals/"
  }

  depends_on = [aws_lambda_permission.derive_from_s3]
}
