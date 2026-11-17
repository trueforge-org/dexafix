package strengthlevel

import (
	"net/http"
	"testing"
)

func TestTheDefaultClientIsTheGuardedOne(t *testing.T) {
	c := newClient("", nil)
	if tr, ok := c.http.Transport.(*http.Transport); !ok || tr == http.DefaultTransport || c.http.CheckRedirect == nil || c.http.Jar == nil {
		t.Fatalf("default client: %T, redirect check %v, jar %v", c.http.Transport, c.http.CheckRedirect != nil, c.http.Jar != nil)
	}
}
