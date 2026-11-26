package handler

import (
	"strings"
	"testing"
)

func TestParsePlanNameCandidates(t *testing.T) {
	raw := "```json\n{\"candidates\":[\" 我过生日，邀请朋友来参加 \",\"在医院就诊时被询问生日\",\"\",\"我过生日，邀请朋友来参加\"]}\n```"
	got, err := parsePlanNameCandidates(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("候选应去重并去掉空项，得到 %v", got)
	}
	if got[0] != "我过生日，邀请朋友来参加" {
		t.Fatalf("候选应去除首尾空白：%q", got[0])
	}

	if _, err := parsePlanNameCandidates("{\"candidates\":[\"\",\"  \"]}"); err == nil {
		t.Fatal("全部候选为空时应报错")
	}
	if _, err := parsePlanNameCandidates("not json"); err == nil {
		t.Fatal("非 JSON 输出应报错")
	}
}

func TestParsePlanDescription(t *testing.T) {
	got, err := parsePlanDescription("{\"description\":\" 围绕生日场景，覆盖自我介绍与问答。 \"}")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got != "围绕生日场景，覆盖自我介绍与问答。" {
		t.Fatalf("说明应去除首尾空白：%q", got)
	}

	if _, err := parsePlanDescription("{\"description\":\" \"}"); err == nil {
		t.Fatal("空说明应报错")
	}

	long, err := parsePlanDescription("{\"description\":\"" + strings.Repeat("说", 2100) + "\"}")
	if err != nil {
		t.Fatalf("超长说明应截断而非报错：%v", err)
	}
	if len([]rune(long)) != maxGeneratedPlanDescRunes {
		t.Fatalf("说明应截断到 %d 字，实际 %d", maxGeneratedPlanDescRunes, len([]rune(long)))
	}
}
