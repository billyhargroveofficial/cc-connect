package pi

import (
	"bytes"
	"io"
	"testing"
)

func TestCappedStderrWriterDrainsOutputAcrossAndAfterCapacity(t *testing.T) {
	var writer cappedStderrWriter
	prefix := bytes.Repeat([]byte("p"), maxStderrSize-3)
	for _, chunk := range [][]byte{prefix, []byte("overflow"), []byte("after capacity")} {
		// os/exec uses io.Copy to drain child stderr. A short Write count stops
		// that drain with ErrShortWrite even when discarding bytes is deliberate.
		copied, err := io.Copy(&writer, bytes.NewReader(chunk))
		if err != nil || copied != int64(len(chunk)) {
			t.Fatalf("stderr was not fully drained: copied %d of %d bytes: %v", copied, len(chunk), err)
		}
	}
	if got, want := writer.String(), string(prefix)+"ove"; got != want {
		t.Fatalf("stderr cap changed: retained %d bytes, want %d", len(got), len(want))
	}
}
