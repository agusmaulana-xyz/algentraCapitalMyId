package config

import "testing"

func TestValidateDevelopmentSecrets(t *testing.T) {
	s := Settings{
		AppEnv: "development", AppSecretKey: "a-random-secret-that-is-long-enough-to-use",
		AdminUsername: "admin", AdminPassword: "a-unique-password-long-enough",
		DatabaseFile: "./data/database.sqlite",
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid development config rejected: %v", err)
	}
}

func TestValidateRequiresDatabaseFile(t *testing.T) {
	base := Settings{
		AppEnv: "development", AppSecretKey: "a-random-secret-that-is-long-enough-to-use",
		AdminUsername: "admin", AdminPassword: "a-unique-password-long-enough",
	}
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing DATABASE_FILE to be rejected")
	}
}

func TestNormalizeOrigins(t *testing.T) {
	got, err := normalizeOrigins("https://portal.example.com/, https://portal.example.com,http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "https://portal.example.com" || got[1] != "http://localhost:3000" {
		t.Fatalf("unexpected normalized origins: %#v", got)
	}
	if _, err := normalizeOrigins("https://portal.example.com/path"); err == nil {
		t.Fatal("expected an origin with a path to be rejected")
	}
}
