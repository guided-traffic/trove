package observability

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// logged returns the log line of a record with one attribute, after the handler's redaction.
func logged(t *testing.T, key string, val any) string {
	t.Helper()
	var buf bytes.Buffer
	NewLogger(testLogCfg(), &buf).Info("event", key, val)
	return buf.String()
}

// TestR_BAS_008_TheBastionTokenHeaderIsRedactedLikeAuthorization: Trove-Bastion-Authorization is a sensitive
// header of R-OPS-013 in every log record, span attribute and support bundle (which take the same handler), as a
// header and as text; the other Trove-Bastion-* headers carry no secret and stay readable.
func TestR_BAS_008_TheBastionTokenHeaderIsRedactedLikeAuthorization(t *testing.T) {
	const secret = "trv_bst_Zy9xKq3LmNpQrStUvWxYz0123456789abcd"
	h := http.Header{
		"Trove-Bastion-Authorization": {"Bearer " + secret},
		"Trove-Bastion-Interface":     {"1"},
		"Trove-Bastion-Replica":       {"trove-bastion-7d9c-x2k4q"},
		"Authorization":               {"Bearer callertoken123"},
	}
	out := logged(t, "headers", h)
	for _, leak := range []string{secret, "Zy9xKq3L", "callertoken123"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked: %s", leak, out)
		}
	}
	if !strings.Contains(out, "trove-bastion-7d9c-x2k4q") || !strings.Contains(out, "Trove-Bastion-Interface") {
		t.Errorf("a header without a secret lost its value: %s", out)
	}
	if !SensitiveKey("Trove_Bastion_Authorization") || !SensitiveKey("trove-bastion-authorization") {
		t.Error("SensitiveKey does not name the header")
	}
	for name, val := range map[string]any{
		"a map of headers": map[string]string{"trove-bastion-authorization": "Bearer " + secret},
		"text":             "forwarded with Trove-Bastion-Authorization: Bearer " + secret,
		"the bare token":   secret,
	} {
		if out := logged(t, "value", val); strings.Contains(out, "Zy9xKq3L") {
			t.Errorf("%s: %s", name, out)
		}
	}
}
