package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// At read committed on PostgreSQL, two requests that each see "another administrator exists" could
// both remove one, and two first sign-ups could both become the administrator. SQLite serialises
// its writes, so the race is PostgreSQL's; the test still runs everywhere (it must hold on both).
func TestTheSetOfAdministratorsChangesOneRequestAtATime(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		t.Run(fmt.Sprint("round ", round), func(t *testing.T) {
			s := newStore(t)
			a, err := s.CreateUser(ctx, "admin-a")
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.CreateUser(ctx, "admin-b")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetUserRole(ctx, b.ID, models.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			// Two administrators, and both are demoted or deleted at the same moment.
			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for i, f := range []func() error{
				func() error { return s.SetUserRole(ctx, a.ID, models.RoleUser) },
				func() error { return s.SetUserRole(ctx, b.ID, models.RoleUser) },
			} {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; errs[i] = f() }()
			}
			close(start)
			wg.Wait()
			refused := 0
			for _, err := range errs {
				if errors.Is(err, store.ErrLastAdmin) {
					refused++
				}
			}
			if n := countAdmins(t, s); n != 1 || refused != 1 {
				t.Fatalf("round %d: %d administrators left, %d refused (%v)", round, n, refused, errs)
			}
		})
	}
}

func TestTwoDeletesCannotRemoveTheLastTwoAdministrators(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		t.Run(fmt.Sprint("round ", round), func(t *testing.T) {
			s := newStore(t)
			a, _ := s.CreateUser(ctx, "admin-a")
			b, _ := s.CreateUser(ctx, "admin-b")
			if err := s.SetUserRole(ctx, b.ID, models.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for i, id := range []int64{a.ID, b.ID} {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; errs[i] = s.DeleteUser(ctx, id) }()
			}
			close(start)
			wg.Wait()
			if n := countAdmins(t, s); n != 1 {
				t.Fatalf("round %d: %d administrators left (%v)", round, n, errs)
			}
		})
	}
}

func TestOnlyOneOfTwoFirstSignUpsBecomesTheAdministrator(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		t.Run(fmt.Sprint("round ", round), func(t *testing.T) {
			s := newStore(t)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, name := range []string{"first-a", "first-b", "first-c"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if _, err := s.CreateUser(ctx, name); err != nil {
						t.Errorf("%s: %v", name, err)
					}
				}()
			}
			close(start)
			wg.Wait()
			if n := countAdmins(t, s); n != 1 {
				t.Fatalf("round %d: %d administrators", round, n)
			}
		})
	}
}

func countAdmins(t *testing.T, s *Store) int {
	t.Helper()
	users, err := s.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, u := range users {
		if u.IsAdmin() {
			n++
		}
	}
	return n
}

// A subject that belongs to another user is not overwritten by a second user who names it.
func TestAnIdentityOfAnotherUserIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a, _ := s.CreateUser(ctx, "owner-a")
	b, _ := s.CreateUser(ctx, "other-b")
	if err := s.PutIdentity(ctx, models.Identity{UserID: a.ID, Provider: "idp", Subject: "s1", Secret: "mine"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdentity(ctx, models.Identity{UserID: b.ID, Provider: "idp", Subject: "s1", Secret: "taken"}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("a subject of another user: %v", err)
	}
	got, err := s.GetIdentity(ctx, "idp", "s1")
	if err != nil || got.UserID != a.ID || got.Secret != "mine" {
		t.Fatalf("the identity changed: %+v %v", got, err)
	}
	// The owner may still change their own (a new password hash).
	if err := s.PutIdentity(ctx, models.Identity{UserID: a.ID, Provider: "idp", Subject: "s1", Secret: "newer"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetIdentity(ctx, "idp", "s1"); got.Secret != "newer" {
		t.Fatalf("the owner's update was lost: %+v", got)
	}
}
