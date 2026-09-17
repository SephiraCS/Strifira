package dto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeTransferJSONRejectsTrailingValues(t *testing.T) {
	var target map[string]any
	require.Error(t, DecodeTransferJSON([]byte(`{"format":"ku.workspace"} {"hidden":true}`), &target))
}

func TestDecodeTransferJSONAllowsTrailingWhitespace(t *testing.T) {
	var target map[string]any
	require.NoError(t, DecodeTransferJSON([]byte("{\"format\":\"ku.workspace\"}\n\t"), &target))
	require.Equal(t, "ku.workspace", target["format"])
}
