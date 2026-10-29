package rawfiles

import (
	"net/http/httptest"
	"testing"
)

func TestR_STOR_007_TheStoredMediaTypeIsParsedAndNormalised(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"application/octet-stream", "application/octet-stream", true},
		{"Text/HTML; Charset=UTF-8", "text/html; charset=UTF-8", true},
		{`text/plain; name="a b"`, `text/plain; name="a b"`, true},
		{"not a media type", "", false},
		{"text/html; <script>", "", false},
		{"text/html;;", "", false},
	} {
		t.Run("R-STOR-007 "+c.in, func(t *testing.T) {
			r := httptest.NewRequest("PUT", "/files/r/a", nil)
			r.Header.Set("Content-Type", c.in)
			got, err := mediaType(r)
			if c.ok != (err == nil) || got != c.want {
				t.Fatalf("mediaType(%q) = %q, %v", c.in, got, err)
			}
		})
	}
}
