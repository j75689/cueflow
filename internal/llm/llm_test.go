package llm

import "testing"

func TestIsOverflowText(t *testing.T) {
	for body, want := range map[string]bool{
		`{"error":{"type":"invalid_request_error","message":"prompt is too long: 1048213 tokens > 1000000 maximum"}}`:     true,
		`{"error":{"message":"input length and max_tokens exceed context limit: 990000 + 16000 > 1000000"}}`:              true,
		`{"message":"Input is too long for requested model."}`:                                                            true,
		`{"error":{"code":"context_length_exceeded","message":"..."}}`:                                                    true,
		`{"error":{"message":"Your input exceeds the context window of this model."}}`:                                    true,
		`{"message":"This model's maximum context length is 65536 tokens. However, you requested 70000 tokens."}`:         true,
		`{"error":{"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."}}`: true,
		`{"error":"context length exceeded"}`:                                                                             true,
		`{"error":{"type":"invalid_request_error","message":"tools.0.name: invalid"}}`:                                    false,
		`{"error":"rate limited"}`: false,
	} {
		if got := IsOverflowText(body); got != want {
			t.Errorf("%s: %v, want %v", body, got, want)
		}
	}
}
