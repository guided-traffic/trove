// Package s3 implements blob.ObjectStore with the AWS SDK for Go v2 (R-STOR-030
// to R-STOR-033). It is the only package of trove that imports the SDK.
//
// All HTTP goes through a client from internal/outbound (purpose objectstore),
// so the trust store, timeouts and request log are the process's. The package
// adds what the SDK does not do for trove: the credential sources of R-STOR-031
// (static credentials read from files and re-read when they change, web
// identity, role assumption), the stall detection of R-STOR-033 (a transfer
// fails when no byte moves for storage.s3.http.idle_transfer_timeout) and the
// classification of errors for the circuit breaker (blob.StoreError).
//
// Multipart orchestration is not here: trove's own part loop lives in
// internal/blob and calls CreateMultipart, UploadPart, CompleteMultipart and
// AbortMultipart, so it reads the parts directly from the spool file (Q-ARCH-4).
package s3
