variable "aws_region" {
  description = "Region every resource here lives in."
  type        = string
  default     = "us-east-1"
}

variable "name_prefix" {
  description = <<-EOT
    Prefix for every resource name, so a second environment can be stood up
    from this same configuration without collisions. The existing hand-created
    resources use the bare "archive-api"/"archive-prod" names, so changing this
    means importing nothing and creating everything fresh.
  EOT
  type        = string
  default     = "archive"
}

variable "lambda_function_name" {
  description = "Name of the API Lambda. Must stay in sync with .github/workflows/ci.yml."
  type        = string
  default     = "archive-api"
}

variable "s3_bucket_name" {
  description = "Bucket holding originals and derivatives. Globally unique."
  type        = string
  default     = "archive-prod"
}

variable "dynamodb_table_name" {
  description = <<-EOT
    The single-table store. This is deliberately NOT the old "archive-items"
    table: the key schema changed from a bare `id` partition key to pk/sk, and
    DynamoDB cannot alter a key schema in place. Pointing at a new table lets
    the old one stay untouched until it is confirmed disposable.
  EOT
  type        = string
  default     = "archive"
}

variable "cognito_user_pool_id" {
  description = <<-EOT
    Existing User Pool the API verifies access tokens against. Created by hand
    (see README) and referenced here as a data source rather than managed, so
    a `terraform destroy` can never take the user directory with it.
  EOT
  type        = string
  default     = "us-east-1_DwtgmBBy3"
}

variable "cognito_app_client_id" {
  description = "Optional: reject tokens not issued to this App Client. Empty disables the check."
  type        = string
  default     = "5f8dpqoustu5u13ks81mf2mdne"
}

variable "api_key" {
  description = <<-EOT
    Value the X-API-Key header must match. Supplied via TF_VAR_api_key or a
    tfvars file that is never committed; it ends up in the Lambda's environment
    and therefore in state, which is the reason the state backend is encrypted.
  EOT
  type        = string
  sensitive   = true
}

variable "log_retention_days" {
  description = "CloudWatch Logs retention. Lambda's default is never-expire, which quietly accrues cost."
  type        = number
  default     = 30
}

variable "originals_transition_days" {
  description = <<-EOT
    Days before an original moves to Glacier Instant Retrieval. Originals are
    written once and re-read rarely (derivatives serve every view), so this is
    the main storage lever once video and scans arrive. Derivatives stay in
    Standard. Set to 0 to disable the transition entirely.
  EOT
  type        = number
  default     = 90
}

variable "media_cors_origins" {
  description = <<-EOT
    Origins allowed to PUT directly to presigned S3 URLs - i.e. wherever
    archives-ui runs in the browser. The API itself needs no CORS (it is only
    ever reached server-side through the Next proxy), but these uploads skip
    the proxy entirely.
  EOT
  type        = list(string)
  default     = ["http://localhost:3000"]
}
