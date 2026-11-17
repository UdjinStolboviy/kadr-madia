package pipeline

import "testing"

// Vectors shared with sdk/node/test/signing.test.ts and edge/kadr-edge.
func TestSignKeyMatchesNodeSDK(t *testing.T) {
	const secret, key = "test-secret-0123456789abcdef", "acme/blb_01JDTEST/main.avif"
	if got := SignKey(secret, key, 1763330400, "receipt.avif"); got != "6htVHR_ussyeRR5QQBVvsmobaFla_5qrDeHoDtYVw5M" {
		t.Fatalf("with download: %s", got)
	}
	if got := SignKey(secret, key, 1763330400, ""); got != "O15QXzAjaI3KPNfA_FguxK3zQPZD0s-YMgTTH8SDHNU" {
		t.Fatalf("without download: %s", got)
	}
}
