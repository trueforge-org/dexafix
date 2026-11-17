package cmd

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/trueforge-org/dexafix/pkg/shared/mtls"
)

// Go leaves the server name empty for an address URL, so the certificate's name used to go unchecked
// unless --backend-server-name was given: any certificate from the CA was accepted at any address.
func TestAnAddressInTheBackendURLIsCheckedAgainstTheCertificate(t *testing.T) {
	b, err := mtls.Generate([]string{"api"}) // the certificate names api, 127.0.0.1 and ::1 only
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"ca.pem": b.CA, "srv.pem": b.ServerCert, "srv.key": b.ServerKey, "cli.pem": b.ClientCert, "cli.key": b.ClientKey} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tlsCfg, err := mtls.ServerConfig(filepath.Join(dir, "srv.pem"), filepath.Join(dir, "srv.key"), filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.2:0") // an address the certificate does not carry
	if err != nil {
		t.Skip("no second loopback address: ", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	srv.Listener.Close()
	srv.Listener = ln
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := func(serverName string) (*http.Client, error) {
		c := &cobra.Command{}
		c.Flags().String("backend-ca-file", filepath.Join(dir, "ca.pem"), "")
		c.Flags().String("backend-client-cert-file", filepath.Join(dir, "cli.pem"), "")
		c.Flags().String("backend-client-key-file", filepath.Join(dir, "cli.key"), "")
		c.Flags().String("backend-server-name", serverName, "")
		hc, _, err := backendHTTPClient(c, srv.URL)
		return hc, err
	}
	hc, err := client("")
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := hc.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("a certificate that does not carry the address was accepted")
	}
	// Naming the certificate's own name still works, as before.
	hc, err = client("api")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatalf("with the name of the certificate: %v", err)
	}
	resp.Body.Close()
}
