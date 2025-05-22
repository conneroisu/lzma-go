package lzma

import (
	"fmt"
	"io"
)

// syncPipeReader is a pipe that can be closed with an error.
type syncPipeReader struct {
	*io.PipeReader
	closeChan chan bool
}

func (sr *syncPipeReader) CloseWithError(err error) error {
	retErr := sr.PipeReader.CloseWithError(err)
	sr.closeChan <- true // finish writer close

	if retErr == nil {
		return nil
	}
	return fmt.Errorf("failed to close syncPipeReader: %v", retErr)
}

// syncPipeWriter is a pipe that can be closed with an error.
type syncPipeWriter struct {
	*io.PipeWriter
	closeChan chan bool
}

// Close closes the pipe with an error.
func (sw *syncPipeWriter) Close() error {
	err := sw.PipeWriter.Close()
	<-sw.closeChan // wait for reader close

	if err == nil {
		return nil
	}
	return fmt.Errorf("failed to close syncPipeWriter: %v", err)
}

// newSyncPipe creates a new syncPipeReader and syncPipeWriter.
func newSyncPipe() (*syncPipeReader, *syncPipeWriter) {
	r, w := io.Pipe()
	sr := &syncPipeReader{r, make(chan bool, 1)}
	sw := &syncPipeWriter{w, sr.closeChan}

	return sr, sw
}
