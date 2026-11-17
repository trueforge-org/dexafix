package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/trueforge-org/dexafix/pkg/store"
)

// The user's own switch only turns on once an administrator allowed it, and taking the permission
// away clears it, so a later re-allow does not switch it back on.
func TestAutoSyncFlags(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	u, err := s.CreateUser(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserWithingsAuto(ctx, u.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("switching on without permission: %v", err)
	}
	if err := s.SetUserWithingsAutoAllowed(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(ctx, u.ID)
	if !got.WithingsAutoAllowed || got.WithingsAuto {
		t.Fatalf("allowing must not switch it on: %+v", got)
	}
	if err := s.SetUserWithingsAuto(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetUser(ctx, u.ID); !got.WithingsAuto {
		t.Fatal("the switch did not turn on")
	}
	if err := s.SetUserWithingsAutoAllowed(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserWithingsAutoAllowed(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetUser(ctx, u.ID); got.WithingsAuto {
		t.Fatal("a re-allow switched the user's own switch back on")
	}

	// The same for Strength Level, and the two pairs do not affect each other.
	if err := s.SetUserStrengthLevelPush(ctx, u.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("switching on without permission: %v", err)
	}
	if err := s.SetUserStrengthLevelPushAllowed(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserStrengthLevelPush(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserWithingsAutoAllowed(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(ctx, u.ID)
	if !got.StrengthLevelPush || !got.StrengthLevelPushAllowed || got.WithingsAutoAllowed {
		t.Fatalf("flags leaked into each other: %+v", got)
	}
	if err := s.SetUserStrengthLevelPushAllowed(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetUser(ctx, u.ID); got.StrengthLevelPush {
		t.Fatal("taking the permission away left the switch on")
	}
}
