package util

import "testing"

func TestContainsString(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     string
		want  bool
	}{
		{
			name:  "contains string",
			slice: []string{"a", "b", "c"},
			s:     "b",
			want:  true,
		},
		{
			name:  "does not contain string",
			slice: []string{"a", "b", "c"},
			s:     "d",
			want:  false,
		},
		{
			name:  "empty slice",
			slice: []string{},
			s:     "a",
			want:  false,
		},
		{
			name:  "empty string",
			slice: []string{"", "a", "b"},
			s:     "",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsString(tt.slice, tt.s); got != tt.want {
				t.Errorf("ContainsString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRemoveString(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     string
		want  []string
	}{
		{
			name:  "remove existing string",
			slice: []string{"a", "b", "c"},
			s:     "b",
			want:  []string{"a", "c"},
		},
		{
			name:  "remove non-existing string",
			slice: []string{"a", "b", "c"},
			s:     "d",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "remove from empty slice",
			slice: []string{},
			s:     "a",
			want:  []string{},
		},
		{
			name:  "remove multiple occurrences",
			slice: []string{"a", "b", "a", "c", "a"},
			s:     "a",
			want:  []string{"b", "c"},
		},
		{
			name:  "remove empty string",
			slice: []string{"", "a", "", "b"},
			s:     "",
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveString(tt.slice, tt.s)
			if len(got) != len(tt.want) {
				t.Errorf("RemoveString() length = %v, want %v", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("RemoveString()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCapitalize(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want string
	}{
		{
			name: "lowercase word",
			s:    "hello",
			want: "Hello",
		},
		{
			name: "already capitalized",
			s:    "Hello",
			want: "Hello",
		},
		{
			name: "single character",
			s:    "a",
			want: "A",
		},
		{
			name: "empty string",
			s:    "",
			want: "",
		},
		{
			name: "all caps",
			s:    "HELLO",
			want: "HELLO",
		},
		{
			name: "number first",
			s:    "123abc",
			want: "123abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Capitalize(tt.s); got != tt.want {
				t.Errorf("Capitalize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{
			name:   "string shorter than max",
			s:      "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "string equal to max",
			s:      "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "string longer than max",
			s:      "hello world this is a long string",
			maxLen: 10,
			want:   "hello worl... (truncated)",
		},
		{
			name:   "truncate to zero",
			s:      "hello",
			maxLen: 0,
			want:   "... (truncated)",
		},
		{
			name:   "empty string",
			s:      "",
			maxLen: 5,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate(tt.s, tt.maxLen); got != tt.want {
				t.Errorf("Truncate() = %v, want %v", got, tt.want)
			}
		})
	}
}
