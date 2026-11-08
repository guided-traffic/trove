package openpgp

import (
	"bytes"
	"io"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

const (
	typePublicKey  = "PGP PUBLIC KEY BLOCK"
	typePrivateKey = "PGP PRIVATE KEY BLOCK"
	typeSignature  = "PGP SIGNATURE"
)

const (
	armorBegin = "-----BEGIN PGP "
	armorEnd   = "\n-----END PGP "
)

// armorEncode writes the packets that write produces as one armor block of the type, without version or comment headers.
func armorEncode(blockType string, write func(io.Writer) error) ([]byte, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, blockType, nil)
	if err != nil {
		return nil, err
	}
	if err := write(w); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// splitArmor cuts text into its armor blocks. Anything between the blocks but white space is refused: the input is a key or a
// signature, and a stray line would be silently dropped by a lenient reader.
func splitArmor(data []byte) ([][]byte, error) {
	var blocks [][]byte
	rest := data
	for {
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) == 0 {
			return blocks, nil
		}
		if !bytes.HasPrefix(rest, []byte(armorBegin)) {
			return nil, invalid("text outside an armored block")
		}
		i := bytes.Index(rest, []byte(armorEnd))
		if i < 0 {
			return nil, invalid("an armored block is not closed")
		}
		end := len(rest)
		if j := bytes.IndexByte(rest[i+1:], '\n'); j >= 0 {
			end = i + 1 + j
		}
		if line := bytes.TrimRight(rest[i+1:end], " \t\r"); !bytes.HasSuffix(line, []byte("-----")) {
			return nil, invalid("text after the end of an armored block")
		}
		blocks = append(blocks, rest[:end])
		rest = rest[end:]
	}
}

// armorDecode decodes one armor block of the wanted type to its binary packets, at most max bytes of them.
func armorDecode(block []byte, wantType string, max int) ([]byte, error) {
	b, err := armor.Decode(bytes.NewReader(block))
	if err != nil {
		return nil, invalid("armor: %v", err)
	}
	if b.Type != wantType {
		return nil, invalid("expected %q, got %q", wantType, b.Type)
	}
	body, err := io.ReadAll(io.LimitReader(b.Body, int64(max)+1))
	if err != nil {
		return nil, invalid("armor: %v", err)
	}
	if len(body) > max {
		return nil, invalid("armored data is too large")
	}
	return body, nil
}

// tooMany is the error of a count over its bound.
func tooMany(what string, n, limit int) error {
	return invalid("%d %s, at most %d", n, what, limit)
}
