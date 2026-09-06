package safetext

import "testing"

func TestMessageBodyKeepsFormattingWithoutRelaxingScalarGuards(t *testing.T) {
	for _, body := range []string{"observation\n", "observation\r\n", " first\n\nsecond\n", "first\r\n\r\nsecond\r\n"} {
		if MessageBodyContainsSecretLike(body) {
			t.Errorf("ordinary multiline body rejected: %q", body)
		}
		if !ContainsSecretLike(body) {
			t.Errorf("scalar newline guard relaxed: %q", body)
		}
	}
}

func TestMessageBodyRejectsCredentialsAcrossLineBoundaries(t *testing.T) {
	for _, body := range []string{
		"observation\x00second\n", "password\n= example", "api_\nkey: example", "Bearer\nabcdefgh1234",
		"Bearer a\nb\nc\nd\ne\nf\ng\nh", "sk-proj-abcdef\nghijklmnop", "ghp_abcdefghij\nklmnopqrstuvwxyz",
		"-----BEGIN RSA\n PRIVATE KEY-----\nexample", "-----BE\r\nGIN PRIVATE KEY-----", "https://user:pass\nword@example.com",
	} {
		if !MessageBodyContainsSecretLike(body) {
			t.Errorf("credential/control body accepted: %q", body)
		}
	}
}
