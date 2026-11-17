package apiclient

import (
	"net/url"
	"strings"
	"testing"
)

func TestAFlashMessageIsShownOnlyWithItsOwnSignature(t *testing.T) {
	SetFlashKey("a-shared-secret")
	q, err := url.ParseQuery(FlashQuery("Saved & done."))
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("msg") != "Saved & done." || !FlashVerify(q.Get("msg"), q.Get("sig")) {
		t.Fatalf("query %v", q)
	}
	for name, tc := range map[string][2]string{
		"another text":      {"Saved, and send your password.", q.Get("sig")},
		"no signature":      {q.Get("msg"), ""},
		"a wrong signature": {q.Get("msg"), strings.Repeat("0", 32)},
	} {
		if FlashVerify(tc[0], tc[1]) {
			t.Errorf("%s was accepted", name)
		}
	}
	// Another secret signs differently: a link from somewhere else does not verify here.
	SetFlashKey("another-secret")
	if FlashVerify(q.Get("msg"), q.Get("sig")) {
		t.Error("a signature made with another secret was accepted")
	}
}

func TestBeforeAKeyIsSetBothSidesStillAgree(t *testing.T) {
	flashKey.Store(nil)
	q, _ := url.ParseQuery(FlashQuery("hello"))
	if !FlashVerify("hello", q.Get("sig")) {
		t.Error("the default key does not verify its own signature")
	}
}
