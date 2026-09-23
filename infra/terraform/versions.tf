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
  }

  # State lives on disk by default, which is fine while one person deploys
  # from one machine. Move it to S3 before a second person (or CI) ever runs
  # `apply`, or you will eventually have two states that disagree about what
  # exists. The bucket has to be created before this block can point at it,
  # so it can't be managed by this configuration.
  #
  # backend "s3" {
  #   bucket       = "archive-tfstate"
  #   key          = "archive-api/terraform.tfstate"
  #   region       = "us-east-1"
  #   encrypt      = true
  #   use_lockfile = true
  # }
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
