package service

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"syscall"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var storageSpaceErrorPattern = regexp.MustCompile(`(?i)\bENOSPC\b|\[errno 28\]|no space left on device|disk quota exceeded`)

// Keep actionable public messages separate from raw transport diagnostics,
// which may contain internal addresses or credentials and belong in error_detail.
func docReaderFailure(err error) (code, message string) {
	var remoteErr *docparser.RemotePDFError
	if errors.As(err, &remoteErr) {
		return remoteErr.PublicCode(), remoteErr.PublicMessage()
	}
	// Remote parser errors may carry an OS error only as text. Match generic
	// error protocol markers, never document contents or business questions.
	if errors.Is(err, syscall.ENOSPC) || storageSpaceErrorPattern.MatchString(err.Error()) {
		return werrors.ErrCodeDocReaderStorageExhausted,
			"The document parsing service has insufficient storage or temporary space. " +
				"Ask an administrator to free or expand its disk and shared memory space, then retry."
	}
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded ||
		strings.Contains(err.Error(), "docreader call timeout") {
		return werrors.ErrCodeDocReaderTimeout,
			"Document parsing service timed out. Check service health and load before retrying."
	}
	var networkError *net.OpError
	if status.Code(err) == codes.Unavailable || errors.As(err, &networkError) {
		return werrors.ErrCodeDocReaderUnavailable,
			"Cannot connect to the document parsing service, or the connection was interrupted. " +
				"Check that DocReader is running and healthy, then retry."
	}
	return werrors.ErrCodeDocReaderParseFailed,
		"Document parsing failed. Check the file format and the DocReader logs for this attempt."
}

// Parser responses can report an error without a transport error. Preserve
// existing parser diagnostics, while giving resource failures the same safe
// public message and stable code as RPC failures.
func docReaderResultFailure(raw string) (code, message string) {
	code, message = docReaderFailure(errors.New(raw))
	if code != werrors.ErrCodeDocReaderStorageExhausted {
		return werrors.ErrCodeDocReaderParseFailed, raw
	}
	return code, message
}
