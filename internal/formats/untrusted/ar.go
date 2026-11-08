package untrusted

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// The bounds of an ar archive (R-DEB-034): at most eight members, names of at most 16 bytes (the field of the header; the long-name
// extensions are refused) and member sizes that add up to the body.
const (
	arMagic       = "!<arch>\n"
	arHeaderBytes = 60
	arMaxMembers  = 8
)

// ArMember is one member of an ar archive in the order the archive holds it: its name without padding and the size its header
// declares.
type ArMember struct {
	Name string
	Size int64
}

// ArReader reads the members of an ar archive, the container of a Debian package (deb(5), R-DEB-007), within the bounds above. It
// checks every header when it is created, so a member that does not fit the body, a name extension, a size that is no number or bytes
// after the last member are refused before any content is read; a member's body is a section of the archive and is never read into
// memory by the reader itself.
type ArReader struct {
	r       io.ReaderAt
	members []arEntry
	next    int
	cur     int
}

type arEntry struct {
	ArMember
	off int64
}

func arInvalid(detail string) error {
	return errs.E(errs.Unprocessable, errs.CodeDebInvalidArchive, "The upload is not a valid ar archive: "+detail+".")
}

// NewAr checks the global header of the archive and its member headers. size is the length of the archive. Malformed input is 422
// deb_invalid_archive; the limits do not change the bounds above, which only tighten the format's own (the deadline is the caller's).
func NewAr(r io.ReaderAt, size int64, _ Limits) (*ArReader, error) {
	if size < int64(len(arMagic)) {
		return nil, arInvalid("it is shorter than the global header")
	}
	head := make([]byte, len(arMagic))
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, arInvalid("the global header cannot be read")
	}
	if string(head) != arMagic {
		return nil, arInvalid("the global header is not \"!<arch>\"")
	}
	a := &ArReader{r: r, cur: -1}
	off := int64(len(arMagic))
	hdr := make([]byte, arHeaderBytes)
	for off < size {
		if len(a.members) == arMaxMembers {
			return nil, errs.E(errs.TooLarge, errs.CodeTooLarge, fmt.Sprintf("The archive holds more than %d members.", arMaxMembers))
		}
		if size-off < arHeaderBytes {
			return nil, arInvalid("a member header is cut off")
		}
		if _, err := r.ReadAt(hdr, off); err != nil {
			return nil, arInvalid("a member header cannot be read")
		}
		if string(hdr[58:60]) != "`\n" {
			return nil, arInvalid("a member header has no terminator")
		}
		name := strings.TrimRight(string(hdr[0:16]), " ")
		name = strings.TrimSuffix(name, "/")
		switch {
		case name == "", strings.HasPrefix(name, "#1/"), name == "//", name == "/":
			return nil, arInvalid("a member has no plain name (long names and symbol tables are not accepted)")
		case !validArName(name):
			return nil, arInvalid("a member name holds a character that is not accepted")
		}
		n, err := strconv.ParseInt(strings.TrimRight(string(hdr[48:58]), " "), 10, 64)
		if err != nil || n < 0 {
			return nil, arInvalid("a member size is not a number")
		}
		off += arHeaderBytes
		if n > size-off {
			return nil, arInvalid("a member is larger than the rest of the archive")
		}
		a.members = append(a.members, arEntry{ArMember: ArMember{Name: name, Size: n}, off: off})
		off += n
		if n%2 == 1 && off < size {
			off++ // the padding byte
		}
	}
	if off != size && off != size+1 {
		return nil, arInvalid("bytes follow the last member")
	}
	return a, nil
}

// validArName admits the characters of a Debian package member: letters, digits, '.', '_' and '-'.
func validArName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// Len is the number of members.
func (a *ArReader) Len() int { return len(a.members) }

// Next advances to the next member and returns it; io.EOF after the last.
func (a *ArReader) Next() (ArMember, error) {
	if a.next >= len(a.members) {
		return ArMember{}, io.EOF
	}
	a.cur = a.next
	a.next++
	return a.members[a.cur].ArMember, nil
}

// Open returns the body of the member Next returned last, bounded by its declared size.
func (a *ArReader) Open() (io.Reader, error) {
	if a.cur < 0 {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "ar: Open before Next")
	}
	m := a.members[a.cur]
	return io.NewSectionReader(a.r, m.off, m.Size), nil
}
