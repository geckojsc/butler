package neterr

import (
	"io"
	"net"
	"net/url"
)

// IsNetworkError returns true if the error's cause is: io.ErrUnexpectedEOF,
// any *net.OpError, any *url.Error, any URL that implements `Temporary()`
// (and returns true)
func IsNetworkError(err error) bool {
	if err == io.ErrUnexpectedEOF {
		return true
	}

	if causer, ok := err.(causer); ok {
		return IsNetworkError(causer.Cause())
	}

	if urlError, ok := err.(*url.Error); ok {
		return IsNetworkError(urlError.Err)
	}

	if _, ok := err.(*net.OpError); ok {
		return true
	}

	if te, ok := err.(temporary); ok {
		return te.Temporary()
	}

	return false
}

type temporary interface {
	Temporary() bool
}

type causer interface {
	Cause() error
}
