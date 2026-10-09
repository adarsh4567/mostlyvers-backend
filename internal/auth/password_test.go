package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("A-secure-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "A-secure-password-123!") {
		t.Fatal("password did not verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("wrong password verified")
	}
}

func TestOpaqueTokenHashStable(t *testing.T) {
	plain, hash, err := OpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if string(hash) != string(TokenHash(plain)) {
		t.Fatal("token hash mismatch")
	}
}
