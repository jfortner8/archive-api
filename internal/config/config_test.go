package config

import "testing"

func TestLoad(t *testing.T) {
	t.Run("required fields missing", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "")
		t.Setenv("DYNAMODB_TABLE", "")
		t.Setenv("API_KEY", "")

		if _, err := Load(); err == nil {
			t.Fatal("expected an error when S3_BUCKET/DYNAMODB_TABLE/API_KEY are unset")
		}
	})

	t.Run("API_KEY missing", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "")

		if _, err := Load(); err == nil {
			t.Fatal("expected an error when API_KEY is unset")
		}
	})

	t.Run("defaults and overrides", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "dev-key")
		t.Setenv("PORT", "")
		t.Setenv("AWS_REGION", "")

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
	})

	t.Run("explicit port and region override defaults", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "archive-dev")
		t.Setenv("DYNAMODB_TABLE", "archive-items-dev")
		t.Setenv("API_KEY", "dev-key")
		t.Setenv("PORT", "9090")
		t.Setenv("AWS_REGION", "eu-west-1")

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
	})
}
