package deb

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// OutOfRange is the answer of a bound of R-DEB-009 crossed (64 suites, 32 components, 32 coordinates): 422 validation_failed with the
// field error out_of_range at pointer.
func OutOfRange(pointer, detail string) error {
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(errs.FieldError{Pointer: pointer, Code: "out_of_range", Detail: detail}))
}

type answerCoordinate struct {
	Distribution string `json:"distribution"`
	Component    string `json:"component"`
}

type answerIndex struct {
	Distribution string `json:"distribution"`
	ChangeSeq    int64  `json:"changeSeq"`
	PublishedSeq int64  `json:"publishedSeq"`
}

// Answer is the body of R-DEB-006: the stored file, its coordinates and the numbers of the suites it is listed in, which a client waits
// on with the index state read (R-DEB-014).
type Answer struct {
	Path         string             `json:"path"`
	Package      string             `json:"package"`
	Version      string             `json:"version"`
	Architecture string             `json:"architecture"`
	SHA256       string             `json:"sha256"`
	Coordinates  []answerCoordinate `json:"coordinates"`
	Index        []answerIndex      `json:"index"`
}

// answerOf renders the answer of an upload or of a change of coordinates.
func answerOf(path, pkg, version, arch string, sum []byte, res rows.UploadResult) Answer {
	a := Answer{
		Path: path, Package: pkg, Version: version, Architecture: arch, SHA256: hex.EncodeToString(sum),
		Coordinates: []answerCoordinate{}, Index: []answerIndex{},
	}
	for _, c := range res.Coordinates {
		a.Coordinates = append(a.Coordinates, answerCoordinate{Distribution: c.Suite, Component: c.Component})
	}
	for _, s := range res.Suites {
		a.Index = append(a.Index, answerIndex{Distribution: s.Suite, ChangeSeq: s.ChangeSeq, PublishedSeq: s.PublishedSeq})
	}
	return a
}

// AnswerOf is [answerOf] for the management API's change of coordinates.
func AnswerOf(path, pkg, version, arch string, sum []byte, res rows.UploadResult) Answer {
	return answerOf(path, pkg, version, arch, sum, res)
}

func writeAnswer(w http.ResponseWriter, status int, in *incoming, sum []byte, res rows.UploadResult) error {
	body, err := json.Marshal(answerOf(in.path, in.name(), in.version(), in.architecture(), sum, res))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}
