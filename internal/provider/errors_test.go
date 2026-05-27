package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadErrorBodyMarksTruncation(t *testing.T) {
	body := strings.Repeat("x", int(MaxErrorBodyBytes)+1)

	got := string(ReadErrorBody(strings.NewReader(body)))

	assert.Len(t, got, int(MaxErrorBodyBytes)+len(ErrorBodyTruncatedMarker))
	assert.True(t, strings.HasPrefix(got, strings.Repeat("x", int(MaxErrorBodyBytes))))
	assert.True(t, strings.HasSuffix(got, ErrorBodyTruncatedMarker))
}
