package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http/httptest"
	"testing"

	"cctrace/internal/configtxn"
	"cctrace/internal/profile"
)

// The fixture changes HOME and the embedded signing key; keep this serial.
func TestServerPolicyAppliedCAAllowsVerifiedUpdate(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	pub, key := newUpdateSigningKey(t)
	binary := []byte("isolated update payload")
	signed := signedUpdateServer(t, binary, key, nil)
	srv := httptest.NewTLSServer(signed.Config.Handler)
	defer srv.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	sum := sha256.Sum256(ca)
	pol.To.Sync, pol.To.OTEL = srv.URL, srv.URL
	pol.CAPEM, pol.CASHA256 = string(ca), hex.EncodeToString(sum[:])

	untrusted, err := serverClient("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := untrusted.Get(srv.URL); err == nil {
		response.Body.Close()
		t.Fatal("fixture server was already trusted before policy application")
	}
	changes, _, err := planServerPolicy(p, "", pol, "fixture-digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := policyPath("", "server-policy-txn")
	if err != nil {
		t.Fatal(err)
	}
	if err := configtxn.Apply(dir, changes, nil); err != nil {
		t.Fatal(err)
	}
	current, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	client, err := serverClient(current.Server.CACertFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	applied := false
	err = downloadAndApplyUpdateWith(context.Background(), client, current.Server.SyncEndpoint,
		func(r io.Reader) error {
			got, err := io.ReadAll(r)
			if !bytes.Equal(got, binary) {
				t.Errorf("verified update bytes = %q", got)
			}
			applied = true
			return err
		}, base64.StdEncoding.EncodeToString(pub), testUpdateVersion)
	if err != nil || !applied {
		t.Fatalf("update using applied profile failed: applied=%v, err=%v", applied, err)
	}
}
