package lzma

import "fmt"

// HeaderError is returned when the header is corrupt.
type HeaderError struct {
	msg string
}

// Error returns the error message and implements the error interface
// on the HeaderError type.
func (e HeaderError) Error() string {
	return "header error: " + e.msg
}

// StreamError is returned when the stream is corrupt.
type StreamError struct {
	msg string
}

// Error returns the error message and implements the error interface
// on the StreamError type.
func (e *StreamError) Error() string {
	return "stream error: " + e.msg
}

// NWriteError is returned when the number of bytes returned by Writer.Write() didn't meet expectances.
type NWriteError struct {
	msg string
}

// Error returns the error message and implements the error interface
// on the NWriteError type.
func (e *NWriteError) Error() string {
	return "number of bytes returned by Writer.Write() didn't meet expectances: " + e.msg
}

// An ArgumentValueError reports an error encountered while parsing user provided arguments.
type ArgumentValueError struct {
	msg string
	val any
}

// Error returns the error message and implements the error interface
// on the ArgumentValueError type.
func (e *ArgumentValueError) Error() string {
	return fmt.Sprintf("illegal argument value error: %s with value %v", e.msg, e.val)
}
