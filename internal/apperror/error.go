package apperror

import (
	"errors"
	"fmt"

	"toolctl/api/v1alpha1"
)

type Error struct {
	Code      v1alpha1.ErrorCode
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code v1alpha1.ErrorCode, message string) error {
	return &Error{Code: code, Message: message}
}

func Wrap(code v1alpha1.ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func Code(err error) v1alpha1.ErrorCode {
	var appErr *Error
	if errors.As(err, &appErr) && appErr.Code != "" {
		return appErr.Code
	}
	return v1alpha1.ErrorInternal
}

func Is(err error) bool {
	var appErr *Error
	return errors.As(err, &appErr)
}

func TargetError(err error) v1alpha1.TargetError {
	if err == nil {
		return v1alpha1.TargetError{}
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return v1alpha1.TargetError{
			Code:      appErr.Code,
			Message:   appErr.Error(),
			Retryable: appErr.Retryable,
			Details:   map[string]string{},
		}
	}
	return v1alpha1.TargetError{
		Code:    v1alpha1.ErrorInternal,
		Message: fmt.Sprintf("internal error: %v", err),
		Details: map[string]string{},
	}
}
