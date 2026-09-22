output "function_url" {
  description = "Base URL for the API. Set as ARCHIVE_API_URL in archives-ui."
  value       = aws_lambda_function_url.api.function_url
}

output "dynamodb_table_name" {
  value = aws_dynamodb_table.archive.name
}

output "s3_bucket_name" {
  value = aws_s3_bucket.media.bucket
}

output "lambda_role_arn" {
  value = aws_iam_role.api_lambda.arn
}
