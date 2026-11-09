# Lets GitHub Actions read this account without a long-lived access key.
#
# GitHub signs a short-lived OIDC token for each workflow run; AWS trusts that
# signature and hands back temporary credentials. Nothing is stored in GitHub
# secrets, so there is no key to leak, rotate, or forget about. The existing
# deploy user still uses an access key - worth moving to this same mechanism
# eventually, but that is a separate change.

data "tls_certificate" "github" {
  url = "https://token.actions.githubusercontent.com"
}

resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github.certificates[0].sha1_fingerprint]
}

data "aws_iam_policy_document" "github_plan_assume" {
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

    # Scoped to pull requests on this one repository. Note this does admit a
    # pull request opened from a fork, which is acceptable only because the
    # role below can do nothing but read - tighten it with an environment
    # approval if that ever stops being true.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "${var.github_oidc_subject_prefix}:pull_request",
        "${var.github_oidc_subject_prefix}:ref:refs/heads/main",
      ]
    }
  }
}

resource "aws_iam_role" "github_plan" {
  name               = "${var.lambda_function_name}-ci-plan"
  description        = "Read-only, for `terraform plan` in GitHub Actions."
  assume_role_policy = data.aws_iam_policy_document.github_plan_assume.json

  # Long enough for a plan, short enough to be uninteresting if it leaked.
  max_session_duration = 3600
}

# AWS's own ReadOnlyAccess rather than a hand-written list of permissions.
#
# The trade-off is deliberate: a narrow policy would be tighter, but a plan
# touches a long tail of describe/list calls across five services and getting
# one wrong means a failing pipeline and a round of permission
# whack-a-mole. This grants broad read and zero write, which for a single-owner
# account is the right side of that trade. Swap it for a scoped policy if the
# account ever holds something that must not be readable.
resource "aws_iam_role_policy_attachment" "github_plan_read" {
  role       = aws_iam_role.github_plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

output "ci_plan_role_arn" {
  description = "Set as AWS_PLAN_ROLE_ARN in the repository's Actions variables."
  value       = aws_iam_role.github_plan.arn
}
