package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStopTokenIDsDecodeBothSpellings(t *testing.T) {
	for _, key := range []string{"stopTokenIds", "stop_token_ids"} {
		t.Run(key, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
				key: []any{151643, 151644, 151645},
			})
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := []int64{151643, 151644, 151645}
			if !reflect.DeepEqual(cfg.StopTokenIDs, want) {
				t.Fatalf("StopTokenIDs = %v, want %v (key %q)", cfg.StopTokenIDs, want, key)
			}
		})
	}
}

func TestStopTokenIDsDefaultsEmpty(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.StopTokenIDs) != 0 {
		t.Fatalf("absent stopTokenIds = %v, want empty", cfg.StopTokenIDs)
	}

	// A wrongly-typed value is ignored rather than failing config load.
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"stopTokenIds": "151643",
	})
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load with string stopTokenIds: %v", err)
	}
	if len(cfg.StopTokenIDs) != 0 {
		t.Fatalf("string stopTokenIds = %v, want empty", cfg.StopTokenIDs)
	}
}

func TestStopTokenIDsLayeredOverride(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
		"stopTokenIds": []any{1, 2},
	})
	writeJSONFile(t, filepath.Join(cwd, ".argent", "config.json"), map[string]any{
		"stopTokenIds": []any{3},
	})
	env := envFunc(map[string]string{"HOME": home})

	// Arrays are replaced, not concatenated: the higher layer wins wholesale.
	cfg, err := Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []int64{3}; !reflect.DeepEqual(cfg.StopTokenIDs, want) {
		t.Fatalf("layered StopTokenIDs = %v, want %v", cfg.StopTokenIDs, want)
	}

	// Flag overrides sit above every file layer and accept a typed slice.
	cfg, err = LoadWithOverrides(cwd, env, map[string]any{
		"stopTokenIds": []int64{7, 8},
	})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if want := []int64{7, 8}; !reflect.DeepEqual(cfg.StopTokenIDs, want) {
		t.Fatalf("override StopTokenIDs = %v, want %v", cfg.StopTokenIDs, want)
	}
}

func TestStopTokenIDsSkipsInvalidMembers(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"stopTokenIds": []any{
			"151643", // numeric string is not a token id
			true,
			nil,
			3.7,                            // fractional
			float64(9223372036854775808.0), // 2^63, outside int64
			151644,
			151645,
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []int64{151644, 151645}; !reflect.DeepEqual(cfg.StopTokenIDs, want) {
		t.Fatalf("StopTokenIDs = %v, want %v", cfg.StopTokenIDs, want)
	}
}

func TestInt64SliceFieldAcceptsJSONNumberAndCopiesTypedSlice(t *testing.T) {
	tree := map[string]any{
		"stopTokenIds": []any{json.Number("151643"), json.Number("9.5"), int64(151644)},
	}
	if got, want := int64SliceField(tree, "stopTokenIds"), []int64{151643, 151644}; !reflect.DeepEqual(got, want) {
		t.Fatalf("json.Number slice = %v, want %v", got, want)
	}

	src := []int64{1, 2}
	tree = map[string]any{"stop_token_ids": src}
	got := int64SliceField(tree, "stop_token_ids")
	src[0] = 99
	if want := []int64{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("typed slice was aliased: %v, want %v", got, want)
	}
}

func TestContextWindowInfoDecodeBothSpellings(t *testing.T) {
	tests := []struct {
		name string
		tree map[string]any
		want ContextWindowInfo
	}{
		{
			name: "camelCase",
			tree: map[string]any{"contextWindowInfo": map[string]any{
				"ropeScaling": map[string]any{"factor": 4, "type": "linear", "originalMax": 4096},
			}},
			want: ContextWindowInfo{RopeScaling: RopeScaling{Factor: 4, Type: "linear", OriginalMax: 4096}},
		},
		{
			name: "snake_case",
			tree: map[string]any{"context_window_info": map[string]any{
				"rope_scaling": map[string]any{"factor": 2.5, "type": "dynamic", "original_max": 8192},
			}},
			want: ContextWindowInfo{RopeScaling: RopeScaling{Factor: 2.5, Type: "dynamic", OriginalMax: 8192}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), tt.tree)
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.ContextWindowInfo != tt.want {
				t.Fatalf("ContextWindowInfo = %+v, want %+v", cfg.ContextWindowInfo, tt.want)
			}
		})
	}
}

func TestContextWindowInfoLayeredMerge(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
		"contextWindowInfo": map[string]any{
			"ropeScaling": map[string]any{"factor": 4, "originalMax": 4096},
		},
	})
	writeJSONFile(t, filepath.Join(cwd, ".argent", "config.json"), map[string]any{
		"contextWindowInfo": map[string]any{
			"ropeScaling": map[string]any{"type": "linear"},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := ContextWindowInfo{RopeScaling: RopeScaling{Factor: 4, Type: "linear", OriginalMax: 4096}}
	if cfg.ContextWindowInfo != want {
		t.Fatalf("merged ContextWindowInfo = %+v, want %+v", cfg.ContextWindowInfo, want)
	}
}

func TestContextWindowInfoInvalidAndNumericStrings(t *testing.T) {
	tests := []struct {
		name string
		tree map[string]any
		want ContextWindowInfo
	}{
		{
			name: "absent",
			tree: map[string]any{},
			want: ContextWindowInfo{},
		},
		{
			name: "invalid types ignored",
			tree: map[string]any{"contextWindowInfo": map[string]any{
				"ropeScaling": map[string]any{"factor": "oops", "type": 42, "originalMax": true},
			}},
			want: ContextWindowInfo{},
		},
		{
			name: "wrongly typed objects ignored",
			tree: map[string]any{
				"contextWindowInfo":   "linear",
				"context_window_info": map[string]any{"rope_scaling": "linear"},
			},
			want: ContextWindowInfo{},
		},
		{
			name: "numeric strings accepted",
			tree: map[string]any{"context_window_info": map[string]any{
				"rope_scaling": map[string]any{"factor": "4.0", "original_max": "4096"},
			}},
			want: ContextWindowInfo{RopeScaling: RopeScaling{Factor: 4, OriginalMax: 4096}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), tt.tree)
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.ContextWindowInfo != tt.want {
				t.Fatalf("ContextWindowInfo = %+v, want %+v", cfg.ContextWindowInfo, tt.want)
			}
		})
	}
}
