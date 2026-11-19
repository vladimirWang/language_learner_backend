package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDailyWordsCount(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", 5, false},
		{"1", 1, false},
		{"20", 20, false},
		{"0", 0, true},
		{"21", 0, true},
		{"-3", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, errMsg := parseDailyWordsCount(tc.raw)
		if tc.wantErr {
			if errMsg == "" {
				t.Fatalf("count=%q 期望报错，实际通过", tc.raw)
			}
			continue
		}
		if errMsg != "" {
			t.Fatalf("count=%q 期望通过，实际报错：%s", tc.raw, errMsg)
		}
		if got != tc.want {
			t.Fatalf("count=%q 期望 %d，实际 %d", tc.raw, tc.want, got)
		}
	}
}

func TestNormalizeGrammarRequirements(t *testing.T) {
	t.Run("空值返回空数组", func(t *testing.T) {
		out, msg := normalizeGrammarRequirements(nil)
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if string(out) != "[]" {
			t.Fatalf("期望 []，实际 %s", out)
		}
	})

	t.Run("合法要求填充默认值", func(t *testing.T) {
		raw := json.RawMessage(`[{
			"key":"konjunktiv2","label":"第二虚拟式","source_path":"de/grammar/第二虚拟式.md",
			"patterns":["\\bwürde\\w*\\b"],"hint_parts":["part2_presentation"]
		}]`)
		out, msg := normalizeGrammarRequirements(raw)
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		var items []map[string]any
		if err := json.Unmarshal(out, &items); err != nil {
			t.Fatalf("解析结果失败: %v", err)
		}
		if items[0]["min_uses"] != float64(1) {
			t.Fatalf("min_uses 默认值错误: %v", items[0]["min_uses"])
		}
		if items[0]["enforce"] != "block" {
			t.Fatalf("enforce 默认值错误: %v", items[0]["enforce"])
		}
	})

	t.Run("超过三条被拒绝", func(t *testing.T) {
		item := `{"key":"k%d","label":"l","patterns":["a"]}`
		raw := json.RawMessage("[" +
			strings.Replace(item, "%d", "1", 1) + "," +
			strings.Replace(item, "%d", "2", 1) + "," +
			strings.Replace(item, "%d", "3", 1) + "," +
			strings.Replace(item, "%d", "4", 1) + "]")
		if _, msg := normalizeGrammarRequirements(raw); !strings.Contains(msg, "at most 3") {
			t.Fatalf("期望超过 3 条报错，实际：%q", msg)
		}
	})

	t.Run("非法正则被拒绝", func(t *testing.T) {
		raw := json.RawMessage(`[{"key":"k","label":"l","patterns":["("]}]`)
		if _, msg := normalizeGrammarRequirements(raw); !strings.Contains(msg, "invalid") {
			t.Fatalf("期望正则报错，实际：%q", msg)
		}
	})

	t.Run("无规则时禁止阻断", func(t *testing.T) {
		raw := json.RawMessage(`[{"key":"k","label":"l","patterns":[],"enforce":"block"}]`)
		if _, msg := normalizeGrammarRequirements(raw); !strings.Contains(msg, "when enforce is block") {
			t.Fatalf("期望无 patterns 的 block 被拒绝，实际：%q", msg)
		}
	})

	t.Run("无规则时允许提示", func(t *testing.T) {
		raw := json.RawMessage(`[{"key":"k","label":"l","patterns":[],"enforce":"warn"}]`)
		out, msg := normalizeGrammarRequirements(raw)
		if msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if !strings.Contains(string(out), `"enforce":"warn"`) {
			t.Fatalf("结果未保留 warn: %s", out)
		}
	})

	t.Run("重复 key 被拒绝", func(t *testing.T) {
		raw := json.RawMessage(`[{"key":"k","label":"l","patterns":["a"]},{"key":"k","label":"m","patterns":["b"]}]`)
		if _, msg := normalizeGrammarRequirements(raw); !strings.Contains(msg, "duplicate") {
			t.Fatalf("期望重复 key 报错，实际：%q", msg)
		}
	})
}

func TestDedupeInt64(t *testing.T) {
	got := dedupeInt64([]int64{3, 1, 3, 0, -2, 1})
	if len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Fatalf("去重结果错误: %v", got)
	}
}

func TestDailyWordsOpenAPISpec(t *testing.T) {
	type operation struct {
		OperationID string `json:"operationId"`
		Parameters  []struct {
			Name     string `json:"name"`
			In       string `json:"in"`
			Required bool   `json:"required"`
			Schema   struct {
				Type    string   `json:"type"`
				Minimum *float64 `json:"minimum"`
				Maximum *float64 `json:"maximum"`
				Default *float64 `json:"default"`
			} `json:"schema"`
		} `json:"parameters"`
	}
	var spec struct {
		OpenAPI    string                          `json:"openapi"`
		Security   []map[string][]string           `json:"security"`
		Paths      map[string]map[string]operation `json:"paths"`
		Components struct {
			SecuritySchemes map[string]struct {
				Type string `json:"type"`
				In   string `json:"in"`
				Name string `json:"name"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(dailyWordsOpenAPI, &spec); err != nil {
		t.Fatalf("OpenAPI JSON 无法解析: %v", err)
	}
	if spec.OpenAPI != "3.0.0" {
		t.Fatalf("openapi 版本错误: %s", spec.OpenAPI)
	}
	if len(spec.Security) == 0 {
		t.Fatalf("缺少全局 security")
	}
	if _, ok := spec.Security[0]["ApiKeyAuth"]; !ok {
		t.Fatalf("全局 security 未要求 ApiKeyAuth: %+v", spec.Security)
	}
	scheme, ok := spec.Components.SecuritySchemes["ApiKeyAuth"]
	if !ok || scheme.Type != "apiKey" || scheme.In != "header" || scheme.Name != "X-API-Key" {
		t.Fatalf("securitySchemes 配置错误: %+v", scheme)
	}

	getDaily, ok := spec.Paths["/api/daily-words"]["get"]
	if !ok || getDaily.OperationID != "getDailyWords" {
		t.Fatalf("缺少 getDailyWords 操作")
	}
	learned, ok := spec.Paths["/api/daily-words/learned"]["post"]
	if !ok || learned.OperationID != "markDailyWordsLearned" {
		t.Fatalf("缺少 markDailyWordsLearned 操作")
	}

	var countFound bool
	for _, param := range getDaily.Parameters {
		if param.Name != "count" {
			continue
		}
		countFound = true
		if param.Schema.Type != "integer" || param.Schema.Minimum == nil || *param.Schema.Minimum != 1 ||
			param.Schema.Maximum == nil || *param.Schema.Maximum != 20 {
			t.Fatalf("count 参数范围与实现不一致: %+v", param.Schema)
		}
	}
	if !countFound {
		t.Fatalf("schema 缺少 count 参数")
	}
}
