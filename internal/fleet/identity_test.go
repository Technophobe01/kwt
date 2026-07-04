package fleet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeHostID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"lowercase simple", "host-a", "host-a", false},
		{"trims and lowercases", " Host.A_01 ", "host.a_01", false},
		{"rejects spaces", "host a", "", true},
		{"rejects slash", "host/a", "", true},
		{"rejects empty", "   ", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeHostID(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeRepositoryIdentity(t *testing.T) {
	tests := map[string]string{
		"git@github.com:kenn-io/kwt.git":     "github.com/kenn-io/kwt",
		"https://github.com/kenn-io/kwt":     "github.com/kenn-io/kwt",
		"https://github.com/kenn-io/kwt.git": "github.com/kenn-io/kwt",
		"https://github.com/fork/kwt.git":    "github.com/fork/kwt",
	}
	for input, want := range tests {
		got, err := NormalizeRepositoryIdentity(input)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestNormalizeRepositoryIdentityRejectsInvalidInputs(t *testing.T) {
	for _, input := range []string{"", "github.com/kenn-io", "https://../kenn-io/kwt"} {
		t.Run(input, func(t *testing.T) {
			_, err := NormalizeRepositoryIdentity(input)
			require.Error(t, err)
		})
	}
}

func TestDefaultHostIDUsesHostname(t *testing.T) {
	got, err := DefaultHostID(func() (string, error) { return "Host.A", nil })
	require.NoError(t, err)
	assert.Equal(t, "host.a", got)
}
