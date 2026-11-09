# Lets GitHub Actions use this account without a long-lived access key.
#
# GitHub signs a short-lived OIDC token for each workflow run; AWS trusts that
# signature and hands back temporary credentials. Nothing is stored in GitHub
# secrets, so there is no key to leak, rotate, or forget about. The existing
# deploy user still uses an access key - worth moving to this same mechanism
# eventually, but that is a separate change.
#
# Two general-purpose runner roles live here and in apply.tf, shared by every
# repository that uses this account, rather than one role per repository:
#
#   github-actions-runner-read    plan and inspect. Pull requests and main.
#   github-actions-runner-apply   apply changes. main only.
#
# They are separate on purpose. A pull request runs the workflow file from its
# own branch, so a role that could write and was trusted for pull requests would
# let anyone able to push a branch rewrite the workflow and apply whatever they
# liked, with no review. Read can be offered to a pull request; write cannot.
#
# Which repositories may use each is a list in variables.tf, so onboarding
# another repository is one line there.

data "tls_certificate" "github" {
  url = "https://token.actions.githubusercontent.com"
}

resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github.certificates[0].sha1_fingerprint]
}

locals {
  # GitHub's subject claim for this account is the immutable form, which embeds
  # numeric owner and repository ids instead of names, so each repository's
  # prefix is looked up rather than built from "owner/name".
  runner_read_subjects = flatten([
    for repo in var.github_runner_read_repos : [
      "${var.github_repo_subjects[repo]}:pull_request",
      "${var.github_repo_subjects[repo]}:ref:refs/heads/main",
    ]
  ])

  runner_apply_subjects = [
    for repo in var.github_runner_apply_repos :
    "${var.github_repo_subjects[repo]}:ref:refs/heads/main"
  ]
}

data "aws_iam_policy_document" "github_runner_read_assume" {
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

    # Pull requests as well as main. This admits a pull request opened from a
    # branch of any trusted repository, which is acceptable only because the
    # role below can do nothing but read - tighten it with an environment
    # approval if that ever stops being true.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = local.runner_read_subjects
    }
  }
}

resource "aws_iam_role" "runner_read" {
  name                 = "github-actions-runner-read"
  description          = "Read-only, for plans and inspection from GitHub Actions in trusted repositories."
  assume_role_policy   = data.aws_iam_policy_document.github_runner_read_assume.json
  max_session_duration = 3600
}

# AWS's own ReadOnlyAccess rather than a hand-written list of permissions.
#
# The trade-off is deliberate: a narrow policy would be tighter, but a plan
# touches a long tail of describe/list calls across many services and getting
# one wrong means a failing pipeline and a round of permission
# whack-a-mole. This grants broad read and zero write, which for a single-owner
# account is the right side of that trade. Swap it for a scoped policy if the
# account ever holds something that must not be readable.
resource "aws_iam_role_policy_attachment" "runner_read_only" {
  role       = aws_iam_role.runner_read.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

output "runner_read_role_arn" {
  description = "Set as AWS_PLAN_ROLE_ARN in each repository's Actions variables."
  value       = aws_iam_role.runner_read.arn
}
