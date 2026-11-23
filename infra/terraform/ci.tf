# Lets GitHub Actions use this account without a long-lived access key.
#
# GitHub signs a short-lived OIDC token for each workflow run; AWS trusts that
# signature and hands back temporary credentials. Nothing is stored in GitHub
# secrets, so there is no key to leak, rotate, or forget about.
#
# One runner role, github-actions-runner, shared by every repository that uses
# this account, for plans and applies alike. It has AdministratorAccess and is
# trusted for pull requests and main in the repositories var.github_runner_repos
# lists, so whoever can push a branch to one of those repositories can change
# the account: keep that list to repositories you own.
#
# Because it can change anything, including itself, CI manages this file like
# any other: no change here needs a manual apply.

data "tls_certificate" "github" {
  url = "https://token.actions.githubusercontent.com"
}

resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github.certificates[0].sha1_fingerprint]
}

locals {
  # Each repository's subject prefix is looked up rather than built from
  # "owner/name": most use GitHub's immutable form, which embeds numeric ids.
  runner_subjects = flatten([
    for repo in var.github_runner_repos : [
      "${var.github_repo_subjects[repo]}:pull_request",
      "${var.github_repo_subjects[repo]}:ref:refs/heads/main",
    ]
  ])
}

data "aws_iam_policy_document" "github_runner_assume" {
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
      values   = local.runner_subjects
    }
  }
}

resource "aws_iam_role" "github_runner" {
  name                 = "github-actions-runner"
  description          = "Plans and applies infrastructure from GitHub Actions in this account's repositories."
  assume_role_policy   = data.aws_iam_policy_document.github_runner_assume.json
  max_session_duration = 3600
}

resource "aws_iam_role_policy_attachment" "github_runner_admin" {
  role       = aws_iam_role.github_runner.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

output "runner_role_arn" {
  description = "Set as the AWS role variables (AWS_PLAN_ROLE_ARN, AWS_APPLY_ROLE_ARN, ...) in each repository's Actions variables."
  value       = aws_iam_role.github_runner.arn
}
