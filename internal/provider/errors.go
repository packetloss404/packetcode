package provider

import "io"

// MaxErrorBodyBytes caps provider error responses that are surfaced in UI
// errors. Providers can return arbitrarily large bodies from proxies or local
// endpoints; the agent only needs the leading diagnostic text.
const MaxErrorBodyBytes int64 = 64 << 10

const ErrorBodyTruncatedMarker = "\n...[provider error body truncated]..."

func ReadErrorBody(r io.Reader) []byte {
	body, _ := io.ReadAll(io.LimitReader(r, MaxErrorBodyBytes+1))
	if int64(len(body)) <= MaxErrorBodyBytes {
		return body
	}
	out := make([]byte, 0, int(MaxErrorBodyBytes)+len(ErrorBodyTruncatedMarker))
	out = append(out, body[:int(MaxErrorBodyBytes)]...)
	out = append(out, ErrorBodyTruncatedMarker...)
	return out
}
