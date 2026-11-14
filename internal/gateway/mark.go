package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A thinking block is accepted back only by the model that produced it:
// Claude verifies the signature, other models expect their own reasoning
// format. The history, however, lives on the client, and after a model
// switch it holds blocks from several authors.
//
// The gateway keeps no history, so it records the author in the block
// itself: the signature in a third-party model's response is prefixed with a
// mark naming that model. The client returns signatures verbatim, and the
// mark tells the gateway which blocks are native to the model being
// addressed. Blocks without a mark belong to Claude.

// markPrefix starts a signature marked by the gateway. Neither Claude's
// signatures (base64) nor model IDs contain a space, so the mark is
// unambiguous.
const markPrefix = "autoclaude "

// mark records in a signature the model that produced the block.
func mark(model, signature string) string {
	return markPrefix + model + " " + signature
}

// author returns the model that produced a block and the block's original
// signature. An empty author means Claude.
func author(signature string) (model, original string) {
	rest, marked := strings.CutPrefix(signature, markPrefix)
	if !marked {
		return "", signature
	}
	model, original, _ = strings.Cut(rest, " ")
	return model, original
}

// markMessage marks the thinking blocks of a non-streaming response.
func markMessage(body []byte, model string) []byte {
	index := -1
	gjson.GetBytes(body, "content").ForEach(func(_, block gjson.Result) bool {
		index++
		if block.Get("type").String() == "thinking" {
			path := fmt.Sprintf("content.%d.signature", index)
			body, _ = sjson.SetBytes(body, path, mark(model, block.Get("signature").String()))
		}
		return true
	})
	return body
}

// markStream marks the thinking blocks of a streaming response as it is read.
//
// A provider may send the signature of a block in one signature_delta event,
// in several, or not at all. The stream withholds those events and sends a
// single marked signature right before the block closes.
type markStream struct {
	source io.ReadCloser
	reader *bufio.Reader
	model  string
	// signatures holds the signature received so far for each open
	// thinking block, by block index.
	signatures map[int64]string
	out        bytes.Buffer
}

func newMarkStream(source io.ReadCloser, model string) *markStream {
	return &markStream{
		source:     source,
		reader:     bufio.NewReader(source),
		model:      model,
		signatures: map[int64]string{},
	}
}

func (s *markStream) Close() error { return s.source.Close() }

func (s *markStream) Read(p []byte) (int, error) {
	for s.out.Len() == 0 {
		event, err := s.readEvent()
		s.out.Write(s.markEvent(event))
		if err != nil && s.out.Len() == 0 {
			return 0, err
		}
	}
	return s.out.Read(p)
}

// readEvent reads one stream event: the lines up to and including a blank one.
func (s *markStream) readEvent() ([]byte, error) {
	var event []byte
	for {
		line, err := s.reader.ReadBytes('\n')
		event = append(event, line...)
		if err != nil || len(bytes.TrimRight(line, "\r\n")) == 0 {
			return event, err
		}
	}
}

// markEvent rewrites the events of thinking blocks and returns the rest as is.
func (s *markStream) markEvent(event []byte) []byte {
	data := eventData(event)
	index := data.Get("index").Int()
	switch data.Get("type").String() {
	case "content_block_start":
		if block := data.Get("content_block"); block.Get("type").String() == "thinking" {
			s.signatures[index] = block.Get("signature").String()
		}
	case "content_block_delta":
		if _, open := s.signatures[index]; open && data.Get("delta.type").String() == "signature_delta" {
			s.signatures[index] += data.Get("delta.signature").String()
			return nil
		}
	case "content_block_stop":
		if signature, open := s.signatures[index]; open {
			delete(s.signatures, index)
			return append(signatureEvent(index, mark(s.model, signature)), event...)
		}
	}
	return event
}

// eventData returns the JSON payload of a stream event.
func eventData(event []byte) gjson.Result {
	for line := range bytes.Lines(event) {
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			return gjson.ParseBytes(data)
		}
	}
	return gjson.Result{}
}

// signatureEvent builds the stream event that delivers a block's signature.
func signatureEvent(index int64, signature string) []byte {
	type delta struct {
		Type      string `json:"type"`
		Signature string `json:"signature"`
	}
	data, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Index int64  `json:"index"`
		Delta delta  `json:"delta"`
	}{"content_block_delta", index, delta{"signature_delta", signature}})
	return fmt.Appendf(nil, "event: content_block_delta\ndata: %s\n\n", data)
}
