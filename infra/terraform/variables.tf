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
  default     = "archive-api-816079797917"
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

variable "lambda_memory_mb" {
  description = <<-EOT
    Lambda scales CPU with memory, so this is a latency/price dial rather than
    a limit. The spine endpoint reads and aggregates a whole archive in memory
    and will eventually want more than 128; raise it when there are numbers
    that say so, not before.
  EOT
  type        = number
  default     = 128
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

    Both entries are load-bearing: the deployed frontend is the Vercel one, and
    removing it would break uploads in production while everything else kept
    working.
  EOT
  type        = list(string)
  default = [
    "http://localhost:3000",
    "https://archives-ui.vercel.app",
  ]
}

variable "github_repo_subjects" {
  description = <<-EOT
    Every repository that may assume a runner role, mapped to the front of the
    `sub` claim GitHub puts in its OIDC tokens.

    This account uses GitHub's immutable subject format, which embeds numeric
    owner and repository ids rather than names, so a plain `repo:owner/name`
    condition never matches and every assumption is refused. Read a repository's
    real value with:
      gh api repos/OWNER/REPO/actions/oidc/customization/sub
    and use its `sub_claim_prefix`.

    Listing a repository here does not by itself grant anything; it must also
    appear in one of the two lists below.
  EOT
  type        = map(string)
  default = {
    "archive-api"   = "repo:jfortner8@160181019/archive-api@1368928181"
    "archives-ui"   = "repo:jfortner8@160181019/archives-ui@1368556526"
    "alecita-y-yon" = "repo:jfortner8@160181019/alecita-y-yon@1411948881"
  }
}

variable "github_runner_read_repos" {
  description = "Repositories whose pull requests and main branch may assume github-actions-runner-read."
  type        = list(string)
  default     = ["archive-api", "archives-ui", "alecita-y-yon"]
}

variable "github_runner_apply_repos" {
  description = <<-EOT
    Repositories whose main branch may assume github-actions-runner-apply, which
    can change infrastructure. Keep this to the repositories that actually own
    Terraform: every entry is a place where a compromised main branch becomes
    write access to the account.
  EOT
  type        = list(string)
  default     = ["archive-api"]
}

variable "derive_memory_mb" {
  description = <<-EOT
    Memory for the derivative function. Decoding a 20-megapixel image to RGBA
    is around 80 MB before any resizing, and Lambda scales CPU with memory, so
    this buys speed as much as headroom. Nobody waits on these invocations,
    but a slow one costs the same per millisecond as a fast one.
  EOT
  type        = number
  default     = 1024
}
