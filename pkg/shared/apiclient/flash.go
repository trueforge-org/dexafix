package apiclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sync/atomic"
)

// A message that follows a redirect (?msg=) is text a page shows in its notice. Anybody can make a
// link with any text in it, so a link could put words in the mouth of the site. The messages the
// program itself sends carry a signature (&sig=) made with a key both the web and the api process
// have (derived from the service secret), and a page shows only a message whose signature fits.

var flashKey atomic.Pointer[[]byte]

// SetFlashKey derives the key that signs flash messages from the service secret. Both processes call
// it with the secret they share; before any call (tests) a fixed key is used on both sides.
func SetFlashKey(secret string) {
	sum := sha256.Sum256([]byte("dexafix flash message:" + secret))
	k := sum[:]
	flashKey.Store(&k)
}

func flashSig(msg string) string {
	key := []byte("dexafix flash message")
	if k := flashKey.Load(); k != nil {
		key = *k
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// FlashQuery is the query string that carries msg to a page: "msg=...&sig=...".
func FlashQuery(msg string) string {
	return "msg=" + url.QueryEscape(msg) + "&sig=" + flashSig(msg)
}

// FlashVerify reports whether sig is the signature of msg.
func FlashVerify(msg, sig string) bool {
	return hmac.Equal([]byte(flashSig(msg)), []byte(sig))
}
