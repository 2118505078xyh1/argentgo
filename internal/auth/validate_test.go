package auth

import (
	"strings"
	"testing"
)

func TestValidatePKCE(t *testing.T) {
	const (
		rfcVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)

	longVerifier := strings.Repeat("a", 128)
	longChallenge := S256(longVerifier)
	last := "A"
	if longChallenge[len(longChallenge)-1] == 'A' {
		last = "B"
	}

	cases := []struct {
		name      string
		verifier  string
		challenge string
		method    string
		want      bool
	}{
		{
			name:      "RFC7636 S256 vector matches",
			verifier:  rfcVerifier,
			challenge: rfcChallenge,
			method:    "S256",
			want:      true,
		},
		{
			name:      "S256 wrong challenge fails",
			verifier:  rfcVerifier,
			challenge: rfcChallenge[:len(rfcChallenge)-1] + "A",
			method:    "S256",
			want:      false,
		},
		{
			name:      "S256 plain verifier is not the challenge",
			verifier:  rfcVerifier,
			challenge: rfcVerifier,
			method:    "S256",
			want:      false,
		},
		{
			name:      "plain equal strings match",
			verifier:  rfcVerifier,
			challenge: rfcVerifier,
			method:    "plain",
			want:      true,
		},
		{
			name:      "plain different strings fail",
			verifier:  rfcVerifier,
			challenge: rfcVerifier + "x",
			method:    "plain",
			want:      false,
		},
		{
			name:      "empty method defaults to plain and matches",
			verifier:  rfcVerifier,
			challenge: rfcVerifier,
			method:    "",
			want:      true,
		},
		{
			name:      "empty method defaults to plain and rejects a hashed challenge",
			verifier:  rfcVerifier,
			challenge: rfcChallenge,
			method:    "",
			want:      false,
		},
		{
			name:      "unknown method fails even when strings match",
			verifier:  rfcVerifier,
			challenge: rfcVerifier,
			method:    "SHA1",
			want:      false,
		},
		{
			name:      "lowercase S256 is not the registered method",
			verifier:  rfcVerifier,
			challenge: rfcChallenge,
			method:    "s256",
			want:      false,
		},
		{
			name:      "method with whitespace fails",
			verifier:  rfcVerifier,
			challenge: rfcChallenge,
			method:    "S256 ",
			want:      false,
		},
		{
			name:      "empty verifier with empty challenge under S256 fails",
			verifier:  "",
			challenge: "",
			method:    "S256",
			want:      false,
		},
		{
			name:      "empty verifier with empty challenge under plain matches",
			verifier:  "",
			challenge: "",
			method:    "plain",
			want:      true,
		},
		{
			name:      "S256 of empty verifier matches its own hash",
			verifier:  "",
			challenge: S256(""),
			method:    "S256",
			want:      true,
		},
		{
			name:      "padded base64 challenge is rejected",
			verifier:  rfcVerifier,
			challenge: rfcChallenge + "=",
			method:    "S256",
			want:      false,
		},
		{
			name:      "single character mismatch in long challenge fails",
			verifier:  longVerifier,
			challenge: longChallenge[:len(longChallenge)-1] + last,
			method:    "S256",
			want:      false,
		},
		{
			name:      "non ASCII verifier is hashed as UTF-8 bytes",
			verifier:  "验证-abc-测试",
			challenge: S256("验证-abc-测试"),
			method:    "S256",
			want:      true,
		},
		{
			name:      "non ASCII plain verifier matches verbatim",
			verifier:  "验证-abc-测试",
			challenge: "验证-abc-测试",
			method:    "plain",
			want:      true,
		},
	}

	for _, tc := range cases {
		if got := ValidatePKCE(tc.verifier, tc.challenge, tc.method); got != tc.want {
			t.Errorf("ValidatePKCE(%q, %q, %q) = %v, want %v",
				tc.verifier, tc.challenge, tc.method, got, tc.want)
		}
	}
}

func TestValidatePKCEAcceptsFreshlyGeneratedPair(t *testing.T) {
	verifier, challenge, err := PKCE()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	if !ValidatePKCE(verifier, challenge, "S256") {
		t.Fatal("fresh PKCE pair did not validate under S256")
	}
	if ValidatePKCE(verifier, challenge, "plain") {
		t.Fatal("fresh PKCE pair validated under plain")
	}
}
