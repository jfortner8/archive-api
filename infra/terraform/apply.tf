# The role CI assumes to run `terraform apply` on merges to main.
#
# This replaces a human typing apply, so it is the most powerful identity in
# the account's CI and is built to be as narrow as a role that can write IAM
# can be made. What it does and does not guard against:
#
#   - Trusted for runs on main only, never pull requests. Whoever can push to
#     main can already run any workflow, so the real boundary is who may push
#     there; protect the branch.
#   - Every write is scoped to this project's own resources by name prefix. It
#     cannot touch another bucket, table, function or role in the account.
#   - It can attach only the two AWS-managed policies this configuration uses,
#     so it cannot attach AdministratorAccess to anything.
#   - It cannot change itself. Edits to this file are applied by a human, by
#     design: a role that can rewrite its own permissions has none worth
#     speaking of.
#
# What it does not stop: it can still write an inline policy onto the other
# roles in this project's prefix, which is how a determined misuse of IAM write
# would widen its reach. That is inherent to any role that can apply IAM
# changes, and is the cost of not applying by hand.

data "aws_iam_policy_document" "github_apply_assume" {
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
      values   = ["${var.github_oidc_subject_prefix}:ref:refs/heads/main"]
    }
  }
}

resource "aws_iam_role" "github_apply" {
  name                 = "${var.lambda_function_name}-ci-apply"
  description          = "Runs `terraform apply` on merges to main. Cannot modify itself."
  assume_role_policy   = data.aws_iam_policy_document.github_apply_assume.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "github_apply" {
  # The state bucket, the media bucket and the docs bucket all share the
  # project prefix. Terraform reads and writes bucket configuration across
  # dozens of separate actions, so this is a wildcard on the service but still
  # confined to buckets named for this project.
  statement {
    sid     = "ProjectBuckets"
    actions = ["s3:*"]
    resources = [
      "arn:aws:s3:::${var.lambda_function_name}-*",
      "arn:aws:s3:::${var.lambda_function_name}-*/*",
    ]
  }

  statement {
    sid     = "ProjectTable"
    actions = ["dynamodb:*"]
    resources = [
      "arn:aws:dynamodb:${var.aws_region}:${data.aws_caller_identity.current.account_id}:table/${var.dynamodb_table_name}",
      "arn:aws:dynamodb:${var.aws_region}:${data.aws_caller_identity.current.account_id}:table/${var.dynamodb_table_name}/*",
    ]
  }

  # The provider asks for account-level limits while planning a table.
  statement {
    sid       = "DynamoDBAccountLimits"
    actions   = ["dynamodb:DescribeLimits"]
    resources = ["*"]
  }

  # Configuration only. Function *code* is deployed by a separate, narrower
  # identity, and lifecycle.ignore_changes stops Terraform fighting it.
  statement {
    sid     = "ProjectFunctions"
    actions = ["lambda:*"]
    resources = [
      "arn:aws:lambda:${var.aws_region}:${data.aws_caller_identity.current.account_id}:function:${var.lambda_function_name}",
      "arn:aws:lambda:${var.aws_region}:${data.aws_caller_identity.current.account_id}:function:${var.lambda_function_name}-*",
    ]
  }

  statement {
    sid     = "ProjectLogGroups"
    actions = ["logs:*"]
    resources = [
      "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/${var.lambda_function_name}",
      "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/${var.lambda_function_name}:*",
      "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/${var.lambda_function_name}-*",
      "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/${var.lambda_function_name}-*:*",
    ]
  }

  statement {
    sid       = "ListLogGroups"
    actions   = ["logs:DescribeLogGroups"]
    resources = ["*"]
  }

  # CloudFront is used for the docs site and nothing else, and several of its
  # read calls (listing cache policies) do not accept a resource.
  statement {
    sid       = "DocsDistribution"
    actions   = ["cloudfront:*"]
    resources = ["*"]
  }

  statement {
    sid = "ProjectRoles"
    actions = [
      "iam:CreateRole",
      "iam:DeleteRole",
      "iam:GetRole",
      "iam:UpdateRole",
      "iam:UpdateAssumeRolePolicy",
      "iam:TagRole",
      "iam:UntagRole",
      "iam:ListRoleTags",
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
      "iam:GetRolePolicy",
      "iam:ListRolePolicies",
      "iam:ListAttachedRolePolicies",
      "iam:ListInstanceProfilesForRole",
    ]
    resources = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.lambda_function_name}-*"]
  }

  # Pinned to the two managed policies this configuration attaches. Anything
  # else - AdministratorAccess in particular - is refused.
  statement {
    sid     = "AttachOnlyKnownPolicies"
    actions = ["iam:AttachRolePolicy", "iam:DetachRolePolicy"]
    resources = [
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.lambda_function_name}-*",
    ]

    condition {
      test     = "ArnEquals"
      variable = "iam:PolicyARN"
      values = [
        "arn:aws:iam::aws:policy/ReadOnlyAccess",
        "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole",
      ]
    }
  }

  # Needed to hand the execution roles to the two functions, and only to
  # Lambda.
  statement {
    sid     = "PassExecutionRoles"
    actions = ["iam:PassRole"]
    resources = [
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.lambda_function_name}-lambda-role",
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.lambda_function_name}-derive-exec",
    ]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["lambda.amazonaws.com"]
    }
  }

  # The GitHub identity provider is shared by all three CI roles, so it can be
  # inspected and re-thumbprinted but not deleted.
  statement {
    sid = "GitHubProvider"
    actions = [
      "iam:GetOpenIDConnectProvider",
      "iam:UpdateOpenIDConnectProviderThumbprint",
      "iam:TagOpenIDConnectProvider",
      "iam:UntagOpenIDConnectProvider",
      "iam:ListOpenIDConnectProviderTags",
    ]
    resources = [aws_iam_openid_connect_provider.github.arn]
  }

  # Refresh needs to read the AWS-managed policy documents it attaches.
  statement {
    sid       = "ReadManagedPolicies"
    actions   = ["iam:GetPolicy", "iam:GetPolicyVersion", "iam:ListPolicyVersions"]
    resources = ["arn:aws:iam::aws:policy/*"]
  }

  # Evaluated after the allows above, so it wins. Without it the broad role
  # permission would let this role rewrite its own permissions.
  statement {
    sid    = "CannotModifyItself"
    effect = "Deny"
    actions = [
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
      "iam:UpdateAssumeRolePolicy",
      "iam:UpdateRole",
      "iam:DeleteRole",
      "iam:TagRole",
      "iam:UntagRole",
      "iam:PutRolePermissionsBoundary",
      "iam:DeleteRolePermissionsBoundary",
    ]
    resources = [aws_iam_role.github_apply.arn]
  }
}

resource "aws_iam_role_policy" "github_apply" {
  name   = "apply-terraform"
  role   = aws_iam_role.github_apply.id
  policy = data.aws_iam_policy_document.github_apply.json
}

output "apply_role_arn" {
  description = "Set as AWS_APPLY_ROLE_ARN in the repository's Actions variables. Until it exists CI skips the apply job."
  value       = aws_iam_role.github_apply.arn
}
