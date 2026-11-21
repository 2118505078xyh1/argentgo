package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// TestThinkingOptionsForProtocol 验证思考档位被映射为协议对应的原生推理控制：
// 空档位与未知档位不产生任何覆盖，已识别档位按协议落到 effort 或 budget 字段。
func TestThinkingOptionsForProtocol(t *testing.T) {
	cases := []struct {
		name       string
		protocol   string
		thinking   string
		wantNil    bool
		wantEffort string
		wantBudget int64
	}{
		{name: "空档位返回 nil", protocol: string(provider.ProtocolOpenAI), thinking: "", wantNil: true},
		{name: "未知档位返回 nil", protocol: string(provider.ProtocolOpenAI), thinking: "nonsense", wantNil: true},
		{
			name:     "high 走 chat 得到 effort",
			protocol: string(provider.ProtocolOpenAI), thinking: "high", wantEffort: "high",
		},
		{
			name:     "中文高走 responses 得到 high",
			protocol: string(provider.ProtocolResponses), thinking: "高", wantEffort: "high",
		},
		{
			name:     "high 走 anthropic 得到正预算",
			protocol: string(provider.ProtocolAnthropic), thinking: "high", wantBudget: 4096,
		},
		{
			name: "未知协议返回 nil", protocol: "gemini", thinking: "high", wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := thinkingOptionsForProtocol(tc.protocol, tc.thinking)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("thinkingOptionsForProtocol(%q, %q) = %+v, want nil", tc.protocol, tc.thinking, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("thinkingOptionsForProtocol(%q, %q) = nil, want config", tc.protocol, tc.thinking)
			}
			if got.Effort != tc.wantEffort {
				t.Errorf("Effort = %q, want %q", got.Effort, tc.wantEffort)
			}
			if got.BudgetTokens != tc.wantBudget {
				t.Errorf("BudgetTokens = %d, want %d", got.BudgetTokens, tc.wantBudget)
			}
		})
	}
}
