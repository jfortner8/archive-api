# A placeholder so `terraform apply` can create the function before any real
# build exists. Every subsequent deploy is CI's `aws lambda
# update-function-code`, which is why `filename`/`source_code_hash` are
# ignored below - otherwise Terraform would revert production to this stub on
# the next apply.
data "archive_file" "placeholder" {
  type        = "zip"
  output_path = "${path.module}/.terraform-tmp/placeholder.zip"

  source {
    filename = "bootstrap"
    content  = "#!/bin/sh\nexit 1\n"
  }
}

resource "aws_cloudwatch_log_group" "api" {
  name              = "/aws/lambda/${var.lambda_function_name}"
  retention_in_days = var.log_retention_days
}

resource "aws_lambda_function" "api" {
  function_name = var.lambda_function_name
  role          = aws_iam_role.api_lambda.arn

  # provided.al2023 + arm64 (Graviton): cheaper per ms than x86, and the Go
  # binary is cross-compiled for it in the Makefile and in CI.
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"

  filename         = data.archive_file.placeholder.output_path
  source_code_hash = data.archive_file.placeholder.output_base64sha256

  timeout = 15

  # The spine endpoint reads and aggregates an entire archive in memory, and
  # Lambda scales CPU with memory - 512 MB is roughly 4x the CPU of 128 MB for
  # 4x the per-ms price, which is usually a wash on cost and a large win on
  # latency for this shape of work. Revisit with real numbers once /items/index
  # is serving traffic.
  memory_size = 512

  environment {
    variables = {
      DYNAMODB_TABLE        = aws_dynamodb_table.archive.name
      S3_BUCKET             = aws_s3_bucket.media.bucket
      API_KEY               = var.api_key
      COGNITO_USER_POOL_ID  = var.cognito_user_pool_id
      COGNITO_REGION        = var.aws_region
      COGNITO_APP_CLIENT_ID = var.cognito_app_client_id
    }
  }

  depends_on = [
    aws_iam_role_policy.api_lambda,
    aws_cloudwatch_log_group.api,
  ]

  lifecycle {
    # Code is CI's to own; configuration is Terraform's. Splitting them this
    # way is what lets every push to main deploy without a Terraform run,
    # while IAM and env vars stop being a manual `update-function-configuration`
    # step that nothing tracks.
    ignore_changes = [filename, source_code_hash]
  }
}

# AWS_IAM auth means every request must carry a valid SigV4 signature or AWS
# rejects it before the Go code runs. A browser cannot produce one, which is
# why archives-ui signs through a server-side proxy holding a narrow IAM
# credential.
#
# No CORS block: the browser never calls this origin directly. (The S3 bucket
# does need CORS, because uploads bypass the proxy - see s3.tf.)
resource "aws_lambda_function_url" "api" {
  function_name      = aws_lambda_function.api.function_name
  authorization_type = "AWS_IAM"
}
