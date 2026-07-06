package store

import (
	"testing"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if len(migrations) != 7 {
		t.Fatalf("expected 7 migration pairs, got %d", len(migrations))
	}
	want := []int{1, 2, 3, 4, 5, 6, 7}
	for i, m := range migrations {
		if m.Version != want[i] {
			t.Errorf("migration %d: version = %d, want %d", i, m.Version, want[i])
		}
		if m.Up == "" {
			t.Errorf("migration %d: missing Up script", m.Version)
		}
		if m.Down == "" {
			t.Errorf("migration %d: missing Down script", m.Version)
		}
	}
}

func TestLoadMigrationsContainsExpectedTables(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	wantTables := []string{
		"payment_intents",
		"captures",
		"settlements",
		"refunds",
		"chargebacks",
		"webhooks",
		"payment_transitions",
	}
	for _, table := range wantTables {
		found := false
		for _, m := range migrations {
			if contains(m.Up, table) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no migration creates table %q", table)
		}
	}
}

func TestLoadMigrationsWebhookUniqueConstraint(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	found := false
	for _, m := range migrations {
		if contains(m.Up, "webhooks_rail_external_event_uniq") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected webhooks migration to declare UNIQUE (rail, external_event_id)")
	}
}

func TestLoadMigrationsAppendOnlyGuard(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	foundTrigger := false
	foundRevoke := false
	for _, m := range migrations {
		if contains(m.Up, "guard_payment_transitions_append_only") {
			foundTrigger = true
		}
		if contains(m.Up, "REVOKE UPDATE, DELETE ON payment_transitions") {
			foundRevoke = true
		}
	}
	if !foundTrigger {
		t.Fatal("expected payment_transitions migration to install an append-only trigger")
	}
	if !foundRevoke {
		t.Fatal("expected payment_transitions migration to REVOKE UPDATE/DELETE")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("DB_URL", "postgres://u:p@host:5432/db")
	cfg := LoadConfig()
	if cfg.DBURL != "postgres://u:p@host:5432/db" {
		t.Errorf("DBURL = %q", cfg.DBURL)
	}
	if cfg.MaxConns != 25 {
		t.Errorf("MaxConns default = %d, want 25", cfg.MaxConns)
	}
	if cfg.MinConns != 2 {
		t.Errorf("MinConns default = %d, want 2", cfg.MinConns)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}