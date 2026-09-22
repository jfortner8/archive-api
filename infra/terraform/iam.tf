data "aws_caller_identity" "current" {}

data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "api_lambda" {
  name               = "${var.lambda_function_name}-exec"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

resource "aws_iam_role_policy_attachment" "api_lambda_logs" {
  role       = aws_iam_role.api_lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "api_lambda" {
  # Note what is absent: dynamodb:Scan. The old handler Scanned the whole
  # table and filtered by account in Go; the new one Queries GSI1 with the
  # archive as the partition key. Leaving Scan out means that regression
  # fails loudly in staging instead of quietly costing money in production.
  statement {
    sid = "TableAccess"

    actions = [
      "dynamodb:GetItem",
      "dynamodb:BatchGetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:BatchWriteItem",
      "dynamodb:Query",
      "dynamodb:ConditionCheckItem",
      "dynamodb:TransactWriteItems",
      "dynamodb:TransactGetItems",
    ]

    resources = [aws_dynamodb_table.archive.arn]
  }

  # Querying a GSI is authorized against the INDEX arn, not the table arn.
  # Granting only the table arn is the classic way to get an
  # AccessDeniedException that reads as though the policy is already correct.
  statement {
    sid       = "IndexAccess"
    actions   = ["dynamodb:Query"]
    resources = ["${aws_dynamodb_table.archive.arn}/index/*"]
  }

  statement {
    sid = "ObjectAccess"

    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
    ]

    resources = ["${aws_s3_bucket.media.arn}/*"]
  }

  # Needed to sweep an item's objects by prefix when the item is deleted.
  statement {
    sid       = "BucketListing"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.media.arn]
  }
}

resource "aws_iam_role_policy" "api_lambda" {
  name   = "${var.lambda_function_name}-access"
  role   = aws_iam_role.api_lambda.id
  policy = data.aws_iam_policy_document.api_lambda.json
}
