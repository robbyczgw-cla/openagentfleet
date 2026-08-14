package harness

import "testing"

func TestCodexLoginStatusConnected(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		output string
		want   bool
	}{
		{name: "chatgpt", output: "Logged in using ChatGPT\n", want: true},
		{name: "api key", output: "Logged in using an API key\n", want: true},
		{name: "not logged in", output: "Not logged in\n", want: false},
		{name: "diagnostic mentioning login", output: "status: user is not logged in", want: false},
		{name: "empty", output: "", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := codexLoginStatusConnected([]byte(test.output)); got != test.want {
				t.Fatalf("codexLoginStatusConnected(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}
