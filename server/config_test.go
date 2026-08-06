package main

import "testing"

func TestEnvInt(t *testing.T) {
	const def = 4096

	cases := []struct {
		name  string
		set   bool
		value string
		want  int
	}{
		{"Unset falls back", false, "", def},
		{"Empty falls back", true, "", def},
		{"Valid value wins", true, "128", 128},
		{"One is valid", true, "1", 1},
		{"Zero falls back", true, "0", def},
		{"Negative falls back", true, "-1", def},
		{"Unparsable falls back", true, "not-a-number", def},
		{"Suffixed value falls back", true, "8K", def},
		{"Float falls back", true, "1.5", def},
		{"Whitespace falls back", true, " 128 ", def},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			const name = "FAASBOX_TEST_ENV_INT"
			if tt.set {
				t.Setenv(name, tt.value)
			}
			if got := envInt(name, def); got != tt.want {
				t.Errorf("envInt(%q=%q) = %d, want %d", name, tt.value, got, tt.want)
			}
		})
	}
}

// TestTunableDefaults locks the documented defaults. They are published in
// .env.example and in docs/, so a silent drift here makes the documentation
// lie about the behaviour of an unconfigured instance.
func TestTunableDefaults(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"defaultMaxOutputSize", defaultMaxOutputSize, 1048576},
		{"defaultMaxBodySize", defaultMaxBodySize, 1048576},
		{"defaultMaxLoggedOutput", defaultMaxLoggedOutput, 8192},
		{"defaultMaxLoggedPayload", defaultMaxLoggedPayload, 4096},
		{"defaultMaxConcurrency", defaultMaxConcurrency, 4},
		{"defaultMaxLogRetention", defaultMaxLogRetention, 1000},
		{"defaultMaxFileView", defaultMaxFileView, 262144},
	}

	for _, tt := range cases {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}
