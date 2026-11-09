# Infrastructure

Everything this service needs, as code. Before this existed, infrastructure
was a list of copy-pasteable `aws` CLI commands in the root README, and CI
only ever deployed *code* - so every IAM or environment change was a manual
step that nothing tracked. Each stage of the API rework needs at least one
such change, which is why this landed first.

## What is and isn't managed here

Managed: the DynamoDB table and its GSI, the S3 bucket (encryption, versioning,
lifecycle, CORS), the Lambda execution role and policy, the Lambda function's
*configuration*, its Function URL, and its log group. Also the public API
reference site (`docs.tf`): a private S3 bucket behind CloudFront, and the
GitHub OIDC role that publishes to it from `main`.

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

## How changes reach AWS

Plans and applies run in CI, not on anyone's machine.

- **On a pull request**, the `plan` job posts what the change would do as a
  comment. That comment is the review: read it before merging.
- **On a merge to `main`**, the `apply` job runs the same plan and applies it,
  *before* the code deploy, so the table or permission a change needs exists
  before the code that uses it ships.

CI authenticates with GitHub OIDC rather than a stored key. Three roles exist,
and only `archive-api-ci-apply` can write: `-ci-plan` is read-only, `-ci-docs`
can publish one file, and the separate `archive-api-ci-deploy` user can only
update Lambda code.

The apply job refuses to run if a plan deletes or replaces anything, because
the table and the media bucket cannot be recovered. A change that genuinely
needs that is applied by hand, deliberately.

### The one manual apply

CI cannot create the role that gives CI permission to apply, so that role is
created by hand, once. Everything after it is automatic.

```bash
terraform -chdir=infra/terraform plan -out=tfplan
terraform -chdir=infra/terraform apply tfplan
terraform -chdir=infra/terraform output -raw apply_role_arn
```

Set that ARN as the `AWS_APPLY_ROLE_ARN` repository **variable** (Settings →
Secrets and variables → Actions → Variables). Until it exists the `apply` job
skips itself and deploys continue as before.

### Edits to `apply.tf` are always manual

The apply role is denied permission to change itself, so a change to its own
policy fails in CI by design and is applied by hand, the same way as the first
one. A role that could rewrite its own permissions would have none worth
speaking of.

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
