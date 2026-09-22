package config

import "testing"

func TestLoad(t *testing.T) {
	t.Run("required fields missing", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "")
		t.Setenv("DYNAMODB_TABLE", "")
		t.Setenv("API_KEY", "")
		t.Setenv("COGNITO_USER_POOL_ID", "")

		if _, err := Load(); err == nil {
			t.Fatal("expected an error when S3_BUCKET/DYNAMODB_TABLE/API_KEY/COGNITO_USER_POOL_ID are unset")
		}
	})

	t.Run("API_KEY missing", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "")
		t.Setenv("COGNITO_USER_POOL_ID", "us-east-1_test")

		if _, err := Load(); err == nil {
			t.Fatal("expected an error when API_KEY is unset")
		}
	})

	t.Run("COGNITO_USER_POOL_ID missing", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "dev-key")
		t.Setenv("COGNITO_USER_POOL_ID", "")

		if _, err := Load(); err == nil {
			t.Fatal("expected an error when COGNITO_USER_POOL_ID is unset")
		}
	})

	t.Run("defaults and overrides", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "dev-key")
		t.Setenv("COGNITO_USER_POOL_ID", "us-east-1_test")
		t.Setenv("PORT", "")
		t.Setenv("AWS_REGION", "")
		t.Setenv("COGNITO_REGION", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Port != "8080" {
			t.Errorf("Port = %q, want default %q", cfg.Port, "8080")
		}
		if cfg.AWSRegion != "us-east-1" {
			t.Errorf("AWSRegion = %q, want default %q", cfg.AWSRegion, "us-east-1")
		}
		if cfg.S3Bucket != "archive-dev" {
			t.Errorf("S3Bucket = %q, want %q", cfg.S3Bucket, "archive-dev")
		}
		if cfg.CognitoRegion != "us-east-1" {
			t.Errorf("CognitoRegion = %q, want it to default to AWSRegion (%q)", cfg.CognitoRegion, "us-east-1")
		}
	})

	t.Run("explicit port and region override defaults", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "dev-key")
		t.Setenv("COGNITO_USER_POOL_ID", "us-east-1_test")
		t.Setenv("PORT", "9090")
		t.Setenv("AWS_REGION", "eu-west-1")
		t.Setenv("COGNITO_REGION", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Port != "9090" {
			t.Errorf("Port = %q, want %q", cfg.Port, "9090")
		}
		if cfg.AWSRegion != "eu-west-1" {
			t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, "eu-west-1")
		}
		if cfg.CognitoRegion != "eu-west-1" {
			t.Errorf("CognitoRegion = %q, want it to default to the overridden AWSRegion (%q)", cfg.CognitoRegion, "eu-west-1")
		}
	})

	t.Run("explicit COGNITO_REGION overrides the AWSRegion default", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "dev-key")
		t.Setenv("COGNITO_USER_POOL_ID", "us-east-1_test")
		t.Setenv("AWS_REGION", "eu-west-1")
		t.Setenv("COGNITO_REGION", "ap-southeast-1")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.CognitoRegion != "ap-southeast-1" {
			t.Errorf("CognitoRegion = %q, want explicit override %q", cfg.CognitoRegion, "ap-southeast-1")
		}
	})
}
