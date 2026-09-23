# Infrastructure

Everything this service needs, as code. Before this existed, infrastructure
was a list of copy-pasteable `aws` CLI commands in the root README, and CI
only ever deployed *code* - so every IAM or environment change was a manual
step that nothing tracked. Each stage of the API rework needs at least one
such change, which is why this landed first.

## What is and isn't managed here

Managed: the DynamoDB table and its GSI, the S3 bucket (encryption, versioning,
lifecycle, CORS), the Lambda execution role and policy, the Lambda function's
*configuration*, its Function URL, and its log group.

Deliberately not managed:

- **The Cognito User Pool.** Referenced by id only. Destroying a user
  directory because of a bad `terraform destroy` is unrecoverable in a way
  nothing else here is.
- **The Lambda's code.** CI owns it via `aws lambda update-function-code`;
  `lifecycle.ignore_changes` keeps Terraform from reverting production to the
  placeholder zip on the next apply.
- **The old `archive-items` table.** The key schema changed from a bare `id`
  partition key to `pk`/`sk`, and DynamoDB cannot alter a key schema in place,
  so this creates a *new* table and leaves the old one alone until you are
  certain it is disposable.

## First run

```bash
cd infra/terraform
export TF_VAR_api_key='<the value X-API-Key must match>'
terraform init
terraform plan
```

`api_key` is the only required variable. Pass it via `TF_VAR_api_key` as above,
or a `terraform.tfvars` that is gitignored - it reaches the Lambda's
environment and therefore state, which is why the S3 backend in `versions.tf`
is encrypted.

## Adopting the resources that already exist

The bucket, function, role, and Function URL were created by hand. Import them
rather than letting Terraform try to create duplicates - check `terraform plan`
after each import and expect only configuration drift, never a replacement:

```bash
terraform import aws_s3_bucket.media archive-prod
terraform import aws_iam_role.api_lambda archive-api-exec
terraform import aws_lambda_function.api archive-api
terraform import aws_cloudwatch_log_group.api /aws/lambda/archive-api
terraform import aws_lambda_function_url.api archive-api
```

If `plan` proposes to *replace* rather than update something, stop and
reconcile the configuration to match reality first. The two resources carrying
`prevent_destroy` (the table and the bucket) will refuse outright, which is the
intent.

`aws_dynamodb_table.archive` is genuinely new - do not import anything for it.

## Caveats

- **Not yet validated or applied.** Terraform was not installed on the machine
  this was written on, so `terraform validate` and `terraform plan` have not
  been run against it. Treat the first `plan` as a review step, not a
  formality.
- **`make dev-setup` duplicates the schema.** The local DynamoDB Local table in
  the root `Makefile` has to match the key schema and GSI defined here. There
  is no mechanism keeping them in sync, so change both together and say so in
  review.
