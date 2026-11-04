terraform {
  required_version = ">= 1.6"

  required_providers {
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.60"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }

  # State lives in S3 rather than on one machine. It is the only record of
  # which real resources this configuration owns, so losing it means Terraform
  # no longer knows what it manages and a later apply tries to create things
  # that already exist.
  #
  # The bucket is deliberately not managed here: a configuration cannot hold
  # the state describing the bucket that holds its own state. It was created
  # by hand, versioned (so a corrupt state can be rolled back) and private.
  #
  # use_lockfile keeps two concurrent applies from interleaving, which replaces
  # the DynamoDB lock table older setups needed.
  backend "s3" {
    bucket       = "archive-api-tfstate-816079797917"
    key          = "archive-api/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project   = "archive-api"
      ManagedBy = "terraform"
    }
  }
}
