package edge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A server with a write timeout cuts off a slow reader, and a download handler can ask for longer,
// also behind the compression wrapper.
func TestAWriteTimeoutCutsOffASlowReaderUnlessTheDownloadAsksForLonger(t *testing.T) {
	big := strings.Repeat("x", 8<<20) // more than the socket buffers hold
	for name, tc := range map[string]struct {
		extend    bool
		wantFull  bool
		timeoutMS int
	}{
		"a plain answer": {false, false, 150},
		"a download":     {true, true, 150},
	} {
		t.Run(name, func(t *testing.T) {
			var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain") // compressible: the wrapper is in the way
				if tc.extend {
					ExtendWriteDeadline(w, 10*time.Second)
				}
				io.WriteString(w, big)
			})
			h = Compress(h)
			srv := httptest.NewUnstartedServer(h)
			srv.Config.WriteTimeout = time.Duration(tc.timeoutMS) * time.Millisecond
			srv.Start()
			defer srv.Close()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			req.Header.Set("Accept-Encoding", "identity")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			time.Sleep(time.Duration(tc.timeoutMS*3) * time.Millisecond) // a reader that does not read
			n, _ := io.Copy(io.Discard, resp.Body)
			if full := n == int64(len(big)); full != tc.wantFull {
				t.Fatalf("read %d of %d bytes, complete = %v, want %v", n, len(big), full, tc.wantFull)
			}
		})
	}
	// A writer that cannot change its deadline is left alone.
	ExtendWriteDeadline(httptest.NewRecorder(), time.Minute)
}

func TestTheGzipWriterLetsAControllerThroughToTheConnection(t *testing.T) {
	g := &gzipWriter{ResponseWriter: httptest.NewRecorder()}
	if g.Unwrap() != g.ResponseWriter {
		t.Error("Unwrap does not return the writer underneath")
	}
}
