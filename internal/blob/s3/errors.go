package s3

import (
	"context"
	"errors"
	"net/http"
	"strings"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"github.com/guided-traffic/trove/internal/blob"
)

// configCodes are the store's answers that retrying does not fix: wrong
// bucket, wrong or missing permission, wrong region or credentials.
func configCode(code string) bool {
	switch code {
	case "AccessDenied", "AllAccessDisabled", "NoSuchBucket", "InvalidBucketName", "InvalidAccessKeyId",
		"SignatureDoesNotMatch", "AuthorizationHeaderMalformed", "InvalidToken", "ExpiredToken",
		"InvalidRegion", "PermanentRedirect", "MethodNotAllowed", "NotImplemented", "AccountProblem":
		return true
	}
	return false
}

func transientCode(code string) bool {
	switch code {
	case "SlowDown", "RequestTimeout", "InternalError", "ServiceUnavailable", "Throttling", "ThrottlingException",
		"RequestTimeTooSkewed", "OperationAborted", "TooManyRequests":
		return true
	}
	return false
}

// mapError turns an SDK error into the classified errors of internal/blob. A
// missing key becomes blob.ErrNotFound; the caller's cancellation is returned
// as is.
func mapError(ctx context.Context, op, key string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	se := &blob.StoreError{Op: op, Key: key, Err: err}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		se.Status = re.HTTPStatusCode()
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		se.Code = ae.ErrorCode()
	}
	switch {
	case se.Code == "NoSuchKey" || se.Code == "NotFound" && op != "HeadBucket" || se.Status == http.StatusNotFound && se.Code != "NoSuchBucket" && se.Code != "NoSuchUpload":
		return blob.ErrNotFound
	case configCode(se.Code) || se.Status == http.StatusForbidden || se.Status == http.StatusUnauthorized:
		se.Kind = blob.KindConfig
	case se.Status >= 500 || se.Status == http.StatusTooManyRequests || transientCode(se.Code):
		se.Kind = blob.KindTransient
	case se.Status == 0:
		// No answer: a network error, a timeout or a credential problem.
		se.Kind = blob.KindTransient
	default:
		se.Kind = blob.KindOther
	}
	if se.Status == 0 && strings.Contains(err.Error(), "failed to retrieve credentials") {
		se.Kind = blob.KindTransient
	}
	return se
}
