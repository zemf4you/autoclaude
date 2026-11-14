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

// wrapMessage wraps the thinking blocks of a non-streaming response.
func wrapMessage(body []byte, model string) []byte {
	index := -1
	gjson.GetBytes(body, "content").ForEach(func(_, block gjson.Result) bool {
		index++
		if isThinking(block) {
			path := fmt.Sprintf("content.%d.%s", index, carrier(block))
			body, _ = sjson.SetBytes(body, path, wrap(model, block.Get(carrier(block)).String()))
		}
		return true
	})
	return body
}

// wrapStream wraps the thinking blocks of a streaming response as it is read.
//
// A provider may send the signature of a block in one signature_delta event,
// in several, or not at all. The stream withholds those events and sends a
// single wrapped signature right before the block closes. The data of a
// redacted block arrives whole, in the event that opens the block.
type wrapStream struct {
	source io.ReadCloser
	reader *bufio.Reader
	model  string
	// signatures holds the signature received so far for each open
	// thinking block, by block index.
	signatures map[int64]string
	out        bytes.Buffer
}

func newWrapStream(source io.ReadCloser, model string) *wrapStream {
	return &wrapStream{
		source:     source,
		reader:     bufio.NewReader(source),
		model:      model,
		signatures: map[int64]string{},
	}
}

func (s *wrapStream) Close() error { return s.source.Close() }

func (s *wrapStream) Read(p []byte) (int, error) {
	for s.out.Len() == 0 {
		event, err := s.readEvent()
		s.out.Write(s.wrapEvent(event))
		if err != nil && s.out.Len() == 0 {
			return 0, err
		}
	}
	return s.out.Read(p)
}

// readEvent reads one stream event: the lines up to and including a blank one.
func (s *wrapStream) readEvent() ([]byte, error) {
	var event []byte
	for {
		line, err := s.reader.ReadBytes('\n')
		event = append(event, line...)
		if err != nil || len(bytes.TrimRight(line, "\r\n")) == 0 {
			return event, err
		}
	}
}

// wrapEvent rewrites the events of thinking blocks and returns the rest as is.
func (s *wrapStream) wrapEvent(event []byte) []byte {
	data := eventData(event)
	index := data.Get("index").Int()
	switch data.Get("type").String() {
	case "content_block_start":
		switch block := data.Get("content_block"); block.Get("type").String() {
		case "thinking":
			s.signatures[index] = block.Get("signature").String()
		case "redacted_thinking":
			return setEventData(event, "content_block.data", wrap(s.model, block.Get("data").String()))
		}
	case "content_block_delta":
		if _, open := s.signatures[index]; open && data.Get("delta.type").String() == "signature_delta" {
			s.signatures[index] += data.Get("delta.signature").String()
			return nil
		}
	case "content_block_stop":
		if signature, open := s.signatures[index]; open {
			delete(s.signatures, index)
			return append(signatureEvent(index, wrap(s.model, signature)), event...)
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

// setEventData sets a field in the JSON payload of a stream event.
func setEventData(event []byte, path, value string) []byte {
	payload := strings.TrimSpace(eventData(event).Raw)
	changed, _ := sjson.Set(payload, path, value)
	return bytes.Replace(event, []byte(payload), []byte(changed), 1)
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
