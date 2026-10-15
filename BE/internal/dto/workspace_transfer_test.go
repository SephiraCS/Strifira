package dto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeTransferJSONRejectsTrailingValues(t *testing.T) {
	var target map[string]any
	require.Error(t, DecodeTransferJSON([]byte(`{"format":"strifira.workspace"} {"hidden":true}`), &target))
}

func TestDecodeTransferJSONAllowsTrailingWhitespace(t *testing.T) {
	var target map[string]any
	require.NoError(t, DecodeTransferJSON([]byte("{\"format\":\"strifira.workspace\"}\n\t"), &target))
	require.Equal(t, "strifira.workspace", target["format"])
}
