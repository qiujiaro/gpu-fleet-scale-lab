package loadgen

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Record is one JSONL row written after the apiserver accepts a Pod.
type Record struct {
	Name     string    `json:"name"`
	UID      string    `json:"uid"`
	SubmitTS time.Time `json:"submit_ts"`
	Attempts int       `json:"attempts"`
}

// Recorder serializes concurrent worker results as one JSON object per line.
type Recorder struct {
	mu  sync.Mutex
	buf *bufio.Writer
	enc *json.Encoder
}

func NewRecorder(w io.Writer) *Recorder {
	buf := bufio.NewWriter(w)
	return &Recorder{
		buf: buf,
		enc: json.NewEncoder(buf),
	}
}

// Record writes exactly one complete JSONL row.
func (r *Recorder) Record(record Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enc.Encode(record)
}

// Close flushes buffered data when buffering is used.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Flush()
}
