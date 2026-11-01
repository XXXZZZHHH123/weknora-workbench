package service

import (
	"reflect"
	"testing"
)

func TestParseGeneratedQuestions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		count   int
		want    []string
	}{
		{
			name:    "numbered list",
			content: "1. What is WeKnora?\n2) How are chunks indexed?\n- ok\n",
			count:   3,
			want:    []string{"What is WeKnora?", "How are chunks indexed?"},
		},
		{
			name:    "numeric and negative body prefixes are preserved",
			content: "2024年棉花市场的主要变化是什么？\n-5.25吨的库存如何计算？\n- 5.25吨的库存如何计算？\n2024. 该编号属于正文",
			count:   4,
			want:    []string{"2024年棉花市场的主要变化是什么？", "-5.25吨的库存如何计算？", "- 5.25吨的库存如何计算？", "2024. 该编号属于正文"},
		},
		{
			name:    "numbered protocol strips only sequential list markers",
			content: "1. 2024年产量是多少？\n2、-5吨变化意味着什么？",
			count:   2,
			want:    []string{"2024年产量是多少？", "-5吨变化意味着什么？"},
		},
		{
			name:    "structured JSON array protocol",
			content: `["2024年产量是多少？", "-5吨变化意味着什么？"]`,
			count:   2,
			want:    []string{"2024年产量是多少？", "-5吨变化意味着什么？"},
		},
		{
			name:    "structured JSON object protocol",
			content: `{"questions":["2024年产量是多少？", "-5吨变化意味着什么？"]}`,
			count:   2,
			want:    []string{"2024年产量是多少？", "-5吨变化意味着什么？"},
		},
		{
			name:    "limited to question count",
			content: "First question?\nSecond question?\nThird question?",
			count:   2,
			want:    []string{"First question?", "Second question?"},
		},
		{
			name: "inline think block is not parsed as questions",
			content: "<think>\nThe chunk talks about indexing.\nI should ask about it.\n</think>\n" +
				"What is WeKnora?\nHow are chunks indexed?",
			count: 3,
			want:  []string{"What is WeKnora?", "How are chunks indexed?"},
		},
		{
			name:    "unterminated think block yields nothing",
			content: "<think>\nThe chunk talks about indexing.\nI should ask about it.",
			count:   3,
			want:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGeneratedQuestions(tt.content, tt.count)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("questions = %q, want %q", got, tt.want)
			}
		})
	}
}
