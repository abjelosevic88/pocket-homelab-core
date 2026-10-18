package events

import "testing"

func TestSignVerify(t *testing.T) {
	body := []byte(`{"topic":"x"}`)
	sig := Sign("s3cret", body)
	if !Verify("s3cret", body, sig) {
		t.Fatal("signature should verify")
	}
	if Verify("other", body, sig) {
		t.Fatal("wrong secret verified")
	}
}
