package s3

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	trove "github.com/guided-traffic/trove/internal/config"
)

// fileReread is how long static credentials are served from memory before the
// files are read again, so that a rotated Secret applies without a restart
// (R-STOR-031) and a request does not read three files.
const fileReread = time.Second

// awsConfig builds the SDK configuration: region, HTTP client, retryer,
// checksum behaviour and the credential source of R-STOR-031.
func awsConfig(ctx context.Context, cfg trove.StorageS3, hc *http.Client) (aws.Config, error) {
	reqCalc, respVal := aws.RequestChecksumCalculationWhenSupported, aws.ResponseChecksumValidationWhenSupported
	if cfg.Checksums.RequestCalculation == "when_required" {
		reqCalc = aws.RequestChecksumCalculationWhenRequired
	}
	if cfg.Checksums.ResponseValidation == "when_required" {
		respVal = aws.ResponseChecksumValidationWhenRequired
	}
	var ac aws.Config
	switch cfg.Credentials.Source {
	case "static", "webIdentity":
		ac = aws.Config{Region: cfg.Region}
	default:
		opts := []func(*config.LoadOptions) error{config.WithHTTPClient(hc)}
		if cfg.Region != "" {
			opts = append(opts, config.WithRegion(cfg.Region))
		}
		loaded, err := config.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return aws.Config{}, fmt.Errorf("load the default AWS configuration: %w", err)
		}
		ac = loaded
	}
	ac.HTTPClient = hc
	ac.Retryer = retryer(cfg.Retry)
	ac.RequestChecksumCalculation = reqCalc
	ac.ResponseChecksumValidation = respVal
	if ac.Region == "" {
		return aws.Config{}, fmt.Errorf("storage.s3.region is required (no region in the environment either)")
	}

	switch cfg.Credentials.Source {
	case "static":
		ac.Credentials = &fileCredentials{
			idFile: cfg.Credentials.AccessKeyIDFile, secretFile: cfg.Credentials.SecretAccessKeyFile,
			tokenFile: cfg.Credentials.SessionTokenFile, now: time.Now,
		}
	case "webIdentity":
		stsc := sts.NewFromConfig(ac, func(o *sts.Options) {
			if cfg.Credentials.STSEndpoint != "" {
				o.BaseEndpoint = aws.String(cfg.Credentials.STSEndpoint)
			}
		})
		ac.Credentials = aws.NewCredentialsCache(stscreds.NewWebIdentityRoleProvider(
			stsc, cfg.Credentials.RoleARN, stscreds.IdentityTokenFile(cfg.Credentials.WebIdentityTokenFile)))
		return ac, nil
	}
	if cfg.Credentials.RoleARN != "" {
		stsc := sts.NewFromConfig(ac, func(o *sts.Options) {
			if cfg.Credentials.STSEndpoint != "" {
				o.BaseEndpoint = aws.String(cfg.Credentials.STSEndpoint)
			}
		})
		ac.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(stsc, cfg.Credentials.RoleARN))
	}
	return ac, nil
}

// fileCredentials serves static credentials from files and reads them again
// when they may have changed.
type fileCredentials struct {
	idFile, secretFile, tokenFile string
	now                           func() time.Time

	mu      sync.Mutex
	read    time.Time
	current aws.Credentials
}

// Retrieve implements aws.CredentialsProvider.
func (f *fileCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.read.IsZero() && f.now().Sub(f.read) < fileReread {
		return f.current, nil
	}
	id, err := readSecret(f.idFile)
	if err != nil {
		return aws.Credentials{}, err
	}
	secret, err := readSecret(f.secretFile)
	if err != nil {
		return aws.Credentials{}, err
	}
	var token string
	if f.tokenFile != "" {
		if token, err = readSecret(f.tokenFile); err != nil {
			return aws.Credentials{}, err
		}
	}
	f.current = aws.Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token, Source: "trove static files"}
	f.read = f.now()
	return f.current, nil
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-configured secret file
	if err != nil {
		return "", fmt.Errorf("read object-store credential file: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("object-store credential file %s is empty", path)
	}
	return s, nil
}
