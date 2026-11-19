package store

import "testing"

func TestGrammarPointKeyStable(t *testing.T) {
	cases := map[string]string{
		"de/grammar/第二虚拟式.md":         "第二虚拟式",
		"de/grammar/Konjunktiv II.md": "konjunktiv-ii",
		"de/grammar/um_zu.md":         "um-zu",
	}
	for input, want := range cases {
		first := grammarPointKey(input)
		second := grammarPointKey(input)
		if first != second {
			t.Fatalf("key 不稳定: %q != %q", first, second)
		}
		if first != want {
			t.Fatalf("input=%q 期望 %q，实际 %q", input, want, first)
		}
	}
	if grammarPointKey("de/grammar/___.md") == "" {
		t.Fatalf("空 key 应回退为哈希值")
	}
}

func TestInt64ArrayRoundTrip(t *testing.T) {
	original := []int64{12, 13, 99}
	encoded := formatInt64Array(original)
	if encoded != "{12,13,99}" {
		t.Fatalf("编码结果错误: %s", encoded)
	}
	decoded := parseInt64Array(encoded)
	if len(decoded) != len(original) {
		t.Fatalf("解码长度错误: %v", decoded)
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Fatalf("解码结果错误: %v", decoded)
		}
	}

	if got := parseInt64Array(""); got != nil {
		t.Fatalf("空数组应返回 nil，实际 %v", got)
	}
	if got := parseInt64Array("{1,broken,2}"); len(got) != 2 {
		t.Fatalf("非法元素应被忽略: %v", got)
	}
}
