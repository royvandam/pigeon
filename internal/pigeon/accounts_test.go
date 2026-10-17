package pigeon

import (
	"context"
	"errors"
	"testing"
)

func TestAccountManagerRequiresAdministrator(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"create", "delete", "reset", "quota", "recycle", "purge"} {
		change, err := cfg.accounts().apply(ctx, u, accountCommand{action: action, id: u.ID, username: "Unauthorized", quota: defaultUserQuota})
		if !errors.Is(err, errAdministratorRequired) || change.Credentials != nil || change.ActorPasswordChanged {
			t.Fatalf("regular user could perform %s: %#v %v", action, change, err)
		}
	}
	if _, err := cfg.store.authenticate(ctx, u.Username, testPassword); err != nil {
		t.Fatalf("rejected changes modified the account: %v", err)
	}
}
