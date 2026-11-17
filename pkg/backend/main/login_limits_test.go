package mainbackend

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/shared/apiclient"
)

// outcome is the status of a sign-in: the one it failed with, or 200 when it went through.
func outcome(t *testing.T, rep reply) int {
	t.Helper()
	var o apiclient.SignInOutcome
	rep.decode(t, &o)
	if o.Error != "" {
		return o.Status
	}
	return http.StatusOK
}

// A login name is typed by whoever asks, so it must not become a key of any size in the state store
// (Valkey keeps it for an hour, and the memory policy there is noeviction).
func TestALoginNameIsNeverAKeyOfItsOwnSize(t *testing.T) {
	for _, login := range []string{"a", "alice@example.com", strings.Repeat("x", 900<<10)} {
		if n := len(loginKey(login)); n != len("login:")+64 {
			t.Errorf("a login of %d bytes made a key of %d bytes", len(login), n)
		}
	}
	if loginKey("Alice@Example.com ") != loginKey("alice@example.com") {
		t.Error("the same login in another case or with spaces counts separately")
	}
	r := &http.Request{RemoteAddr: "192.0.2.1:1"}
	if k := loginAddrKey(r, strings.Repeat("x", 900<<10)); len(k) > 200 {
		t.Errorf("a login of 900 KiB made a key of %d bytes", len(k))
	}
}

func TestALoginLongerThanAnAddressCanBeIsRefusedBeforeItIsCounted(t *testing.T) {
	g := newRig(t)
	g.signUp("alice")
	ctx := context.Background()
	long := strings.Repeat("x", 900<<10)
	for i := 0; i < 3; i++ {
		rep := g.do("POST", "/auth/password", map[string]string{"Email": long, "Password": "wrong password"})
		if got := outcome(t, rep); got != http.StatusUnauthorized {
			t.Fatalf("answer %d: %d", i, got)
		}
	}
	since := time.Now().Add(-time.Hour)
	for _, key := range []string{loginKey(long), loginAddrKey(&http.Request{RemoteAddr: "192.0.2.10:1"}, long)} {
		if n, err := g.b.limiter.st.CountFailures(ctx, key, since); err != nil || n != 0 {
			t.Errorf("the long login was counted: %d %v", n, err)
		}
	}
	// It does cost the address, so a flood of them from one place is stopped.
	req := &http.Request{RemoteAddr: "192.0.2.10:1"}
	if n, _ := g.b.limiter.st.CountFailures(ctx, ipKey(req), since); n != 3+1 { // and the sign-up above
		t.Errorf("the address was charged %d times, want 3 and the sign-up", n)
	}
	// And the longest login an address can be still goes through the normal path.
	ok := strings.Repeat("a", maxEmailLen-len("@example.com")) + "@example.com"
	if rep := g.do("POST", "/auth/password", map[string]string{"Email": ok, "Password": "wrong password"}); outcome(t, rep) != http.StatusUnauthorized {
		t.Fatalf("a login of %d bytes: %d", len(ok), outcome(t, rep))
	}
	if n, _ := g.b.limiter.st.CountFailures(ctx, loginKey(ok), since); n != 1 {
		t.Errorf("a login of the longest size was counted %d times, want 1", n)
	}
}

// Whoever types a victim's name from other addresses can lock those addresses, not the victim's.
func TestSomeoneElsesWrongPasswordsDoNotLockTheOwnerOut(t *testing.T) {
	g := newRig(t)
	g.signUp("alice")
	wrong := map[string]string{"Email": "alice@example.com", "Password": "not the password"}
	right := map[string]string{"Email": "alice@example.com", "Password": testPassword}

	for i := 0; i < maxLoginFail; i++ {
		if rep := g.doFrom("198.51.100.7", "POST", "/auth/password", wrong); outcome(t, rep) != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d", i, outcome(t, rep))
		}
	}
	if rep := g.doFrom("198.51.100.7", "POST", "/auth/password", right); outcome(t, rep) != http.StatusTooManyRequests {
		t.Fatalf("the attacker's address is not locked out: %d", outcome(t, rep))
	}
	if rep := g.doFrom("203.0.113.9", "POST", "/auth/password", right); outcome(t, rep) != http.StatusOK {
		t.Fatalf("the owner, from their own address: %d %s", outcome(t, rep), rep.body())
	}
}

// From enough addresses the name is blocked everywhere: that is what the higher cap is for.
func TestALoginNameFailingFromEverywhereIsBlockedEverywhere(t *testing.T) {
	g2 := newRig(t)
	g2.signUp("alice")
	right := map[string]string{"Email": "alice@example.com", "Password": testPassword}
	ctx := context.Background()
	for i := 0; i < maxLoginFailAll; i++ {
		g2.b.limiter.Fail(ctx, loginKey("alice@example.com"))
	}
	if rep := g2.doFrom("203.0.113.9", "POST", "/auth/password", right); outcome(t, rep) != http.StatusTooManyRequests {
		t.Fatalf("the name is not blocked after %d failures from everywhere: %d", maxLoginFailAll, outcome(t, rep))
	}
}

// The same holds for reset mail: an address cannot be flooded, and its owner cannot be shut out by
// someone who types it from another address.
func TestResetMailToOneAddressIsLimitedPerClientAndInTotal(t *testing.T) {
	g := newRig(t)
	g.signUp("alice")
	mailer := &fakeMailer{}
	enableSelfService(t, g.srv, mailer)
	ask := func(from string) {
		t.Helper()
		if rep := g.doFrom(from, "POST", "/auth/password/forgot", map[string]string{"Email": "alice@example.com"}); rep.Code != http.StatusOK {
			t.Fatalf("forgot from %s: %d", from, rep.Code)
		}
	}
	waitMails := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(mailer.all()) < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond) // a mail that should not be sent has time to show
		if got := len(mailer.all()); got != n {
			t.Fatalf("%d mails, want %d", got, n)
		}
	}
	for i := 0; i < maxResetAddr+2; i++ {
		ask("198.51.100.7") // one address asks over and over: only the first few mails go out
	}
	waitMails(maxResetAddr)
	ask("203.0.113.9") // the owner, from their own address, is not shut out
	waitMails(maxResetAddr + 1)

	// From many addresses an address still gets no more than maxResetAddrAll in total.
	g.b.limiter.Reset(context.Background(), "reset-addr:alice@example.com")
	for i := 0; i < maxResetAddrAll; i++ {
		g.b.limiter.Fail(context.Background(), "reset-addr:alice@example.com")
	}
	ask("192.0.2.77")
	waitMails(maxResetAddr + 1)
}
