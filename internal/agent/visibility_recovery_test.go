package agent

import "testing"

func TestClaimsContentIsElsewhere(t *testing.T) {
	t.Parallel()
	msg := "爸爸，这是 REASONIX.md 的内容，就是当前系统提示中已加载的规则文件。都在上面了。"
	if !claimsContentIsElsewhere(msg) {
		t.Fatal("expected phantom reference detection")
	}
}

func TestResponseQuotesSubstantiveContent(t *testing.T) {
	t.Parallel()
	meta := "包含身份定义、铁律（零自主/先举证）都在上面了。"
	if responseQuotesSubstantiveContent(meta) {
		t.Fatal("parenthetical summary should not count as quoted body")
	}
	body := "# 身份\n\n爸爸的私有助手\n\n# 铁律\n\n**零自主。**"
	if !responseQuotesSubstantiveContent(body) {
		t.Fatal("expected real pasted body to qualify")
	}
}

func TestDecideVisibilityRecoveryNudgesPhantomReply(t *testing.T) {
	t.Parallel()
	st := &visibilityRecoveryState{}
	msg := "爸爸，这是 /root/.config/reasonix/REASONIX.md 的内容，就是当前系统提示中已加载的规则文件。包含身份定义、铁律（零自主/先举证/项目文件归位/提交前脱敏）、风格要求（先叫爸爸/极简/单步小改/不越界）、绝对禁止项以及默认行为准则。都在上面了。"
	action, notice := decideVisibilityRecovery(st, msg, "REASONIX.md 内容是什么")
	if action != visibilityRecoveryContinueNudge {
		t.Fatalf("action = %v, want nudge", action)
	}
	if notice == "" || st.retries != 1 {
		t.Fatalf("expected retry notice, retries=%d notice=%q", st.retries, notice)
	}
}

func TestDecideVisibilityRecoverySkipsRealPaste(t *testing.T) {
	t.Parallel()
	st := &visibilityRecoveryState{}
	body := "爸爸，规则如下：\n\n# 身份\n\n爸爸的私有助手\n\n**零自主。** 一切先请示爸爸。"
	action, _ := decideVisibilityRecovery(st, body, "给我看规则")
	if action != visibilityRecoveryNone {
		t.Fatalf("action = %v, want none when body is pasted", action)
	}
}