package mainclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
)

// TestR_BAS_006_TheSyntaxCheckAgreesWithTheMainInstallationsFormat: the Bastion's check and internal/auth/token
// accept the same tokens and refuse the same corruptions (R-AUTH-022). The test may import the main
// installation's package; the production code may not (R-BAS-001).
func TestR_BAS_006_TheSyntaxCheckAgreesWithTheMainInstallationsFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		raw, err := token.Generate(token.TypeBST)
		if err != nil {
			t.Fatal(err)
		}
		tok, err := ParseToken(raw)
		if err != nil || tok.Value != raw || tok.Hint != token.Hint(raw) {
			t.Fatalf("ParseToken(%q) = %+v, %v", raw, tok, err)
		}
		// One flipped character fails both checks.
		flipped := raw[:20] + string(rune(map[bool]byte{true: 'a', false: 'b'}[raw[20] != 'a'])) + raw[21:]
		_, ours := ParseToken(flipped)
		_, theirs := token.Check(flipped)
		if (ours == nil) != (theirs == nil) {
			t.Fatalf("flipped token %q: ours %v, theirs %v", flipped, ours, theirs)
		}
		if ours == nil {
			t.Fatalf("a token with a flipped character passed")
		}
	}
	pat, _ := token.Generate(token.TypePAT)
	for _, bad := range []string{"", "trv_bst_", pat, strings.Repeat("a", 57), "trv_bst_" + strings.Repeat("!", 49)} {
		if _, err := ParseToken(bad); !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("ParseToken(%q) = %v", bad, err)
		}
	}
}

func TestR_BAS_006_ATokenFileIsReadWithoutItsTrailingNewline(t *testing.T) {
	raw, _ := token.Generate(token.TypeBST)
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(raw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := ReadTokenFile(path)
	if err != nil || tok.Value != raw {
		t.Fatalf("ReadTokenFile = %+v, %v", tok, err)
	}
	if err := os.WriteFile(path, []byte(raw+" x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadTokenFile(path)
	if !errors.Is(err, ErrTokenMalformed) || strings.Contains(err.Error(), raw) {
		t.Fatalf("a malformed file: %v", err)
	}
	if _, err := ReadTokenFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file was read")
	}
}

func TestR_BAS_006_TheHolderReplacesTheTokenAtomically(t *testing.T) {
	h := NewHolder(Token{Value: "a", Hint: "a"})
	taken := h.Token()
	h.Set(Token{Value: "b"})
	if taken.Value != "a" || h.Token().Value != "b" {
		t.Fatalf("taken = %q, now = %q", taken.Value, h.Token().Value)
	}
	if (&Holder{}).Token() != (Token{}) {
		t.Fatal("a fresh holder holds a token")
	}
}
